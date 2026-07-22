package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"time"

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
	Src        string
	Mountpoint string
	Label      string // friendly name for the dashboard; empty falls back to Src
	Token      string
	UIPort     int // actual bound dashboard port

	eng      *engine.Engine
	prov     *provider.RamCache
	metrics  *obs.JanusMetrics
	adapter  *mount.Adapter
	apiSrv   *api.Server
	hist     *history.Store
	eventBus *obs.EventBus
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
	if rt.metrics != nil {
		rt.metrics.Generation.Store(rt.eng.Generation())
	}
	return err
}

// startMount builds the full per-mount stack for an already-resolved and
// validated cfg (Mountpoint set), launches the FUSE serve loop in a
// goroutine, and returns once the mount is live (OnMounted fired) or errors
// if the mount could not be established. cfg.UIPort == 0 auto-assigns a free
// dashboard port, read back into the returned runtime's UIPort.
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

	eventBus := obs.NewEventBus(4096)
	metrics := &obs.JanusMetrics{}
	ringBuf := obs.NewRingBuffer(8192)
	topN := obs.NewTopN(1000)

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		eventBus.Close()
		return nil, fmt.Errorf("generating bearer token: %w", err)
	}
	bearerToken := hex.EncodeToString(tokenBytes)

	ctx, cancel := context.WithCancel(parent)

	rt := &mountRuntime{
		Src:        cfg.Src,
		Mountpoint: cfg.Mountpoint,
		Token:      bearerToken,
		eng:        eng,
		prov:       prov,
		metrics:    metrics,
		eventBus:   eventBus,
		cancel:     cancel,
		done:       make(chan struct{}),
		logger:     logger,
	}

	metrics.Generation.Store(eng.Generation())

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

	apiSrv := api.New(ui.FS, bearerToken, metrics, ringBuf, topN, eventBus, rt.hist)
	apiSrv.SetMountInfo(cfg.Src, cfg.Mountpoint)
	apiSrv.SetVFSMeta(cfg.Src, func() (int, int64, uint64, uint64, uint64) {
		ps := prov.Stats()
		return ps.Entries, ps.Bytes, ps.Hits, ps.Misses, ps.Rebuilds
	}, func(relPath string, isDir bool) (string, []string, string) {
		res := eng.Resolve(relPath, isDir)
		return res.Decision.String(), res.PatternNames, res.RuleRef
	}, func() bool { return true }) // no watcher; rules reload on demand
	apiSrv.SetReload(rt.reload)
	rt.apiSrv = apiSrv

	apiAddr := fmt.Sprintf("0.0.0.0:%d", cfg.UIPort)
	apiLn, err := apiSrv.Listen(apiAddr)
	if err != nil {
		rt.stop() // nil-safe: tears down watcher, event bus, history, api server
		return nil, fmt.Errorf("dashboard port %d unavailable: %w", cfg.UIPort, err)
	}
	if tcp, ok := apiLn.Addr().(*net.TCPAddr); ok {
		rt.UIPort = tcp.Port
	}
	go func() {
		al := logging.New("api")
		if err := apiSrv.Serve(apiLn); err != nil && err != http.ErrServerClosed {
			al.Error("API server error", "error", err)
		}
	}()

	go drainEvents(eventBus, metrics, ringBuf, topN, rt.hist)

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
		Observe: makeObserver(eventBus),
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
// it drags (FR-2), then shuts down the API server and closes history/event
// resources. Nil-safe so it doubles as the cleanup path for a mount that
// failed partway through startMount.
func (rt *mountRuntime) stop() {
	if rt.cancel != nil {
		rt.cancel()
	}
	if rt.adapter != nil && rt.done != nil {
		select {
		case <-rt.done:
			// graceful unmount completed within the grace window
		case <-time.After(shutdownGrace):
			// FR-2: force the unmount, then give the serve loop a moment.
			_ = rt.adapter.Unmount(rt.Mountpoint)
			select {
			case <-rt.done:
			case <-time.After(2 * time.Second):
				if rt.logger != nil {
					rt.logger.Warn("mount serve loop did not exit after force unmount", "mountpoint", rt.Mountpoint)
				}
			}
		}
	}

	if rt.apiSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		rt.apiSrv.Shutdown(shutdownCtx)
		cancel()
	}
	if rt.eventBus != nil {
		rt.eventBus.Close()
	}
	if rt.hist != nil {
		rt.hist.Close()
	}
}

// drainEvents fans every FUSE event into the obs components and history store
// so FUSE handlers never block on observability (FR-22). Returns when the bus
// is closed.
func drainEvents(bus *obs.EventBus, metrics *obs.JanusMetrics, ring *obs.RingBuffer, topN *obs.TopN, hist *history.Store) {
	for e := range bus.Events() {
		metrics.RecordOp(e.Op, e.Decision)
		if e.Bytes > 0 {
			metrics.RecordBytes(e.Decision, e.Bytes)
		}
		if e.LatencyUs > 0 {
			metrics.RecordLatency(e.Op, e.LatencyUs)
		}
		switch e.Cache {
		case obs.CacheHit:
			metrics.CacheHit.Add(1)
		case obs.CacheMiss:
			metrics.CacheMiss.Add(1)
		case obs.CacheRebuild:
			metrics.CacheRebuild.Add(1)
		}
		ring.Push(e.Label())
		if e.Decision == obs.Masked || e.Decision == obs.Allowed {
			topN.Record(e.Path, e.Bytes)
		}
		if hist != nil {
			hist.Record(e)
		}
	}
}

// makeObserver adapts mount.OpEvent (the adapter's wire vocabulary) into
// obs.Event and emits it on the bus.
func makeObserver(bus *obs.EventBus) func(mount.OpEvent) {
	return func(evt mount.OpEvent) {
		var d obs.Decision
		switch evt.Decision {
		case "ALLOWED":
			d = obs.Allowed
		case "MASKED":
			d = obs.Masked
		case "HIDDEN":
			d = obs.Hidden
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
		bus.Emit(obs.Event{
			TS:        time.Now(),
			Op:        obs.Op(evt.Op),
			Path:      evt.Path,
			Decision:  d,
			Bytes:     evt.Bytes,
			LatencyUs: evt.LatencyUs,
			Cache:     c,
		})
	}
}
