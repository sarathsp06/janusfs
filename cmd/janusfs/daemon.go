package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/control"
	"github.com/sarathsp06/janusfs/internal/logging"
)

// daemonRequest, daemonResponse, and mountStatus are the local names for the
// shared control-socket protocol types (internal/control), kept as aliases so
// every existing call site in this package (mount.go, umount.go, paths.go,
// the tests) compiles unchanged. The single source of truth for the protocol
// itself is internal/control, imported by both this package and
// internal/execrunner, so the two can no longer drift apart.
type (
	daemonRequest  = control.Request
	daemonResponse = control.Response
	mountStatus    = control.MountStatus
)

func newDaemonCmd() *cobra.Command {
	var debug, noOpen, background bool
	var indexPort int

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the JanusFS daemon: owns every mount, resumes past ones, serves the dashboard",
		Long: "Starts the long-running JanusFS process. It resumes every mount recorded by a\n" +
			"previous `janusfs mount` (and not explicitly unmounted), serves a combined\n" +
			"dashboard listing them all, and accepts `janusfs mount`/`umount` commands over\n" +
			"a local control socket. Runs in the foreground until Ctrl-C (then unmounts\n" +
			"everything cleanly), or pass --background to detach and log to a file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if background {
				return startDaemonBackground(debug, indexPort)
			}
			return runDaemon(cmd.Context(), debug, noOpen, indexPort)
		},
	}
	cmd.Flags().BoolVar(&debug, "debug", false, "verbose debug logging")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open the dashboard in a browser on start")
	cmd.Flags().BoolVar(&background, "background", false, "detach from the terminal and run in the background, logging to ~/.janusfs/logs/daemon.log")
	cmd.Flags().IntVar(&indexPort, "ui-port", config.DefaultUIPort, "port for the combined dashboard (binds 127.0.0.1)")
	return cmd
}

// daemon owns every live mount in one process and multiplexes control
// commands from `janusfs mount`/`umount` clients onto them.
var validateMountConfig = func(cfg config.Config) error { return cfg.Validate() }
var startMountFunc = startMount

type daemon struct {
	opsMu       sync.Mutex               // serializes mount/unmount so check-then-act is atomic
	mu          sync.Mutex               // guards the mounts map for concurrent reads
	mounts      map[string]*mountRuntime // keyed by absolute mountpoint
	base        config.Config            // resolved defaults (mount root, cache sizing, …)
	debug       bool
	ctx         context.Context
	logger      *slog.Logger
	uiPort      int
	indexLn     net.Listener
	indexer     *http.Server
	watchdogPID int // 0 if no watchdog was spawned (spawn failure is non-fatal)
}

func runDaemon(parent context.Context, debug, noOpen bool, indexPort int) error {
	sock, err := control.SocketPath()
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	// Refuse a second daemon: if the socket answers, one is already running.
	conn, derr := net.Dial("unix", sock)
	if derr == nil {
		// Query the running daemon for its mount list.
		enc := json.NewEncoder(conn)
		_ = enc.Encode(daemonRequest{Cmd: "list"})
		var resp daemonResponse
		_ = json.NewDecoder(conn).Decode(&resp)
		_ = conn.Close()

		fmt.Fprintf(os.Stderr, "JanusFS daemon is already running.\n")
		fmt.Fprintf(os.Stderr, "  Dashboard: http://127.0.0.1:%d/\n", indexPort)
		if resp.OK && len(resp.Mounts) > 0 {
			fmt.Fprintf(os.Stderr, "  Mounts:\n")
			for _, m := range resp.Mounts {
				fmt.Fprintf(os.Stderr, "    %s → %s\n", m.Src, m.Dashboard)
			}
		}
		return nil
	}
	// Stale socket from a crashed daemon — remove before rebinding.
	_ = os.Remove(sock)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return fmt.Errorf("daemon: creating %s: %w", filepath.Dir(sock), err)
	}

	logging.SetOutput(os.Stderr, logLevel(debug))
	logger := logging.New("daemon")

	base := config.Default()
	if err := config.ApplyFile(&base); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	if err := config.ApplyEnv(&base); err != nil {
		return fmt.Errorf("daemon: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	d := &daemon{
		mounts: map[string]*mountRuntime{},
		base:   base,
		debug:  debug,
		ctx:    ctx,
		logger: logger,
		uiPort: indexPort,
	}

	// Combined dashboard (127.0.0.1 only: it links to per-mount dashboards).
	indexAddr := fmt.Sprintf("127.0.0.1:%d", indexPort)
	ln, err := net.Listen("tcp", indexAddr)
	if err != nil {
		return fmt.Errorf("daemon: dashboard port %d unavailable: %w", indexPort, err)
	}
	d.indexLn = ln
	d.indexer = &http.Server{Handler: http.HandlerFunc(d.handleHTTP)}
	go func() {
		if err := d.indexer.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.Error("dashboard server error", "error", err)
		}
	}()
	logger.Info("daemon dashboard listening", "url", fmt.Sprintf("http://%s/", indexAddr))

	// Resume recorded mounts before accepting new commands.
	d.resume()

	lc, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("daemon: binding control socket %s: %w", sock, err)
	}
	logger.Info("control socket listening", "path", sock)

	fmt.Printf("JanusFS daemon running.\n  Dashboard: http://%s/\n  Control:   %s\n", indexAddr, sock)
	if !noOpen {
		_ = exec.Command("open", fmt.Sprintf("http://%s/", indexAddr)).Start()
	}

	spawnWatchdog(d, logger)

	// Accept control connections until shutdown.
	go d.acceptLoop(lc)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
		logger.Info("shutdown signal received, unmounting everything")
	case <-ctx.Done():
	}

	cancel()
	_ = lc.Close()
	_ = os.Remove(sock)
	d.shutdown()
	return nil
}

