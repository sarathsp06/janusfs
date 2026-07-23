package obs

import (
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
)

type blockingSink struct {
	entered chan struct{}
	release chan struct{}
}

func newBlockingSink() *blockingSink {
	return &blockingSink{entered: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingSink) Record(Event) {
	select {
	case <-s.entered:
	default:
		close(s.entered)
	}
	<-s.release
}

func TestRecorderEmitsPrometheusMetrics(t *testing.T) {
	r := newRecorder(nil, 0)
	defer r.Close()

	r.SetGeneration(7)
	r.IncConfigReloads()
	r.Emit(Event{Op: OpRead, Decision: Allowed, Bytes: 128, LatencyUs: 42, Cache: CacheHit})
	r.Emit(Event{Op: OpOpen, Decision: Hidden, LatencyUs: 3, Cache: CacheNA})

	assertMetric(t, r.Registry(), "janusfs_ops_total", map[string]string{"op": "read", "decision": "ALLOWED"}, 1)
	assertMetric(t, r.Registry(), "janusfs_ops_total", map[string]string{"op": "open", "decision": "HIDDEN"}, 1)
	assertMetric(t, r.Registry(), "janusfs_bytes_served_total", map[string]string{"decision": "ALLOWED"}, 128)
	assertMetric(t, r.Registry(), "janusfs_cache_results_total", map[string]string{"result": "hit"}, 1)
	assertMetric(t, r.Registry(), "janusfs_generation", nil, 7)
	assertMetric(t, r.Registry(), "janusfs_config_reloads_total", nil, 1)
}

func TestRecorderHistoryFanoutDropsInsteadOfBlocking(t *testing.T) {
	sink := newBlockingSink()
	r := newRecorder(sink, 1)
	defer r.Close()

	r.Emit(Event{Op: OpRead, Decision: Allowed})
	<-sink.entered

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Emit(Event{Op: OpRead, Decision: Allowed})
		}()
	}
	wg.Wait()

	assertMetric(t, r.Registry(), "janusfs_events_dropped_total", nil, 1)
	close(sink.release)
}

func TestRecorderDoesNotUsePathLabels(t *testing.T) {
	r := newRecorder(nil, 0)
	defer r.Close()

	r.Emit(Event{Op: OpRead, Decision: Allowed, Path: "/secret/path.env", Bytes: 1})

	families, err := r.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		for _, m := range mf.Metric {
			for _, lp := range m.Label {
				if lp.GetName() == "path" || strings.Contains(lp.GetValue(), "secret/path.env") {
					t.Fatalf("metric %s leaked path label %q=%q", mf.GetName(), lp.GetName(), lp.GetValue())
				}
			}
		}
	}
}

func assertMetric(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string, want float64) {
	t.Helper()
	got := metricValue(t, reg, name, labels)
	if got != want {
		t.Fatalf("%s%v = %v, want %v", name, labels, got, want)
	}
}

func metricValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.Metric {
			if !labelsMatch(m, labels) {
				continue
			}
			if m.Counter != nil {
				return m.Counter.GetValue()
			}
			if m.Gauge != nil {
				return m.Gauge.GetValue()
			}
		}
	}
	return 0
}

func labelsMatch(m *io_prometheus_client.Metric, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	for k, v := range want {
		found := false
		for _, lp := range m.Label {
			if lp.GetName() == k && lp.GetValue() == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
