package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sarathsp06/janusfs/internal/ui"
)

func testRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "janusfs_test_metric", Help: "test metric"}))
	return reg
}

func testServer() *Server {
	return New(ui.FS, "test-token", testRegistry(), nil)
}

func TestSummaryEndpoint(t *testing.T) {
	s := testServer()
	s.SetVFSMeta("/src/project", func() (int, int64, uint64, uint64, uint64) {
		return 2, 4096, 3, 4, 5
	}, nil, nil, func() uint64 { return 9 })

	req := httptest.NewRequest("GET", "/api/v1/summary", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Status struct {
			Generation uint64 `json:"generation"`
			Cache      struct {
				Entries  int    `json:"entries"`
				Bytes    int64  `json:"bytes"`
				Hits     uint64 `json:"hits"`
				Misses   uint64 `json:"misses"`
				Rebuilds uint64 `json:"rebuilds"`
			} `json:"cache"`
		} `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status.Generation != 9 {
		t.Fatalf("generation = %d, want 9", resp.Status.Generation)
	}
	if resp.Status.Cache.Entries != 2 || resp.Status.Cache.Bytes != 4096 || resp.Status.Cache.Hits != 3 || resp.Status.Cache.Misses != 4 || resp.Status.Cache.Rebuilds != 5 {
		t.Fatalf("cache status = %+v", resp.Status.Cache)
	}
}

func TestSummaryIncludesMountInfo(t *testing.T) {
	s := testServer()
	s.SetMountInfo("/src/project", "/mnt/project")
	req := httptest.NewRequest("GET", "/api/v1/summary", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Mount struct {
			Source     string `json:"source"`
			Mountpoint string `json:"mountpoint"`
		} `json:"mount"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mount.Source != "/src/project" || resp.Mount.Mountpoint != "/mnt/project" {
		t.Fatalf("mount info = %+v, want source and mountpoint", resp.Mount)
	}
}

func TestAuthMissing(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/summary", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthQueryParam(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/summary?token=test-token", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with token query param, got %d", w.Code)
	}
}

func TestTopEndpointRemoved(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/top", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for removed top endpoint, got %d", w.Code)
	}
}

func TestLatencyEndpointRemoved(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/latency", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for removed latency endpoint, got %d", w.Code)
	}
}

func TestHistoryEndpointNoStore(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/history", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if enabled, ok := resp["enabled"]; ok && enabled == true {
		t.Log("history enabled")
	} else {
		t.Log("history not enabled (expected with nil store)")
	}
}

func TestSessionsEndpointNoStore(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestHostSecurity(t *testing.T) {
	s := testServer()
	handler := withSecurity(s.mux)

	// Valid localhost request.
	req := httptest.NewRequest("GET", "/api/v1/summary?token=test-token", nil)
	req.Host = "127.0.0.1:7381"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for localhost, got %d", w.Code)
	}

	// External host is now allowed (0.0.0.0 bind).
	req2 := httptest.NewRequest("GET", "/api/v1/summary?token=test-token", nil)
	req2.Host = "evil.com"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("expected 200 for external host (0.0.0.0 bind), got %d", w2.Code)
	}
}

func TestHeaders(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/summary", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	withHeaders(s.mux).ServeHTTP(w, req)

	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("expected Cache-Control: no-store")
	}
}

func TestRevealViewAndEdit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.env"), []byte("KEY=1"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := testServer()
	s.SetVFSMeta(root, nil, nil, nil, nil)

	get := func() string {
		req := httptest.NewRequest("GET", "/api/v1/reveal?path=secret.env&token=test-token", nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET reveal: got %d, body %q", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if got := get(); got != "KEY=1" {
		t.Fatalf("GET reveal content = %q, want %q", got, "KEY=1")
	}

	req := httptest.NewRequest("POST", "/api/v1/reveal?path=secret.env&token=test-token", strings.NewReader("KEY=2"))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST reveal: got %d, body %q", w.Code, w.Body.String())
	}
	if got := get(); got != "KEY=2" {
		t.Fatalf("after edit, GET reveal content = %q, want %q", got, "KEY=2")
	}

	info, err := os.Stat(filepath.Join(root, "secret.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("file mode after save = %o, want unchanged 0644", info.Mode().Perm())
	}

	escReq := httptest.NewRequest("GET", "/api/v1/reveal?path=../outside&token=test-token", nil)
	escW := httptest.NewRecorder()
	s.mux.ServeHTTP(escW, escReq)
	if escW.Code != http.StatusForbidden {
		t.Errorf("path escape: got %d, want 403", escW.Code)
	}
}

func TestReloadEndpoint(t *testing.T) {
	s := testServer()
	called := 0
	s.SetReload(func() error { called++; return nil })

	req := httptest.NewRequest("POST", "/api/v1/reload?token=test-token", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reload: got %d, body %q", w.Code, w.Body.String())
	}
	if called != 1 {
		t.Errorf("reload callback called %d times, want 1", called)
	}
}

func TestConfigSaveTriggersReload(t *testing.T) {
	root := t.TempDir()
	s := testServer()
	s.SetVFSMeta(root, nil, nil, nil, nil)
	called := 0
	s.SetReload(func() error { called++; return nil })

	body := `{"path":".janusmask","content":"*.env : env-value\n"}`
	req := httptest.NewRequest("POST", "/api/v1/config?token=test-token", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("config save: got %d, body %q", w.Code, w.Body.String())
	}
	if called != 1 {
		t.Errorf("saving config should trigger reload; callback called %d times", called)
	}
	if _, err := os.Stat(filepath.Join(root, ".janusmask")); err != nil {
		t.Errorf("config file not written: %v", err)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "janusfs_test_metric", Help: "test metric"})
	reg.MustRegister(g)
	g.Set(12)
	s := New(nil, "test-token", reg, nil)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "janusfs_test_metric 12") {
		t.Fatalf("metrics body missing test metric: %s", w.Body.String())
	}
}

func TestVendorAssetServed(t *testing.T) {
	s := New(ui.FS, "test-token", testRegistry(), nil)
	req := httptest.NewRequest("GET", "/vendor/cm.js", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET /vendor/cm.js: got %d, want 200", w.Code)
	}
}
