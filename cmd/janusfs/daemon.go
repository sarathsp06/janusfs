package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/sarathsp06/janusfs/internal/config"
	"github.com/sarathsp06/janusfs/internal/logging"
)

// daemonRequest is one command sent by a `janusfs mount`/`umount`/`daemon
// status` client over the control socket. One JSON object per connection.
type daemonRequest struct {
	Cmd        string `json:"cmd"` // "mount" | "unmount" | "list"
	Src        string `json:"src,omitempty"`
	Mountpoint string `json:"mountpoint,omitempty"`
	Name       string `json:"name,omitempty"`
	NoHistory  bool   `json:"no_history,omitempty"`
}

type daemonResponse struct {
	OK      bool          `json:"ok"`
	Error   string        `json:"error,omitempty"`
	Message string        `json:"message,omitempty"`
	Mounts  []mountStatus `json:"mounts,omitempty"`
}

type mountStatus struct {
	Src        string `json:"src"`
	Mountpoint string `json:"mountpoint"`
	Dashboard  string `json:"dashboard"`
}

func newDaemonCmd() *cobra.Command {
	var debug, noOpen bool
	var indexPort int

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the JanusFS daemon: owns every mount, resumes past ones, serves the dashboard",
		Long: "Starts the long-running JanusFS process. It resumes every mount recorded by a\n" +
			"previous `janusfs mount` (and not explicitly unmounted), serves a combined\n" +
			"dashboard listing them all, and accepts `janusfs mount`/`umount` commands over\n" +
			"a local control socket. Runs until Ctrl-C, then unmounts everything cleanly.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd.Context(), debug, noOpen, indexPort)
		},
	}
	cmd.Flags().BoolVar(&debug, "debug", false, "verbose debug logging")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open the dashboard in a browser on start")
	cmd.Flags().IntVar(&indexPort, "ui-port", config.DefaultUIPort, "port for the combined dashboard (binds 127.0.0.1)")
	return cmd
}

// daemon owns every live mount in one process and multiplexes control
// commands from `janusfs mount`/`umount` clients onto them.
type daemon struct {
	opsMu   sync.Mutex               // serializes mount/unmount so check-then-act is atomic
	mu      sync.Mutex               // guards the mounts map for concurrent reads
	mounts  map[string]*mountRuntime // keyed by absolute mountpoint
	base    config.Config            // resolved defaults (mount root, cache sizing, …)
	debug   bool
	ctx     context.Context
	logger  *slog.Logger
	uiPort  int
	indexLn net.Listener
	indexer *http.Server
}

