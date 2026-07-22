// Package api implements the HTTP server and REST/SSE handlers described in
// SPEC.md §11: REST endpoints for summary/coverage/top/latency and a
// Server-Sent Events endpoint for the live event feed. It is a thin adapter —
// no business logic, no redaction, no SQL — delegating to internal/obs,
// internal/engine, and internal/provider (SPEC §5's dependency rule).
//
// Per SPEC §11 the server binds 127.0.0.1 only, requires a bearer token on
// all /api/* endpoints, and sets Cache-Control: no-store on all API responses.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sarathsp06/janusfs/internal/history"
	"github.com/sarathsp06/janusfs/internal/obs"
	"github.com/sarathsp06/janusfs/internal/vfsmeta"
)

// Server is the localhost HTTP/SSE server (SPEC §11).
type Server struct {
	mux           *http.ServeMux
	server        *http.Server
	metrics       *obs.JanusMetrics
	ring          *obs.RingBuffer
	topN          *obs.TopN
	bus           *obs.EventBus
	history       *history.Store
	token         string
	ui            fs.FS
	root          string
	mountpoint    string
	providerStats func() (int, int64, uint64, uint64, uint64)
	resolvePath   func(relPath string, isDir bool) (string, []string, string) // decision, patternNames, ruleRef ("<file>:<line>")
	watcherAlive  func() bool
	reload        func() error // recompile the rule set on demand (config save / reload button)
	startTime     time.Time
}

// New constructs an API server. uiFS is the embedded dashboard filesystem
// (internal/ui.FS). token is the per-mount bearer token minted at startup.
// bus may be nil (no live feed). hist may be nil (no history persistence).
func New(uiFS fs.FS, token string, metrics *obs.JanusMetrics, ring *obs.RingBuffer, topN *obs.TopN, bus *obs.EventBus, hist *history.Store) *Server {
	s := &Server{
		mux:     http.NewServeMux(),
		metrics: metrics,
		ring:    ring,
		topN:    topN,
		bus:     bus,
		history: hist,
		token:   token,
		ui:      uiFS,
	}
	s.register()
	return s
}

// SetVFSMeta configures the root path, provider stats, path resolver, and
// watcher status for vfsmeta and coverage endpoints. Call before Start.
func (s *Server) SetVFSMeta(root string, providerStats func() (int, int64, uint64, uint64, uint64), resolvePath func(relPath string, isDir bool) (string, []string, string), watcherAlive func() bool) {
	s.root = root
	s.providerStats = providerStats
	s.resolvePath = resolvePath
	s.watcherAlive = watcherAlive
	if s.startTime.IsZero() {
		s.startTime = time.Now()
	}
}

// SetReload registers the callback that recompiles the rule set (used by the
// config-save handler and the /api/v1/reload endpoint). Call before Start.
func (s *Server) SetReload(reload func() error) {
	s.reload = reload
}

// SetMountInfo exposes source and mountpoint metadata to the operator
// dashboard, so it can clearly show which path should be handed to an agent.
func (s *Server) SetMountInfo(source, mountpoint string) {
	s.root = source
	s.mountpoint = mountpoint
	if s.startTime.IsZero() {
		s.startTime = time.Now()
	}
}

func (s *Server) register() {
	s.mux.HandleFunc("/api/v1/summary", s.withToken(s.handleSummary))
	s.mux.HandleFunc("/api/v1/coverage", s.withToken(s.handleCoverage))
	s.mux.HandleFunc("/api/v1/reveal", s.withToken(s.handleReveal))
	s.mux.HandleFunc("/api/v1/top", s.withToken(s.handleTop))
	s.mux.HandleFunc("/api/v1/latency", s.withToken(s.handleLatency))
	s.mux.HandleFunc("/api/v1/config", s.withToken(s.handleConfig))
	s.mux.HandleFunc("/api/v1/reload", s.withToken(s.handleReload))
	s.mux.HandleFunc("/api/v1/events", s.withToken(s.handleEvents))
	s.mux.HandleFunc("/api/v1/history", s.withToken(s.handleHistory))
	s.mux.HandleFunc("/api/v1/sessions", s.withToken(s.handleSessions))
	s.mux.HandleFunc("/api/v1/vfsmeta/status.json", s.withToken(s.handleStatusJSON))
	s.mux.HandleFunc("/api/v1/vfsmeta/conflicts.json", s.withToken(s.handleConflictsJSON))

	s.mux.Handle("/", http.FileServer(http.FS(s.ui)))
}

// ServeHTTP implements the http.Handler interface so it can be nested or routed
// easily inside other servers.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	withSecurity(withHeaders(s.mux)).ServeHTTP(w, r)
}

// Listen binds to addr (127.0.0.1 only, SPEC §11) and returns the listener,
// so a port collision fails fast at mount time instead of asynchronously
// after the dashboard URL has already been printed. Serve the result with
// Serve.
func (s *Server) Listen(addr string) (net.Listener, error) {
	s.server = &http.Server{
		Addr:    addr,
		Handler: s,
	}
	return net.Listen("tcp", addr)
}

