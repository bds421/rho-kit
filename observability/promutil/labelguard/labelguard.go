// Package labelguard enforces a label-value allowlist on Prometheus
// CounterVec / HistogramVec at observation time.
//
// Why: Prometheus cardinality is the single biggest operational
// foot-gun the kit's metrics surface. A handler that accidentally
// uses a raw user ID, request path, or tenant ID as a metric label
// blows the time-series cardinality up to user-count × everything-
// else and brings the scraper to a halt. Type-checked allowlists at
// the *observation* call site catch the bug at the moment the bad
// value is produced, not three days later when the Prometheus pod is
// OOMing.
//
// Rejected observations are silently dropped (labels are usually
// user-input-derived, panicking would crash production on a probe-
// crafted request) and surface on a counter
// `labelguard_rejected_total{vec, label}` so SREs can alert on
// "values being silently dropped" before it masks a real metric gap.
package labelguard

import (
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/promutil"
)

// rejectedMetricName is the Prometheus name for the dropped-
// observation counter. Stable across the kit so dashboards can be
// shared.
const rejectedMetricName = "labelguard_rejected_total"

// AllowedLabels enforces a label-value allowlist over a CounterVec or
// HistogramVec at observation time. Construct one per logical metric
// surface (or share one across vecs that share an allowlist) and call
// the Observe* methods with the label set you intend to record.
//
// Zero value is not usable; use [New].
type AllowedLabels struct {
	// allowed is keyed by label name; the inner map is keyed by the
	// permitted value with a presence flag. Lookup is O(1).
	allowed map[string]map[string]struct{}

	// rejected counts dropped observations, sliced by the vec's name
	// (Prometheus collectors expose this via Desc().String() but we
	// take the name explicitly to keep the label cardinality bounded
	// to what the registrar declared) and the offending label name.
	rejected *prometheus.CounterVec

	// vecNameMu guards vecName lookups against the underlying
	// CounterVec; CounterVec.WithLabelValues is goroutine-safe but we
	// only need a write lock for the once-per-vec name resolution.
	vecNameMu sync.Mutex
	vecNames  map[prometheus.Collector]string
}

// Option configures an AllowedLabels.
type Option func(*config)

type config struct {
	registerer prometheus.Registerer
}

// WithRegisterer overrides the Prometheus registerer used to register
// the rejected-observations counter. Defaults to
// [prometheus.DefaultRegisterer]; tests should pass a fresh
// [prometheus.NewRegistry] to keep state isolated across runs.
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(c *config) {
		c.registerer = reg
	}
}

// New returns an AllowedLabels enforcing the supplied allowlist.
//
// The allowed map is keyed by label NAME; each value is the slice of
// permitted label VALUES for that label. Labels not present in the
// map are unconstrained — the guard only rejects observations whose
// label is *in* the map but whose value is *not* in the slice.
//
// New panics if allowed is nil — a nil allowlist would silently
// disable every guard, almost certainly a wiring bug.
func New(allowed map[string][]string, opts ...Option) *AllowedLabels {
	if allowed == nil {
		panic("labelguard: allowed map must not be nil")
	}
	cfg := config{registerer: prometheus.DefaultRegisterer}
	for _, o := range opts {
		o(&cfg)
	}

	// Copy the input into the internal lookup shape. The copy makes
	// the AllowedLabels insensitive to caller-side mutations of the
	// map after construction — important for long-lived guards.
	idx := make(map[string]map[string]struct{}, len(allowed))
	for label, vals := range allowed {
		set := make(map[string]struct{}, len(vals))
		for _, v := range vals {
			set[v] = struct{}{}
		}
		idx[label] = set
	}

	rejected := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: rejectedMetricName,
		Help: "Observations dropped because a label value was outside the allowlist.",
	}, []string{"vec", "label"})

	// Reuse on AlreadyRegisteredError so multiple guards in one
	// process share the same rejected counter. Operators read one
	// metric, not N copies.
	live := registerOrReuse(cfg.registerer, rejected)

	return &AllowedLabels{
		allowed:  idx,
		rejected: live,
		vecNames: make(map[prometheus.Collector]string),
	}
}

