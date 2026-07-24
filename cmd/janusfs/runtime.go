package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/sarathsp06/janusfs/internal/api"
	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/engine"
	"github.com/sarathsp06/janusfs/internal/history"
	"github.com/sarathsp06/janusfs/internal/logging"
	"github.com/sarathsp06/janusfs/internal/mount"
	"github.com/sarathsp06/janusfs/internal/obs"
	"github.com/sarathsp06/janusfs/internal/provider"
	"github.com/sarathsp06/janusfs/internal/ui"
)

// mountRuntime is one live mount owned by the daemon: its FUSE adapter, the
// per-mount dashboard/API server, and the observability/history machinery
// behind them. The daemon holds one of these per mounted source and calls
// stop() to tear it down cleanly. Rule changes are applied on demand via
// reload() (there is no file watcher — see janusfs update / the dashboard's
// Reload button; docs/SPEC_AMENDMENTS.md 2026-07-21).
type mountRuntime struct {
	UUID       string
	Src        string
	Mountpoint string
	Label      string // friendly name for the dashboard; empty falls back to Src
	Token      string

	eng      *engine.Engine
	prov     *provider.RamCache
	recorder *obs.Recorder
	adapter  *mount.Adapter
	apiSrv   *api.Server
	hist     *history.Store
	cancel   context.CancelFunc
	done     chan struct{} // closed when the FUSE serve loop exits
	mountErr error
	logger   *slog.Logger
}

// reload recompiles the rule set from disk and invalidates the redaction cache
// so the next read reflects edited .janusignore/.janusmask files. Called by
// `janusfs update` and the dashboard (config save + Reload button).
func (rt *mountRuntime) reload() error {
	if rt.eng == nil {
		return nil
	}
	err := rt.eng.Reload(rt.Src)
	if rt.prov != nil {
		rt.prov.InvalidateAll()
	}
	if rt.recorder != nil {
		rt.recorder.SetGeneration(rt.eng.Generation())
		rt.recorder.IncConfigReloads()
	}
	return err
}

// startMount builds the full per-mount stack for an already-resolved and
// validated cfg (Mountpoint set), launches the FUSE serve loop in a
// goroutine, and returns once the mount is live (OnMounted fired) or errors
// if the mount could not be established. Per-mount HTTP listeners are removed
// because all routing is now consolidated within the single daemon server.
func startMount(parent context.Context, cfg config.Config, debug bool) (*mountRuntime, error) {
	logger := logging.New("mount")

	errLog := log.New(logWriter{logger, slog.LevelError}, "", 0)
	var dbgLog *log.Logger
	if debug {
		dbgLog = log.New(logWriter{logger, slog.LevelDebug}, "", 0)
	}

	eng, err := engine.New(cfg.Src)
	if err != nil {
		return nil, fmt.Errorf("compiling rule set: %w", err)
	}
	prov := provider.NewRamCache(cfg.CacheMaxBytes, cfg.CacheMaxFile, cfg.RedactBufferMax)

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generating bearer token: %w", err)
	}
	bearerToken := hex.EncodeToString(tokenBytes)

	ctx, cancel := context.WithCancel(parent)

	rt := &mountRuntime{
		UUID:       uuid.New().String(),
		Src:        cfg.Src,
		Mountpoint: cfg.Mountpoint,
		Token:      bearerToken,
		eng:        eng,
		prov:       prov,
		cancel:     cancel,
		done:       make(chan struct{}),
		logger:     logger,
	}

	if !cfg.NoHistory {
		hd := historyDBPath(cfg.Src)
		h, hErr := history.Open(cfg.Src, hd, cfg.HistoryRetentionDays)
		if hErr != nil {
			logger.Warn("failed to open history DB, continuing without persistence", "error", hErr)
		} else if h != nil {
			rt.hist = h
			logger.Info("history DB opened", "path", hd)
		}
	}

	recorder := obs.NewRecorder(rt.hist)
	recorder.SetGeneration(eng.Generation())
	rt.recorder = recorder

	apiSrv := api.New(ui.FS, bearerToken, recorder.Registry(), rt.hist)
	apiSrv.SetMountInfo(cfg.Src, cfg.Mountpoint)
	apiSrv.SetVFSMeta(cfg.Src, func() (int, int64, uint64, uint64, uint64) {
		ps := prov.Stats()
		return ps.Entries, ps.Bytes, ps.Hits, ps.Misses, ps.Rebuilds
	}, func(relPath string, isDir bool) (string, []string, string) {
		res := eng.Resolve(relPath, isDir)
		return res.Decision.String(), res.PatternNames, res.RuleRef
	}, func() bool { return true }, eng.Generation) // no watcher; rules reload on demand
	apiSrv.SetReload(rt.reload)
	rt.apiSrv = apiSrv

	ready := make(chan error, 1)
	rt.adapter = &mount.Adapter{
		Engine:      eng,
		Provider:    prov,
		ErrorLogger: errLog,
		DebugLogger: dbgLog,
		OnMounted: func() {
			logger.Info("successfully mounted", "src", cfg.Src, "mountpoint", cfg.Mountpoint)
			ready <- nil
		},
		Observe: makeObserver(recorder),
	}

	go func() {
		err := rt.adapter.Mount(ctx, cfg.Src, cfg.Mountpoint)
		rt.mountErr = err
		// Signal failure only if OnMounted never fired (fs.Mount failed
		// before the mount went live); on a normal unmount OnMounted has
		// already put nil in the buffer and this send hits the default.
		select {
		case ready <- err:
		default:
		}
		close(rt.done)
	}()

	if err := <-ready; err != nil {
		rt.stop()
		return nil, fmt.Errorf("mounting %s: %w", cfg.Src, err)
	}
	return rt, nil
}

