// Package runtimemetrics registers Go runtime metrics with Prometheus.
//
// The prometheus client library ships its own NewGoCollector, but the
// kit registers a slightly smaller curated set so dashboards stay
// portable across services and so the collector cardinality stays
// predictable. Specifically we expose:
//
//   - go_goroutines (gauge) — current goroutine count
//   - go_threads (gauge) — OS threads owned by the runtime
//   - go_heap_alloc_bytes (gauge) — currently-live heap bytes
//   - go_heap_sys_bytes (gauge) — heap reservation from the OS
//   - go_gc_pause_seconds_sum (counter) — cumulative GC pause time
//   - go_gc_count_total (counter) — total GC cycles
//   - go_max_rss_bytes (gauge, Linux only) — max resident-set size
//
// All metrics derive from runtime.MemStats / runtime.NumGoroutine /
// runtime.NumCPU. Cost is one mallocless ReadMemStats per scrape.
package runtimemetrics

import (
	"runtime"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Register registers the curated runtime collector with reg. Pass nil
// to skip registration (e.g. for tests that gather from an isolated
// registry built later).
//
// Idempotent across processes: a second Register on the same registry
// silently no-ops via promutil's AlreadyRegistered branch.
func Register(reg prometheus.Registerer) {
	if reg == nil {
		return
	}
	promutil.RegisterCollector(reg, newCollector())
}

// collector implements prometheus.Collector by reading runtime stats
// on each Collect call.
type collector struct {
	goroutines *prometheus.Desc
	threads    *prometheus.Desc
	heapAlloc  *prometheus.Desc
	heapSys    *prometheus.Desc
	gcPauseSum *prometheus.Desc
	gcCount    *prometheus.Desc
	maxRSS     *prometheus.Desc
}

func newCollector() *collector {
	return &collector{
		goroutines: prometheus.NewDesc("go_goroutines", "Number of currently running goroutines.", nil, nil),
		threads:    prometheus.NewDesc("go_threads", "Number of OS threads owned by the Go runtime.", nil, nil),
		heapAlloc:  prometheus.NewDesc("go_heap_alloc_bytes", "Currently-allocated heap bytes (in use).", nil, nil),
		heapSys:    prometheus.NewDesc("go_heap_sys_bytes", "Heap memory reserved from the OS.", nil, nil),
		gcPauseSum: prometheus.NewDesc("go_gc_pause_seconds_sum", "Cumulative GC stop-the-world pause time, in seconds.", nil, nil),
		gcCount:    prometheus.NewDesc("go_gc_count_total", "Total number of completed GC cycles.", nil, nil),
		maxRSS:     prometheus.NewDesc("go_max_rss_bytes", "Maximum resident-set size in bytes (rusage; Linux/Darwin).", nil, nil),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.goroutines
	ch <- c.threads
	ch <- c.heapAlloc
	ch <- c.heapSys
	ch <- c.gcPauseSum
	ch <- c.gcCount
	ch <- c.maxRSS
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	ch <- prometheus.MustNewConstMetric(c.goroutines, prometheus.GaugeValue, float64(runtime.NumGoroutine()))

	threads, _ := runtime.ThreadCreateProfile(nil)
	ch <- prometheus.MustNewConstMetric(c.threads, prometheus.GaugeValue, float64(threads))

	ch <- prometheus.MustNewConstMetric(c.heapAlloc, prometheus.GaugeValue, float64(ms.HeapAlloc))
	ch <- prometheus.MustNewConstMetric(c.heapSys, prometheus.GaugeValue, float64(ms.HeapSys))

	// PauseTotalNs is cumulative across the lifetime of the process.
	ch <- prometheus.MustNewConstMetric(c.gcPauseSum, prometheus.CounterValue, float64(ms.PauseTotalNs)/1e9)
	ch <- prometheus.MustNewConstMetric(c.gcCount, prometheus.CounterValue, float64(ms.NumGC))

	if rss := readMaxRSS(); rss >= 0 {
		ch <- prometheus.MustNewConstMetric(c.maxRSS, prometheus.GaugeValue, float64(rss))
	}
}