// registerOrReuse delegates to promutil so we get the kit's standard
// "reuse on conflict" semantics.
func registerOrReuse(reg prometheus.Registerer, c *prometheus.CounterVec) *prometheus.CounterVec {
	got, err := promutil.Register(reg, c)
	if err != nil {
		// promutil.Register returns nil error on AlreadyRegistered;
		// any non-nil err is a real registration failure (e.g. name
		// conflict with a different shape). Surfacing as panic
		// matches RegisterCollector's behaviour.
		panic(err)
	}
	if cv, ok := got.(*prometheus.CounterVec); ok {
		return cv
	}
	// Defensive: if the registry returned a different shape, fall
	// back to the freshly-constructed counter so the guard remains
	// usable. Cardinality remains correct because every observation
	// goes through this single instance.
	return c
}

// ObserveCounter increments vec(labels) iff every label in labels
// satisfies the allowlist. Otherwise the offending label is logged
// to the rejected counter and the observation is dropped.
func (g *AllowedLabels) ObserveCounter(vec *prometheus.CounterVec, labels prometheus.Labels) {
	if vec == nil {
		return
	}
	if !g.permit(vec, labels) {
		return
	}
	vec.With(labels).Inc()
}

// ObserveHistogram observes val on vec(labels) iff every label in
// labels satisfies the allowlist. Otherwise the offending label is
// logged to the rejected counter and the observation is dropped.
func (g *AllowedLabels) ObserveHistogram(vec *prometheus.HistogramVec, labels prometheus.Labels, val float64) {
	if vec == nil {
		return
	}
	if !g.permit(vec, labels) {
		return
	}
	vec.With(labels).Observe(val)
}

// permit checks every label against the allowlist, increments the
// rejected counter for any violation, and returns whether the
// observation may proceed. Returns true when no violation is found.
//
// We continue iterating past the first violation so a single bad
// observation that picks up two un-allowed values is logged for
// *both* — operators see the full footprint of the bug rather than
// just the first label.
func (g *AllowedLabels) permit(vec prometheus.Collector, labels prometheus.Labels) bool {
	ok := true
	name := g.vecName(vec)
	for label, value := range labels {
		set, guarded := g.allowed[label]
		if !guarded {
			continue
		}
		if _, allowed := set[value]; allowed {
			continue
		}
		g.rejected.WithLabelValues(name, label).Inc()
		ok = false
	}
	return ok
}

// vecName extracts a stable, low-cardinality name for the supplied
// vec. We cache the result keyed by the collector pointer so the
// O(scan) Describe call only happens once per vec.
func (g *AllowedLabels) vecName(c prometheus.Collector) string {
	g.vecNameMu.Lock()
	defer g.vecNameMu.Unlock()
	if name, ok := g.vecNames[c]; ok {
		return name
	}
	name := describeName(c)
	g.vecNames[c] = name
	return name
}

// describeName pulls the FQName out of the first descriptor a
// collector emits. Falls back to "<unknown>" when the collector
// supplies no descriptor, which would be unusual for the *Vec types
// this package targets.
//
// Uses a buffered channel large enough for the typical fanout (16
// covers every kit-emitted vec) and runs Describe synchronously, so
// each cache miss is one allocation rather than one goroutine —
// previous versions leaked an ephemeral goroutine per first-time vec.
func describeName(c prometheus.Collector) string {
	ch := make(chan *prometheus.Desc, 16)
	c.Describe(ch)
	close(ch)
	for d := range ch {
		// fqNameFromDesc parses the FQName out of *prometheus.Desc's
		// String form: `Desc{fqName: "X", help: "...", ...}`. We use
		// the public fqName accessor when one exists; otherwise the
		// String form is the only stable surface.
		if d == nil {
			continue
		}
		return parseFQName(d.String())
	}
	return "<unknown>"
}

// parseFQName extracts the fqName from prometheus.Desc.String(),
// which has shape:
//
//	Desc{fqName: "labelguard_rejected_total", help: "...", ...}
//
// Returns "<unknown>" if the marker isn't found — defensive against
// future client_golang format changes.
func parseFQName(s string) string {
	const marker = `fqName: "`
	i := strings.Index(s, marker)
	if i < 0 {
		return "<unknown>"
	}
	rest := s[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "<unknown>"
	}
	return rest[:end]
}
