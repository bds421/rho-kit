package tracing

import (
	"fmt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/bds421/rho-kit/core/v2/tenant"
)

// TenantSampler is a [sdktrace.Sampler] that picks per-tenant sample
// rates from the context. It must be wrapped in [sdktrace.ParentBased]
// to preserve distributed-trace consistency — see [newTenantSampler]
// where the wrapping happens.
//
// Use this when a small set of high-volume tenants warrants a lower
// sample rate than the default, or vice versa (a noisy debug tenant
// running at 1.0 while the rest run at 0.05). Without this, operators
// either over-sample (high cost) or under-sample (no signal on
// low-volume tenants).
//
// # Construction
//
// Callers don't instantiate this type directly. Set
// [Config.TenantSampleRates] before calling [Init]; the toolkit
// builds the wrapper and wires it as the tracer provider's root
// sampler.
//
// asvs: V11.1.1
type tenantSampler struct {
	defaultSampler sdktrace.Sampler
	overrides      map[string]sdktrace.Sampler
}

// ShouldSample implements [sdktrace.Sampler]. It reads the tenant ID
// from parameters.ParentContext (set by upstream middleware via
// [tenant.WithID]) and selects the override sampler if present.
// Falls through to the default sampler for requests with no tenant
// context.
func (s tenantSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if id, ok := tenant.FromContext(p.ParentContext); ok {
		if override, found := s.overrides[id.String()]; found {
			return override.ShouldSample(p)
		}
	}
	return s.defaultSampler.ShouldSample(p)
}

// Description identifies the sampler for OpenTelemetry's own
// diagnostics (collector logs include the sampler description on
// every export tick).
func (s tenantSampler) Description() string {
	// Emit only the override count — raw tenant IDs are topology that
	// Config.LogValue deliberately redacts, and this description is
	// exported by the OTel SDK on every export tick.
	return fmt.Sprintf("TenantSampler{default=%s, overrides=%d tenants}", s.defaultSampler.Description(), len(s.overrides))
}

// newTenantSampler constructs the per-tenant sampler tree and wraps
// it in [sdktrace.ParentBased] so that downstream services receiving
// an upstream-sampled trace remain sampled (and vice versa). The
// per-tenant logic only kicks in for *root* sampling decisions,
// which is the only place a service can choose for itself.
//
// Returns an error if any override rate is outside [0, 1] — invalid
// rates would silently truncate to TraceIDRatioBased's clamped
// behaviour and confuse operators looking at the actual sample
// fraction.
func newTenantSampler(defaultRate float64, overrides map[string]float64) (sdktrace.Sampler, error) {
	if defaultRate < 0 || defaultRate > 1 {
		return nil, fmt.Errorf("tracing: default sample rate must be in [0, 1] (got %.4f)", defaultRate)
	}
	if len(overrides) == 0 {
		// Match the legacy code path verbatim when no tenant
		// overrides are present, so a service that doesn't use the
		// feature gets identical behaviour to v2.0.0-pre-tenant.
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(defaultRate)), nil
	}

	built := make(map[string]sdktrace.Sampler, len(overrides))
	for id, rate := range overrides {
		if id == "" {
			return nil, fmt.Errorf("tracing: TenantSampleRates contains empty tenant ID")
		}
		if rate < 0 || rate > 1 {
			return nil, fmt.Errorf("tracing: tenant %q sample rate must be in [0, 1] (got %.4f)", id, rate)
		}
		built[id] = sdktrace.TraceIDRatioBased(rate)
	}

	return sdktrace.ParentBased(tenantSampler{
		defaultSampler: sdktrace.TraceIDRatioBased(defaultRate),
		overrides:      built,
	}), nil
}
