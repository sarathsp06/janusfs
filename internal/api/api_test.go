package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sarathsp06/janusfs/internal/obs"
)

func testMetrics() *obs.JanusMetrics {
	m := &obs.JanusMetrics{}
	m.RecordOp(obs.OpRead, obs.Allowed)
	m.RecordOp(obs.OpRead, obs.Masked)
	m.RecordOp(obs.OpOpen, obs.Hidden)
	m.RecordBytes(obs.Allowed, 1000)
	return m
}

func testServer() *Server {
	m := testMetrics()
	r := obs.NewRingBuffer(64)
	r.Push("test event")
	return New(nil, "test-token", m, r, nil, nil, nil)
}

func TestSummaryEndpoint(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/summary", nil)
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
	snap, ok := resp["snapshot"].(map[string]any)
	if !ok {
		t.Fatal("expected snapshot in response")
	}
	ops := snap["ops"].(map[string]any)
	if ops["read:ALLOWED"].(float64) != 1 {
		t.Errorf("expected 1 read:ALLOWED, got %v", ops["read:ALLOWED"])
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

func TestTopEndpoint(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/top", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestLatencyEndpoint(t *testing.T) {
	s := testServer()
	req := httptest.NewRequest("GET", "/api/v1/latency", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
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
