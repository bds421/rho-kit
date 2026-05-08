package flags

import (
	"context"
	"sync"

	"github.com/open-feature/go-sdk/openfeature"
)

// MemoryProvider is a goroutine-safe in-memory [Provider] for tests
// and local development. It stores fixed values per flag key with no
// targeting, segmentation, or rollouts — those are the OpenFeature
// SDK's job and belong in real providers.
//
// The kit ships this rather than reaching for the SDK's reference
// in-memory provider because the kit's contract is "construct the
// provider, hand it to flags.New" — no per-test SDK plumbing. Build
// MemoryProvider with [NewMemoryProvider], populate via Set, hand to
// New.
type MemoryProvider struct {
	mu     sync.RWMutex
	bools  map[string]bool
	strs   map[string]string
	ints   map[string]int64
	floats map[string]float64
	objs   map[string]any
}

// NewMemoryProvider returns an empty in-memory provider.
func NewMemoryProvider() *MemoryProvider {
	return &MemoryProvider{
		bools:  map[string]bool{},
		strs:   map[string]string{},
		ints:   map[string]int64{},
		floats: map[string]float64{},
		objs:   map[string]any{},
	}
}

// SetBool registers a boolean value for key.
func (p *MemoryProvider) SetBool(key string, v bool) {
	p.mu.Lock()
	p.bools[key] = v
	p.mu.Unlock()
}

// SetString registers a string value for key.
func (p *MemoryProvider) SetString(key, v string) {
	p.mu.Lock()
	p.strs[key] = v
	p.mu.Unlock()
}

// SetInt registers an integer value for key.
func (p *MemoryProvider) SetInt(key string, v int64) {
	p.mu.Lock()
	p.ints[key] = v
	p.mu.Unlock()
}

// SetFloat registers a float value for key.
func (p *MemoryProvider) SetFloat(key string, v float64) {
	p.mu.Lock()
	p.floats[key] = v
	p.mu.Unlock()
}

// SetObject registers an opaque-shape value for key.
func (p *MemoryProvider) SetObject(key string, v any) {
	p.mu.Lock()
	p.objs[key] = v
	p.mu.Unlock()
}

// Metadata implements [openfeature.FeatureProvider].
func (p *MemoryProvider) Metadata() openfeature.Metadata {
	return openfeature.Metadata{Name: "rho-kit/flags/memory"}
}

// Hooks implements [openfeature.FeatureProvider].
func (p *MemoryProvider) Hooks() []openfeature.Hook { return nil }

// BooleanEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) BooleanEvaluation(_ context.Context, flag string, fallback bool, _ openfeature.FlattenedContext) openfeature.BoolResolutionDetail {
	p.mu.RLock()
	v, ok := p.bools[flag]
	p.mu.RUnlock()
	return openfeature.BoolResolutionDetail{Value: pickBool(ok, v, fallback), ProviderResolutionDetail: detail(ok)}
}

// StringEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) StringEvaluation(_ context.Context, flag string, fallback string, _ openfeature.FlattenedContext) openfeature.StringResolutionDetail {
	p.mu.RLock()
	v, ok := p.strs[flag]
	p.mu.RUnlock()
	return openfeature.StringResolutionDetail{Value: pickString(ok, v, fallback), ProviderResolutionDetail: detail(ok)}
}

// FloatEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) FloatEvaluation(_ context.Context, flag string, fallback float64, _ openfeature.FlattenedContext) openfeature.FloatResolutionDetail {
	p.mu.RLock()
	v, ok := p.floats[flag]
	p.mu.RUnlock()
	return openfeature.FloatResolutionDetail{Value: pickFloat(ok, v, fallback), ProviderResolutionDetail: detail(ok)}
}

// IntEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) IntEvaluation(_ context.Context, flag string, fallback int64, _ openfeature.FlattenedContext) openfeature.IntResolutionDetail {
	p.mu.RLock()
	v, ok := p.ints[flag]
	p.mu.RUnlock()
	return openfeature.IntResolutionDetail{Value: pickInt(ok, v, fallback), ProviderResolutionDetail: detail(ok)}
}

// ObjectEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) ObjectEvaluation(_ context.Context, flag string, fallback any, _ openfeature.FlattenedContext) openfeature.InterfaceResolutionDetail {
	p.mu.RLock()
	v, ok := p.objs[flag]
	p.mu.RUnlock()
	out := fallback
	if ok {
		out = v
	}
	return openfeature.InterfaceResolutionDetail{Value: out, ProviderResolutionDetail: detail(ok)}
}

func detail(found bool) openfeature.ProviderResolutionDetail {
	if found {
		return openfeature.ProviderResolutionDetail{Reason: openfeature.StaticReason}
	}
	return openfeature.ProviderResolutionDetail{Reason: openfeature.DefaultReason}
}

func pickBool(ok bool, v, fallback bool) bool {
	if ok {
		return v
	}
	return fallback
}

func pickString(ok bool, v, fallback string) string {
	if ok {
		return v
	}
	return fallback
}

func pickFloat(ok bool, v, fallback float64) float64 {
	if ok {
		return v
	}
	return fallback
}

func pickInt(ok bool, v, fallback int64) int64 {
	if ok {
		return v
	}
	return fallback
}