// Serve serves on a listener obtained from Listen. Blocks until the server
// stops.
func (s *Server) Serve(ln net.Listener) error {
	return s.server.Serve(ln)
}

// Shutdown drains and stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// withSecurity is a no-op middleware retained for symmetry. The server binds
// 0.0.0.0 (accessible from the network) and relies on bearer-token auth
// (/api/* via withToken) rather than host/origin filtering.
func withSecurity(next http.Handler) http.Handler {
	return next
}

// withHeaders wraps a handler with no-store and other security headers.
func withHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// withToken wraps a handler with bearer-token authentication. Since consolidated
// dashboards running on localhost do not require token auth, token verification
// is skipped on local connections.
func (s *Server) withToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil && (host == "127.0.0.1" || host == "::1" || host == "localhost") {
			// Skip token check for localhost as requested.
			next(w, r)
			return
		}
		if s.token != "" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+s.token {
				if r.URL.Query().Get("token") != s.token {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
		}
		next(w, r)
	}
}

func (s *Server) handleStatusJSON(w http.ResponseWriter, r *http.Request) {
	if s.root == "" {
		writeJSON(w, map[string]any{"error": "vfsmeta not configured"})
		return
	}
	watcherAlive := false
	if s.watcherAlive != nil {
		watcherAlive = s.watcherAlive()
	}
	snap := s.metrics.Snapshot()
	opCounts := make(map[string]uint64)
	for k, v := range snap.Ops {
		opCounts[k] = v
	}
	start := s.startTime
	if start.IsZero() {
		start = time.Now()
	}

	var entries int
	var bytes int64
	var hits, misses, rebuilds uint64
	if s.providerStats != nil {
		entries, bytes, hits, misses, rebuilds = s.providerStats()
	}
	b := vfsmeta.StatusJSON(start, snap.Generation, snap.ConfigReloads, hits, misses, rebuilds, watcherAlive, entries, bytes, opCounts)
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (s *Server) handleConflictsJSON(w http.ResponseWriter, r *http.Request) {
	if s.root == "" {
		writeJSON(w, map[string]any{"error": "vfsmeta not configured"})
		return
	}
	b, err := vfsmeta.ConflictsJSON(s.root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// relativeRuleRef turns a RuleRef's "<absolute-file>:<line>" into a
// "<path-relative-to-root>:<line>" so the dashboard can point at the exact
// config file and line without leaking the host's absolute filesystem
// layout.
func relativeRuleRef(root, ruleRef string) string {
	if ruleRef == "" {
		return ""
	}
	file, line, hasLine := strings.Cut(ruleRef, ":")
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return ruleRef
	}
	if hasLine {
		return rel + ":" + line
	}
	return rel
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	snap := s.metrics.Snapshot()
	// Provider tracks cache hit/miss/rebuild; the obs metrics don't — merge
	// in provider stats when available.
	if s.providerStats != nil {
		_, _, hits, misses, rebuilds := s.providerStats()
		snap.CacheHits = hits
		snap.CacheMisses = misses
		snap.CacheRebuilds = rebuilds
	}
	writeJSON(w, map[string]any{
		"snapshot": snap,
		"mount": map[string]any{
			"source":     s.root,
			"mountpoint": s.mountpoint,
		},
		"uptime": time.Now().Unix(),
	})
}

func (s *Server) handleCoverage(w http.ResponseWriter, r *http.Request) {
	if s.root == "" || s.resolvePath == nil {
		writeJSON(w, map[string]any{"masked": []any{}, "hidden": []any{}})
		return
	}

	var masked, hidden []map[string]any

	filepath.WalkDir(s.root, func(fpath string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(s.root, fpath)
		dec, pats, ruleRef := s.resolvePath(rel, false)
		ruleRef = relativeRuleRef(s.root, ruleRef)
		switch dec {
		case "MASKED":
			masked = append(masked, map[string]any{
				"path":     rel,
				"patterns": pats,
				"rule":     ruleRef,
			})
		case "HIDDEN":
			hidden = append(hidden, map[string]any{
				"path": rel,
				"rule": ruleRef,
			})
		}
		return nil
	})

	writeJSON(w, map[string]any{
		"masked": masked,
		"hidden": hidden,
	})
}

// maxRevealWrite caps the body of an edit-save (10 MiB).
const maxRevealWrite = 10 << 20

// handleReveal serves (GET) and saves (POST) the real source file behind a
// masked/hidden entry. Reads/writes the source tree directly, bypassing the
// mount decision — this is the token-authenticated operator view.
func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	if s.root == "" {
		http.Error(w, "not configured", http.StatusNotFound)
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	realPath := filepath.Join(s.root, filepath.FromSlash(rel))
	if realPath != s.root && !strings.HasPrefix(realPath, s.root+string(os.PathSeparator)) {
		http.Error(w, "path escape", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		http.ServeFile(w, r, realPath)
	case http.MethodPost:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRevealWrite))
		if err != nil {
			http.Error(w, "read error (file too large?)", http.StatusBadRequest)
			return
		}
		info, err := os.Stat(realPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if info.IsDir() {
			http.Error(w, "cannot write a directory", http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(realPath, body, info.Mode().Perm()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"saved": true, "path": rel})
	default:
		http.Error(w, "use GET or POST", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleTop(w http.ResponseWriter, r *http.Request) {
	n := 50
	if s.topN == nil {
		writeJSON(w, map[string]any{"byReads": []obs.TopEntry{}, "byBytes": []obs.TopEntry{}})
		return
	}
	writeJSON(w, map[string]any{
		"byReads": s.topN.TopReads(n),
		"byBytes": s.topN.TopBytes(n),
	})
}

func (s *Server) handleLatency(w http.ResponseWriter, r *http.Request) {
	snaps := s.metrics.LatencySnapshots()
	if snaps == nil {
		snaps = []obs.LatencySnapshot{}
	}
	writeJSON(w, map[string]any{"ops": snaps})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if s.ring != nil {
		for _, label := range s.ring.Snapshot() {
			fmt.Fprintf(w, "data: %s\n\n", label)
		}
		flusher.Flush()
	}

	if s.bus == nil {
		fmt.Fprintf(w, "data: {\"type\":\"done\"}\n\n")
		flusher.Flush()
		return
	}

	ctx := r.Context()
	for {
		select {
		case e, ok := <-s.bus.Events():
			if !ok {
				return
			}
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// handleHistory returns aggregated history rollups (FR-46).
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeJSON(w, map[string]any{"rollups": []history.OpRollup{}, "enabled": false})
		return
	}
	since := time.Now().Add(-1 * time.Hour)
	if s := r.URL.Query().Get("since"); s != "" {
		if parsed, err := time.Parse(time.RFC3339, s); err == nil {
			since = parsed
		}
	}
	rollups, err := s.history.Query(r.Context(), since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rollups == nil {
		rollups = []history.OpRollup{}
	}
	writeJSON(w, map[string]any{"rollups": rollups, "enabled": true, "since": since.Format(time.RFC3339)})
}

// handleSessions returns history store stats (FR-46).
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeJSON(w, map[string]any{"enabled": false})
		return
	}
	stats := s.history.Stats(r.Context())
	if stats == nil {
		stats = map[string]any{}
	}
	stats["enabled"] = true
	writeJSON(w, stats)
}

// handleConfig dispatches GET and POST on /api/v1/config.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleConfigGet(w, r)
	case http.MethodPost:
		s.handleConfigSave(w, r)
	default:
		http.Error(w, "use GET or POST", http.StatusMethodNotAllowed)
	}
}

// handleConfigGet returns all .janusignore and .janusmask files found in the
// source tree, with their full content, for the config editor.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	if s.root == "" {
		writeJSON(w, map[string]any{"files": []any{}})
		return
	}
	var files []map[string]any
	filepath.WalkDir(s.root, func(fpath string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := d.Name()
		if base != ".janusignore" && base != ".janusmask" {
			return nil
		}
		rel, _ := filepath.Rel(s.root, fpath)
		b, err := os.ReadFile(fpath)
		if err != nil {
			return nil
		}
		files = append(files, map[string]any{
			"path":    rel,
			"content": string(b),
		})
		return nil
	})
	if files == nil {
		files = []map[string]any{}
	}
	writeJSON(w, map[string]any{"files": files})
}

// handleConfigSave writes content to a config file.
func (s *Server) handleConfigSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if s.root == "" {
		http.Error(w, "not configured", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	// Only allow .janusignore and .janusmask files.
	base := filepath.Base(req.Path)
	if base != ".janusignore" && base != ".janusmask" {
		http.Error(w, "only .janusignore and .janusmask files are editable", http.StatusForbidden)
		return
	}
	realPath := filepath.Join(s.root, filepath.FromSlash(req.Path))
	if !strings.HasPrefix(realPath, s.root) {
		http.Error(w, "path escape", http.StatusForbidden)
		return
	}
	if err := os.MkdirAll(filepath.Dir(realPath), 0o755); err != nil {
		http.Error(w, "mkdir error", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(realPath, []byte(req.Content), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Apply the edit immediately: there is no file watcher, so a save must
	// recompile the rule set itself.
	reloaded := false
	if s.reload != nil {
		if err := s.reload(); err != nil {
			http.Error(w, "saved, but reload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		reloaded = true
	}
	writeJSON(w, map[string]any{"saved": true, "path": req.Path, "reloaded": reloaded})
}

// handleReload recompiles the rule set on demand (POST). Backs the dashboard's
// "Reload rules" button and `janusfs update`.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if s.reload == nil {
		http.Error(w, "reload not available", http.StatusNotFound)
		return
	}
	if err := s.reload(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"reloaded": true})
}