// stop tears down the mount: cancels the context (asking go-fuse to unmount),
// waits up to the grace window for the serve loop to exit, force-unmounts if
// it drags (FR-2), then closes history/event resources. Nil-safe so it doubles
// as the cleanup path for a mount that failed partway through startMount.
func (rt *mountRuntime) stop() {
	if rt.cancel != nil {
		rt.cancel()
	}
	if rt.adapter != nil && rt.done != nil {
		select {
		case <-rt.done:
			// graceful unmount completed within the grace window
		case <-time.After(shutdownGrace):
			// FR-2: force the unmount at the OS level, then give the serve loop
			// a moment to observe it. Calling server.Unmount again is not enough
			// when macFUSE leaves a busy/stale mount behind.
			if err := unmountKernel(rt.Mountpoint, true); err != nil && rt.logger != nil {
				rt.logger.Warn("force unmount failed", "mountpoint", rt.Mountpoint, "error", err)
			}
			select {
			case <-rt.done:
			case <-time.After(forceUnmountSettle):
				if rt.logger != nil {
					if mountpointMounted(rt.Mountpoint) {
						rt.logger.Warn("mount serve loop did not exit after force unmount", "mountpoint", rt.Mountpoint)
					} else {
						rt.logger.Debug("mount serve loop still settling after detached force unmount", "mountpoint", rt.Mountpoint)
					}
				}
			}
		}
	}

	if rt.recorder != nil {
		rt.recorder.Close()
	}
	if rt.hist != nil {
		rt.hist.Close()
	}
}

// makeObserver adapts mount.OpEvent (the adapter's wire vocabulary) into
// obs.Event and emits it through the recorder.
func makeObserver(recorder *obs.Recorder) func(mount.OpEvent) {
	return func(evt mount.OpEvent) {
		var d obs.Decision
		var err error
		switch evt.Decision {
		case "ALLOWED":
			d = obs.Allowed
		case "MASKED":
			d = obs.Masked
		case "HIDDEN":
			d = obs.Hidden
		case "PANIC":
			d = obs.Hidden
			err = fmt.Errorf("janusfs: panic recovered during %s", evt.Op)
		case "CONFIG_READONLY":
			d = obs.Hidden
			err = fmt.Errorf("janusfs: config file is read-only")
		default:
			return
		}
		var c obs.CacheResult
		switch evt.Cache {
		case "hit":
			c = obs.CacheHit
		case "miss":
			c = obs.CacheMiss
		case "rebuild":
			c = obs.CacheRebuild
		default:
			c = obs.CacheNA
		}
		recorder.Emit(obs.Event{
			TS:        time.Now(),
			Op:        obs.Op(evt.Op),
			Path:      evt.Path,
			Decision:  d,
			Bytes:     evt.Bytes,
			LatencyUs: evt.LatencyUs,
			Cache:     c,
			Err:       err,
		})
	}
}
