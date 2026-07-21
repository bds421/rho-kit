package signedrequest

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Verify-failure reason labels. The set is intentionally small and
// closed: any future-failure-mode addition must extend this list and
// the documented dashboard contract together so operators do not see
// silently-dropped categories.
const (
	verifyReasonExpired            = "expired"
	verifyReasonClockSkew          = "clock_skew"
	verifyReasonBadSignature       = "bad_signature"
	verifyReasonMissingHeader      = "missing_header"
	verifyReasonMalformedSignature = "malformed_signature"
	verifyReasonReplayNonce        = "replay_nonce"
	// verifyReasonStoreError covers NonceStore backend failures (e.g. a
	// Redis outage). These are server-side dependency faults, not
	// client-attributable rejections, so they get a dedicated label
	// rather than being lumped under bad_signature where an infra outage
	// would masquerade as a forged-signature attack spike.
	verifyReasonStoreError = "store_error"
	// verifyReasonBodyTooLarge is a client payload-size rejection (HTTP 413),
	// not a malformed signature. Keeping it distinct stops a partner
	// exceeding WithBodyMaxSize from looking like a tampering spike.
	verifyReasonBodyTooLarge = "body_too_large"
	// verifyReasonMisconfigured covers server-side wiring faults such as
	// ErrSecretTooShort (resolver returned a key shorter than the MAC
	// requires). These are operator-actionable 500s, not client forgeries.
	verifyReasonMisconfigured = "misconfigured"
)

// Metrics holds Prometheus collectors for signed-request verification.
//
// The label set is deliberately small: reason is one of the
// package-defined verify-reason constants. The middleware does not
// label by route, key ID, or remote IP — those are request-derived
// dimensions that belong in trace exemplars rather than long-lived
// metric labels.
type Metrics struct {
	verifyFailures *prometheus.CounterVec
}

// MetricsOption configures signed-request metric construction.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for the
// signed-request metrics. When unset, [prometheus.DefaultRegisterer]
// is used.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("signedrequest: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers signed-request metrics. Pass
// [WithRegisterer] to use a non-default registry. Repeated
// calls reuse already-registered collectors on the same registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("signedrequest: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	m := &Metrics{
		verifyFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "signing",
			Name:      "verify_failures_total",
			Help:      "Total signed-request verification failures by reason. Reasons are a closed set: expired, clock_skew, bad_signature, missing_header, malformed_signature, replay_nonce, store_error, body_too_large, misconfigured.",
		}, []string{"reason"}),
	}
	m.verifyFailures = promutil.MustRegisterOrGet(cfg.registerer, m.verifyFailures)
	return m
}

func (m *Metrics) observeVerifyFailure(reason string) {
	if m == nil {
		return
	}
	m.verifyFailures.WithLabelValues(reason).Inc()
}

// errTimestampClockSkew is returned by verify when the producer's
// timestamp is too far in the future of the verifier's clock. It
// unwraps to [ErrTimestampInvalid] so existing error handling
// (writeError → 400) is unchanged; the wrapper exists purely so
// metrics can split "future-skew" from "past-maxAge" without
// re-deriving the direction.
var errTimestampClockSkew = errors.New("signedrequest: timestamp too far in the future")

// errTimestampExpired is the symmetric wrapper for past-maxAge
// timestamps. Unwraps to [ErrTimestampInvalid] so writeError keeps
// returning 400 without changes.
var errTimestampExpired = errors.New("signedrequest: timestamp older than maxClockSkew")

// classifyVerifyFailure maps an error returned by [verify] to one of
// the verify-reason label constants. The classification is the only
// place the package decides how to project its internal sentinels onto
// the bounded metric label set; all callers (and any future
// metrics-aware code path) MUST route through this helper so the
// label set stays closed.
func classifyVerifyFailure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errTimestampClockSkew):
		return verifyReasonClockSkew
	case errors.Is(err, errTimestampExpired):
		return verifyReasonExpired
	case errors.Is(err, ErrMissingHeaders):
		return verifyReasonMissingHeader
	case errors.Is(err, ErrNonceReplayed):
		return verifyReasonReplayNonce
	case errors.Is(err, ErrNonceStore):
		// NonceStore backend failure (e.g. Redis outage) — a server-side
		// dependency fault, not a client-attributable rejection. Keep it
		// out of bad_signature so an outage is not misread as an attack.
		return verifyReasonStoreError
	case errors.Is(err, ErrBodyTooLarge):
		return verifyReasonBodyTooLarge
	case errors.Is(err, ErrSecretTooShort):
		return verifyReasonMisconfigured
	case errors.Is(err, ErrNonceInvalid),
		errors.Is(err, ErrTimestampInvalid),
		errors.Is(err, ErrInvalidRequest),
		errors.Is(err, ErrBodyReadFailed):
		return verifyReasonMalformedSignature
	default:
		// ErrSignatureInvalid collapses into "bad_signature". Other
		// unresolved errors (including unexpected resolver faults that
		// are not ErrSecretTooShort) also funnel here because the
		// wire-level failure mode is identical (request did not auth).
		return verifyReasonBadSignature
	}
}