func (d *daemon) acceptLoop(lc net.Listener) {
	for {
		conn, err := lc.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go d.handleConn(conn)
	}
}

func (d *daemon) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var req daemonRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		control.WriteResponse(conn, daemonResponse{Error: "bad request: " + err.Error()})
		return
	}
	switch req.Cmd {
	case "mount":
		control.WriteResponse(conn, d.doMount(req))
	case "unmount":
		control.WriteResponse(conn, d.doUnmount(req))
	case "list":
		control.WriteResponse(conn, daemonResponse{OK: true, Mounts: d.snapshot()})
	case "reload":
		control.WriteResponse(conn, d.doReload(req))
	default:
		control.WriteResponse(conn, daemonResponse{Error: "unknown command: " + req.Cmd})
	}
}

// doReload recompiles the rule set for the mount matching req.Mountpoint (by
// mountpoint or src), or every mount when it's empty.
func (d *daemon) doReload(req daemonRequest) daemonResponse {
	d.mu.Lock()
	var targets []*mountRuntime
	if req.Mountpoint == "" {
		for _, rt := range d.mounts {
			targets = append(targets, rt)
		}
	} else {
		// Match the mount by its mountpoint, its source, OR any path inside
		// either tree — so `janusfs update <the .janusmask you just edited>`
		// resolves to the mount that owns it. Most specific (longest matching
		// base) wins.
		want, _ := filepath.Abs(req.Mountpoint)
		var best *mountRuntime
		bestLen := -1
		for mp, rt := range d.mounts {
			src, _ := filepath.Abs(rt.Src)
			for _, base := range []string{src, mp} {
				if want == base || strings.HasPrefix(want, base+string(filepath.Separator)) {
					if len(base) > bestLen {
						best, bestLen = rt, len(base)
					}
				}
			}
		}
		if best != nil {
			targets = append(targets, best)
		}
	}
	d.mu.Unlock()

	if len(targets) == 0 {
		if req.Mountpoint == "" {
			return daemonResponse{OK: true, Message: "no mounts to reload"}
		}
		return daemonResponse{Error: fmt.Sprintf("%q is not mounted by this daemon", req.Mountpoint)}
	}
	var failed int
	for _, rt := range targets {
		if err := rt.reload(); err != nil {
			failed++
			d.logger.Warn("reload failed", "mountpoint", rt.Mountpoint, "error", err)
		}
	}
	if failed > 0 {
		return daemonResponse{Error: fmt.Sprintf("%d of %d mount(s) failed to reload; see daemon log", failed, len(targets))}
	}
	return daemonResponse{OK: true, Message: fmt.Sprintf("reloaded %d mount(s)", len(targets))}
}

