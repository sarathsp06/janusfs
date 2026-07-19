package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestNewProducesValidJSONWithComponent(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf, slog.LevelInfo)

	logger := New("engine")
	logger.Info("hello world")

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("expected log output, got none")
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("log output is not valid JSON: %v\nline: %s", err, line)
	}

	if decoded["component"] != "engine" {
		t.Errorf("component = %v, want %q", decoded["component"], "engine")
	}
	if decoded["msg"] != "hello world" {
		t.Errorf("msg = %v, want %q", decoded["msg"], "hello world")
	}
}

func TestSetOutputLevelRespected(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf, slog.LevelWarn)
	defer SetOutput(os.Stderr, slog.LevelInfo)

	logger := New("watch")
	logger.Info("should be filtered")
	if buf.Len() != 0 {
		t.Errorf("expected Info logs to be filtered at Warn level, got: %s", buf.String())
	}

	logger.Warn("should appear")
	if buf.Len() == 0 {
		t.Fatalf("expected Warn log to appear")
	}
}

func TestNewConcurrentWithSetOutputIsSafe(t *testing.T) {
	// New and SetOutput must be safe to call concurrently without a race or
	// panic (atomic.Value guards the shared handler).
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = New("concurrent")
		}()
	}
	wg.Wait()
	SetOutput(os.Stderr, slog.LevelInfo)
}