func runDaemon(parent context.Context, debug, noOpen bool, indexPort int) error {
	sock, err := socketPath()
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	// Refuse a second daemon: if the socket answers, one is already running.
	if _, derr := net.Dial("unix", sock); derr == nil {
		return fmt.Errorf("daemon: already running (control socket %s is live); stop it first", sock)
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

	// Combined dashboard (127.0.0.1 only: it links to per-mount dashboards
	// with their bearer tokens embedded).
	indexAddr := fmt.Sprintf("127.0.0.1:%d", indexPort)
	ln, err := net.Listen("tcp", indexAddr)
	if err != nil {
		return fmt.Errorf("daemon: dashboard port %d unavailable: %w", indexPort, err)
	}
	d.indexLn = ln
	d.indexer = &http.Server{Handler: http.HandlerFunc(d.handleIndex)}
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
	lc.Close()
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
	defer conn.Close()
	var req daemonRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeResp(conn, daemonResponse{Error: "bad request: " + err.Error()})
		return
	}
	switch req.Cmd {
	case "mount":
		writeResp(conn, d.doMount(req))
	case "unmount":
		writeResp(conn, d.doUnmount(req))
	case "list":
		writeResp(conn, daemonResponse{OK: true, Mounts: d.snapshot()})
	default:
		writeResp(conn, daemonResponse{Error: "unknown command: " + req.Cmd})
	}
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
	cfg.UIPort = 0 // auto-assign a free per-mount dashboard port
	cfg.Src = req.Src
	cfg.Mountpoint = req.Mountpoint
	cfg.Name = req.Name
	cfg.NoHistory = req.NoHistory || d.base.NoHistory

	if err := cfg.ResolveMountpoint(); err != nil {
		return daemonResponse{Error: err.Error()}
	}
	if cfg.Mountpoint == "" {
		return daemonResponse{Error: "no mountpoint: pass one explicitly or configure a mount root (janusfs install)"}
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

	if err := cfg.Validate(); err != nil {
		return daemonResponse{Error: err.Error()}
	}

	rt, err := startMount(d.ctx, cfg, d.debug)
	if err != nil {
		return daemonResponse{Error: err.Error()}
	}

	d.mu.Lock()
	d.mounts[mpAbs] = rt
	d.mu.Unlock()

	_ = writePidfile(mpAbs) // records the daemon PID; keeps the double-mount guard working
	srcAbs, _ := filepath.Abs(cfg.Src)
	if err := config.RecordMount(srcAbs, mpAbs); err != nil {
		d.logger.Warn("failed to record mount", "error", err)
	}

	return daemonResponse{
		OK:      true,
		Message: fmt.Sprintf("mounted %s -> %s", cfg.Src, mpAbs),
		Mounts:  []mountStatus{d.status(rt)},
	}
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
		return daemonResponse{Error: fmt.Sprintf("%q is not mounted by this daemon (pass its mountpoint or source path)", req.Mountpoint)}
	}

	rt.stop()
	_ = removePidfile(mp)
	pruneMirrorDirs(mp, d.base.MountRoot)
	if err := config.RemoveMount(mp); err != nil {
		d.logger.Warn("failed to update mounts registry", "error", err)
	}
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
		resp := d.doMount(daemonRequest{Cmd: "mount", Src: rec.Src, Mountpoint: rec.Mountpoint})
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

	if d.indexer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		d.indexer.Shutdown(shutdownCtx)
		cancel()
	}
}

func (d *daemon) status(rt *mountRuntime) mountStatus {
	return mountStatus{
		Src:        rt.Src,
		Mountpoint: rt.Mountpoint,
		Dashboard:  fmt.Sprintf("http://localhost:%d/?token=%s", rt.UIPort, rt.Token),
	}
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

// handleIndex serves the combined dashboard: a plain list of every live
// mount linking to its individual dashboard.
func (d *daemon) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	mounts := d.snapshot()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>JanusFS</title>")
	fmt.Fprint(w, "<style>body{font-family:-apple-system,system-ui,sans-serif;background:#f4efea;color:#383838;max-width:820px;margin:40px auto;padding:0 24px}"+
		"h1{font-size:22px}a{color:#383838}li{margin:8px 0;list-style:none;padding:12px;background:#fff;border:2px solid #383838;box-shadow:-4px 4px 0 #383838}"+
		".mp{font-family:monospace;font-size:13px;color:#666}</style></head><body>")
	fmt.Fprintf(w, "<h1>JanusFS — %d mount(s)</h1><ul>", len(mounts))
	if len(mounts) == 0 {
		fmt.Fprint(w, "<p>No active mounts. Run <code>janusfs mount &lt;src&gt;</code>.</p>")
	}
	for _, m := range mounts {
		fmt.Fprintf(w, "<li><a href=\"%s\">%s</a><div class=\"mp\">%s</div></li>",
			html.EscapeString(m.Dashboard), html.EscapeString(m.Src), html.EscapeString(m.Mountpoint))
	}
	fmt.Fprint(w, "</ul></body></html>")
}

// socketPath is the daemon control socket, ~/.janusfs/daemon.sock.
func socketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".janusfs", "daemon.sock"), nil
}

// daemonCall dials the daemon control socket, sends one request, and returns
// the response. Returns an error (with errDaemonNotRunning wrapped) if no
// daemon is listening, so callers can offer a clear next step.
func daemonCall(req daemonRequest) (daemonResponse, error) {
	sock, err := socketPath()
	if err != nil {
		return daemonResponse{}, err
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return daemonResponse{}, errDaemonNotRunning
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return daemonResponse{}, err
	}
	var resp daemonResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return daemonResponse{}, err
	}
	return resp, nil
}

func writeResp(conn net.Conn, resp daemonResponse) {
	resp.OK = resp.OK || resp.Error == ""
	_ = json.NewEncoder(conn).Encode(resp)
}