func (d *daemon) doMount(req daemonRequest) daemonResponse {
	// Serialize the whole check-resolve-start-insert path so two concurrent
	// mounts of the same mountpoint can't both pass the "already mounted"
	// guard and leak a runtime.
	d.opsMu.Lock()
	defer d.opsMu.Unlock()

	if req.Src == "" {
		return daemonResponse{Error: "src is required"}
	}
	cfg := d.base
	cfg.Src = req.Src
	cfg.Mountpoint = req.Mountpoint // empty for a client mount (derived); set only on resume
	cfg.NoHistory = req.NoHistory || d.base.NoHistory

	if err := cfg.ResolveMountpoint(); err != nil {
		return daemonResponse{Error: err.Error()}
	}
	if cfg.Mountpoint == "" {
		return daemonResponse{Error: hintNoMountRoot}
	}
	if req.Resume && req.Mountpoint != "" {
		if err := os.MkdirAll(cfg.Mountpoint, 0o700); err != nil {
			return daemonResponse{Error: fmt.Sprintf("creating recorded mountpoint %s: %v", cfg.Mountpoint, err)}
		}
	}
	mpAbs, err := filepath.Abs(cfg.Mountpoint)
	if err != nil {
		return daemonResponse{Error: err.Error()}
	}
	cfg.Mountpoint = mpAbs

	d.mu.Lock()
	_, exists := d.mounts[mpAbs]
	d.mu.Unlock()
	if exists {
		return daemonResponse{Error: fmt.Sprintf("%s is already mounted", mpAbs)}
	}

	if err := validateMountConfig(cfg); err != nil {
		if errors.Is(err, syscall.ENXIO) {
			if d.logger != nil {
				d.logger.Warn("clearing stale or broken mount before retry", "mountpoint", mpAbs, "error", err)
			}
			_ = unmountKernel(mpAbs, true)
			if mkErr := os.MkdirAll(mpAbs, 0o700); mkErr != nil {
				return daemonResponse{Error: fmt.Sprintf("creating mountpoint %s after stale cleanup: %v", mpAbs, mkErr)}
			}
			if retryErr := validateMountConfig(cfg); retryErr != nil {
				return daemonResponse{Error: d.mountValidationError(req.Src, mpAbs, retryErr)}
			}
		} else {
			return daemonResponse{Error: d.mountValidationError(req.Src, mpAbs, err)}
		}
	}

	rt, err := startMountFunc(d.ctx, cfg, d.debug)
	if err != nil {
		return daemonResponse{Error: err.Error()}
	}
	rt.Label = req.Label

	d.mu.Lock()
	d.mounts[mpAbs] = rt
	d.mu.Unlock()

	d.recordMount(rt)

	return daemonResponse{
		OK:      true,
		Message: fmt.Sprintf("mounted %s -> %s", cfg.Src, mpAbs),
		Mounts:  []mountStatus{d.status(rt)},
	}
}

// recordMount persists everything a now-live mount owns on disk: the pidfile
// (the double-mount guard and `janusfs doctor` read it) and the registry entry
// (resume reads it at the next daemon start). One place answers "what does a
// live mount leave on disk", so the doMount path stays a single call.
func (d *daemon) recordMount(rt *mountRuntime) {
	_ = writePidfile(rt.Mountpoint) // records the daemon PID; keeps the double-mount guard working
	srcAbs, _ := filepath.Abs(rt.Src)
	if err := config.RecordMount(srcAbs, rt.Mountpoint, rt.Label); err != nil {
		d.logger.Warn("failed to record mount", "error", err)
	}
}

// forgetMount removes everything recordMount persisted, plus the now-empty
// mirror directories — the mount is gone for good and must not resume. This is
// deliberately distinct from shutdown's pidfile-only cleanup, which keeps the
// registry entry so the mount resumes on the next start.
func (d *daemon) forgetMount(mp string) {
	_ = removePidfile(mp)
	pruneMirrorDirs(mp, d.base.MountRoot)
	if err := config.RemoveMount(mp); err != nil {
		d.logger.Warn("failed to update mounts registry", "error", err)
	}
}

func (d *daemon) mountValidationError(src, mountpoint string, err error) string {
	if errors.Is(err, config.ErrMountpointNotEmpty) {
		if kids := d.childMountsUnder(mountpoint); len(kids) > 0 {
			return fmt.Sprintf(
				"%s already has %d mount(s) nested under it (%s). Nested mounts aren't supported — a parent mount would shadow them. Unmount those first, e.g. `janusfs umount %s`, then mount %s.",
				mountpoint, len(kids), strings.Join(kids, ", "), kids[0], src)
		}
		return fmt.Sprintf("mountpoint %s is not empty (leftover from a previous run?); remove its contents and retry", mountpoint)
	}
	if errors.Is(err, syscall.ENXIO) {
		return fmt.Sprintf("mountpoint %s is a stale or broken mount (device not configured); run `janusfs umount %s` to clear it, then retry", mountpoint, mountpoint)
	}
	return err.Error()
}

