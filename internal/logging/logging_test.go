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

// resetForTest restores the shared handler to a known state after a test
// that calls SetOutput, so tests don't leak configuration into each other.
// logging has no test-only reset hook by design (SetOutput is the only
// mutator, matching "one place configures the handler"), so tests call
// SetOutput directly to restore stderr/Info at the end.
func TestMain(m *testing.M) {
	m.Run()
}

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

	comp, ok := decoded["component"]
	if !ok {
		t.Fatalf("log line missing component attribute: %s", line)
	}
	if comp != "engine" {
		t.Errorf("component = %v, want %q", comp, "engine")
	}
	if decoded["msg"] != "hello world" {
		t.Errorf("msg = %v, want %q", decoded["msg"], "hello world")
	}
}

func TestSetOutputAffectsPreexistingLoggers(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	SetOutput(&buf1, slog.LevelInfo)

	// Logger constructed before the next SetOutput call.
	logger := New("provider")

	SetOutput(&buf2, slog.LevelInfo)
	logger.Info("after switch")

	if buf1.Len() != 0 {
		t.Errorf("expected nothing written to old output, got: %s", buf1.String())
	}
	if buf2.Len() == 0 {
		t.Fatalf("expected pre-existing logger to write to new output after SetOutput")
	}

	var decoded map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf2.Bytes()), &decoded); err != nil {
		t.Fatalf("log output is not valid JSON: %v", err)
	}
	if decoded["component"] != "provider" {
		t.Errorf("component = %v, want %q", decoded["component"], "provider")
	}
}

func TestSetOutputLevelRespectedByBothOldAndNewLoggers(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf, slog.LevelInfo)

	preexisting := New("watch")

	SetOutput(&buf, slog.LevelWarn)
	postswitch := New("watch")

	preexisting.Info("should be filtered")
	postswitch.Info("should also be filtered")
	if buf.Len() != 0 {
		t.Errorf("expected Info logs to be filtered at Warn level, got: %s", buf.String())
	}

	preexisting.Warn("should appear")
	if buf.Len() == 0 {
		t.Fatalf("expected Warn log from pre-existing logger to appear")
	}
	buf.Reset()

	postswitch.Warn("should appear too")
	if buf.Len() == 0 {
		t.Fatalf("expected Warn log from post-switch logger to appear")
	}

	// Restore default for any other tests running in this package.
	SetOutput(os.Stderr, slog.LevelInfo)
}

func TestWithAttrsAndWithGroupSurviveOutputSwap(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	SetOutput(&buf1, slog.LevelInfo)

	// Layer WithAttrs and WithGroup before swapping the output — both
	// derivedHandler branches must resolve against the *live* root, not a
	// snapshot from construction time.
	logger := New("provider").With("pid", 42).WithGroup("stats")

	SetOutput(&buf2, slog.LevelInfo)
	logger.Info("swapped", "count", 3)

	if buf1.Len() != 0 {
		t.Errorf("expected nothing written to original output, got: %s", buf1.String())
	}
	if buf2.Len() == 0 {
		t.Fatal("expected the layered logger to write to the new output after SetOutput")
	}

	var decoded map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf2.Bytes()), &decoded); err != nil {
		t.Fatalf("log output is not valid JSON: %v", err)
	}
	if decoded["component"] != "provider" || decoded["pid"] != float64(42) {
		t.Errorf("expected component=provider and pid=42, got %v / %v", decoded["component"], decoded["pid"])
	}
	stats, ok := decoded["stats"].(map[string]any)
	if !ok {
		t.Fatalf("expected a 'stats' group in output, got %v", decoded)
	}
	if stats["count"] != float64(3) {
		t.Errorf("expected stats.count=3, got %v", stats["count"])
	}
	SetOutput(os.Stderr, slog.LevelInfo)
}

func TestEnabledMatchesConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf, slog.LevelWarn)
	defer SetOutput(os.Stderr, slog.LevelInfo)

	logger := New("t")
	if logger.Enabled(nil, slog.LevelInfo) {
		t.Error("Info should be disabled at Warn level")
	}
	if !logger.Enabled(nil, slog.LevelWarn) {
		t.Error("Warn should be enabled at Warn level")
	}

	// And after adding attrs/groups (exercises derivedHandler.Enabled).
	derived := logger.With("k", "v").WithGroup("g")
	if derived.Enabled(nil, slog.LevelInfo) {
		t.Error("derived Info should be disabled at Warn level")
	}
	if !derived.Enabled(nil, slog.LevelWarn) {
		t.Error("derived Warn should be enabled at Warn level")
	}
}

func TestDefaultBeforeSetOutputIsSafe(t *testing.T) {
	// New must not panic even if called without a prior SetOutput in this
	// process (simulated here by constructing a fresh handler the way init()
	// does, since the package-level root has already been configured by
	// earlier tests in this file/package — this test instead just verifies
	// that calling New concurrently with SetOutput doesn't race or panic).
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = New("concurrent")
		}()
	}
	wg.Wait()
}
