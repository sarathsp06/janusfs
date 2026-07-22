package obs

import (
	"strings"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector implements prometheus.Collector by reading atomic counters from JanusMetrics.
type Collector struct {
	m *JanusMetrics

	opsTotal         *prometheus.Desc
	bytesServedTotal *prometheus.Desc
	cacheHits        *prometheus.Desc
	cacheMisses      *prometheus.Desc
	cacheRebuilds    *prometheus.Desc
	configReloads    *prometheus.Desc
	watcherEvents    *prometheus.Desc
	eventsDropped    *prometheus.Desc
	generation       *prometheus.Desc
}

// NewCollector creates a Collector that exposes JanusMetrics as Prometheus metrics.
func NewCollector(m *JanusMetrics) *Collector {
	return &Collector{
		m:                m,
		opsTotal:         prometheus.NewDesc("janusfs_ops_total", "Total filesystem operations", []string{"op", "decision"}, nil),
		bytesServedTotal: prometheus.NewDesc("janusfs_bytes_served_total", "Total bytes served", []string{"decision"}, nil),
		cacheHits:        prometheus.NewDesc("janusfs_cache_hits_total", "Total cache hits", nil, nil),
		cacheMisses:      prometheus.NewDesc("janusfs_cache_misses_total", "Total cache misses", nil, nil),
		cacheRebuilds:    prometheus.NewDesc("janusfs_cache_rebuilds_total", "Total cache rebuilds", nil, nil),
		configReloads:    prometheus.NewDesc("janusfs_config_reloads_total", "Total config reloads", nil, nil),
		watcherEvents:    prometheus.NewDesc("janusfs_watcher_events_total", "Total watcher events", nil, nil),
		eventsDropped:    prometheus.NewDesc("janusfs_events_dropped_total", "Total events dropped", nil, nil),
		generation:       prometheus.NewDesc("janusfs_generation", "Current rule generation", nil, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.opsTotal
	ch <- c.bytesServedTotal
	ch <- c.cacheHits
	ch <- c.cacheMisses
	ch <- c.cacheRebuilds
	ch <- c.configReloads
	ch <- c.watcherEvents
	ch <- c.eventsDropped
	ch <- c.generation
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.m.OpsTotal.Range(func(key, value any) bool {
		k := key.(string)
		parts := strings.SplitN(k, ":", 2)
		if len(parts) == 2 {
			v := value.(*atomic.Uint64)
			ch <- prometheus.MustNewConstMetric(c.opsTotal, prometheus.CounterValue, float64(v.Load()), parts[0], parts[1])
		}
		return true
	})

	c.m.BytesServed.Range(func(key, value any) bool {
		decision := key.(string)
		v := value.(*atomic.Int64)
		ch <- prometheus.MustNewConstMetric(c.bytesServedTotal, prometheus.CounterValue, float64(v.Load()), decision)
		return true
	})

	ch <- prometheus.MustNewConstMetric(c.cacheHits, prometheus.CounterValue, float64(c.m.CacheHit.Load()))
	ch <- prometheus.MustNewConstMetric(c.cacheMisses, prometheus.CounterValue, float64(c.m.CacheMiss.Load()))
	ch <- prometheus.MustNewConstMetric(c.cacheRebuilds, prometheus.CounterValue, float64(c.m.CacheRebuild.Load()))
	ch <- prometheus.MustNewConstMetric(c.configReloads, prometheus.CounterValue, float64(c.m.ConfigReloads.Load()))
	ch <- prometheus.MustNewConstMetric(c.watcherEvents, prometheus.CounterValue, float64(c.m.WatcherEvents.Load()))
	ch <- prometheus.MustNewConstMetric(c.eventsDropped, prometheus.CounterValue, float64(c.m.EventsDropped.Load()))
	ch <- prometheus.MustNewConstMetric(c.generation, prometheus.GaugeValue, float64(c.m.Generation.Load()))
}