func (d *daemon) doUnmount(req daemonRequest) daemonResponse {
	d.opsMu.Lock()
	defer d.opsMu.Unlock()

	if req.Mountpoint == "" {
		return daemonResponse{Error: "mountpoint (or src) is required"}
	}
	target, err := filepath.Abs(req.Mountpoint)
	if err != nil {
		return daemonResponse{Error: err.Error()}
	}

	// Match on the mountpoint first, then fall back to the source path — the
	// mirrored mountpoints are deep, so unmounting by <src> is the friendlier
	// path.
	d.mu.Lock()
	mp := target
	rt, ok := d.mounts[mp]
	if !ok {
		for k, r := range d.mounts {
			if abs, _ := filepath.Abs(r.Src); abs == target {
				mp, rt, ok = k, r, true
				break
			}
		}
	}
	if ok {
		delete(d.mounts, mp)
	}
	d.mu.Unlock()
	if !ok {
		// Not live in this daemon. It may still be a stale registry entry — a
		// mount recorded earlier that failed to resume (e.g. after a restart).
		// Prune it so it stops being listed and re-attempted, and report that
		// rather than a bare "not mounted" the user can't act on.
		if pruned := pruneStaleRegistry(target); pruned != "" {
			return daemonResponse{
				OK:      true,
				Message: fmt.Sprintf("%s was not live; removed its stale entry from the mount registry", pruned),
				Mounts:  []mountStatus{{Mountpoint: pruned}},
			}
		}
		return daemonResponse{Error: fmt.Sprintf("%q is not mounted by this daemon and is not in the registry (pass its mountpoint or source path)", req.Mountpoint)}
	}

	rt.stop()
	d.forgetMount(mp)
	return daemonResponse{OK: true, Message: "unmounted " + mp}
}

// resume mounts every record in the registry not already live, at daemon
// start. Failures are logged, not fatal — one bad record shouldn't stop the
// rest from coming back.
func (d *daemon) resume() {
	records, err := config.LoadMounts()
	if err != nil {
		d.logger.Warn("failed to read mounts registry", "error", err)
		return
	}
	for _, rec := range records {
		resp := d.doMount(daemonRequest{Cmd: "mount", Src: rec.Src, Mountpoint: rec.Mountpoint, Label: rec.Label, Resume: true})
		if resp.OK {
			d.logger.Info("resumed mount", "src", rec.Src, "mountpoint", rec.Mountpoint)
		} else {
			d.logger.Warn("failed to resume mount", "mountpoint", rec.Mountpoint, "error", resp.Error)
		}
	}
}

func (d *daemon) shutdown() {
	d.mu.Lock()
	rts := make([]*mountRuntime, 0, len(d.mounts))
	for mp, rt := range d.mounts {
		rts = append(rts, rt)
		_ = removePidfile(mp)
	}
	d.mounts = map[string]*mountRuntime{}
	d.mu.Unlock()

	var wg sync.WaitGroup
	for _, rt := range rts {
		wg.Add(1)
		go func(rt *mountRuntime) { defer wg.Done(); rt.stop() }(rt)
	}
	wg.Wait()

	// Every mount above is already unmounted at this point, so a surviving
	// watchdog's own liveness sweep would find nothing mounted and be a no-op
	// regardless. Signalling it here is a courtesy that ends it promptly
	// instead of leaving it polling a PID that's about to vanish — not a
	// correctness requirement.
	stopWatchdog(d)

	if d.indexer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = d.indexer.Shutdown(shutdownCtx)
		cancel()
	}
}

func (d *daemon) status(rt *mountRuntime) mountStatus {
	return mountStatus{
		Src:        rt.Src,
		Label:      rt.Label,
		Mountpoint: rt.Mountpoint,
		Dashboard:  fmt.Sprintf("http://localhost:%d/mounts/%s/", d.uiPort, rt.UUID),
	}
}

// pruneStaleRegistry removes a registry entry matching target (by mountpoint
// or source path, absolute) and returns the removed mountpoint, or "" if no
// entry matched. Used to clear entries the daemon isn't tracking live so they
// stop being listed and re-resumed.
func pruneStaleRegistry(target string) string {
	records, err := config.LoadMounts()
	if err != nil {
		return ""
	}
	for _, r := range records {
		mpAbs, _ := filepath.Abs(r.Mountpoint)
		srcAbs, _ := filepath.Abs(r.Src)
		if mpAbs == target || srcAbs == target {
			if err := config.RemoveMount(r.Mountpoint); err != nil {
				return ""
			}
			return r.Mountpoint
		}
	}
	return ""
}

// childMountsUnder returns the source paths of active mounts whose mountpoint
// is nested under parent, so a parent-mount attempt can name exactly what to
// unmount first. Sources (not mountpoints) because `janusfs umount` accepts a
// source path and it's the friendlier thing to show.
func (d *daemon) childMountsUnder(parent string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	prefix := parent + string(filepath.Separator)
	var out []string
	for mp, rt := range d.mounts {
		if strings.HasPrefix(mp, prefix) {
			out = append(out, rt.Src)
		}
	}
	sort.Strings(out)
	return out
}

func (d *daemon) snapshot() []mountStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]mountStatus, 0, len(d.mounts))
	for _, rt := range d.mounts {
		out = append(out, d.status(rt))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	return out
}

// The daemon's HTTP dashboard (handleHTTP / handleIndex) lives in dashboard.go;
// the background-daemonize plumbing (startDaemonBackground / newLogsCmd) lives
// in daemonize.go.
