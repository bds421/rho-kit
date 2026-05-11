package flags

import (
	"context"
	"encoding/json"
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
	mustValidateKey(key)
	p.mu.Lock()
	p.bools[key] = v
	p.mu.Unlock()
}

// SetString registers a string value for key.
func (p *MemoryProvider) SetString(key, v string) {
	mustValidateKey(key)
	p.mu.Lock()
	p.strs[key] = v
	p.mu.Unlock()
}

// SetInt registers an integer value for key.
func (p *MemoryProvider) SetInt(key string, v int64) {
	mustValidateKey(key)
	p.mu.Lock()
	p.ints[key] = v
	p.mu.Unlock()
}

// SetFloat registers a float value for key.
func (p *MemoryProvider) SetFloat(key string, v float64) {
	mustValidateKey(key)
	p.mu.Lock()
	p.floats[key] = v
	p.mu.Unlock()
}

// SetObject registers an opaque-shape value for key.
//
// FR-035 [LOW]: the value is JSON-marshalled and re-unmarshalled
// on store so callers cannot mutate provider-held state outside the
// provider lock. Non-marshallable values (channels, funcs) panic at
// SetObject so the configuration error surfaces at startup.
func (p *MemoryProvider) SetObject(key string, v any) {
	mustValidateKey(key)
	frozen, err := deepCopyJSON(v)
	if err != nil {
		panic("flags/memory: SetObject value must be JSON-marshallable")
	}
	p.mu.Lock()
	p.objs[key] = frozen
	p.mu.Unlock()
}

// deepCopyJSON round-trips v through JSON so the stored copy shares
// no references with the caller's input. Used by SetObject to enforce
// provider-side immutability (audit FR-035).
func deepCopyJSON(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Metadata implements [openfeature.FeatureProvider].
func (p *MemoryProvider) Metadata() openfeature.Metadata {
	return openfeature.Metadata{Name: "rho-kit/flags/memory"}
}

// Hooks implements [openfeature.FeatureProvider].
func (p *MemoryProvider) Hooks() []openfeature.Hook { return nil }

// BooleanEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) BooleanEvaluation(_ context.Context, flag string, fallback bool, _ openfeature.FlattenedContext) openfeature.BoolResolutionDetail {
	if invalid, ok := invalidKeyDetail(flag); ok {
		return openfeature.BoolResolutionDetail{Value: fallback, ProviderResolutionDetail: invalid}
	}
	p.mu.RLock()
	v, ok := p.bools[flag]
	p.mu.RUnlock()
	return openfeature.BoolResolutionDetail{Value: pickBool(ok, v, fallback), ProviderResolutionDetail: detail(ok)}
}

// StringEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) StringEvaluation(_ context.Context, flag string, fallback string, _ openfeature.FlattenedContext) openfeature.StringResolutionDetail {
	if invalid, ok := invalidKeyDetail(flag); ok {
		return openfeature.StringResolutionDetail{Value: fallback, ProviderResolutionDetail: invalid}
	}
	p.mu.RLock()
	v, ok := p.strs[flag]
	p.mu.RUnlock()
	return openfeature.StringResolutionDetail{Value: pickString(ok, v, fallback), ProviderResolutionDetail: detail(ok)}
}

// FloatEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) FloatEvaluation(_ context.Context, flag string, fallback float64, _ openfeature.FlattenedContext) openfeature.FloatResolutionDetail {
	if invalid, ok := invalidKeyDetail(flag); ok {
		return openfeature.FloatResolutionDetail{Value: fallback, ProviderResolutionDetail: invalid}
	}
	p.mu.RLock()
	v, ok := p.floats[flag]
	p.mu.RUnlock()
	return openfeature.FloatResolutionDetail{Value: pickFloat(ok, v, fallback), ProviderResolutionDetail: detail(ok)}
}

// IntEvaluation implements [openfeature.FeatureProvider].
func (p *MemoryProvider) IntEvaluation(_ context.Context, flag string, fallback int64, _ openfeature.FlattenedContext) openfeature.IntResolutionDetail {
	if invalid, ok := invalidKeyDetail(flag); ok {
		return openfeature.IntResolutionDetail{Value: fallback, ProviderResolutionDetail: invalid}
	}
	p.mu.RLock()
	v, ok := p.ints[flag]
	p.mu.RUnlock()
	return openfeature.IntResolutionDetail{Value: pickInt(ok, v, fallback), ProviderResolutionDetail: detail(ok)}
}

// ObjectEvaluation implements [openfeature.FeatureProvider].
//
// FR-035 [LOW]: returns a fresh JSON-decoded copy on every read so
// callers cannot mutate provider-held state.
func (p *MemoryProvider) ObjectEvaluation(_ context.Context, flag string, fallback any, _ openfeature.FlattenedContext) openfeature.InterfaceResolutionDetail {
	if invalid, ok := invalidKeyDetail(flag); ok {
		return openfeature.InterfaceResolutionDetail{Value: fallback, ProviderResolutionDetail: invalid}
	}
	p.mu.RLock()
	v, ok := p.objs[flag]
	p.mu.RUnlock()
	out := fallback
	if ok {
		// Best-effort re-clone — if the deep-copy fails (only
		// possible if the stored value is non-marshallable, which
		// SetObject prevents), fall back to the stored reference.
		if cloned, err := deepCopyJSON(v); err == nil {
			out = cloned
		} else {
			out = v
		}
	}
	return openfeature.InterfaceResolutionDetail{Value: out, ProviderResolutionDetail: detail(ok)}
}

func detail(found bool) openfeature.ProviderResolutionDetail {
	if found {
		return openfeature.ProviderResolutionDetail{Reason: openfeature.StaticReason}
	}
	return openfeature.ProviderResolutionDetail{Reason: openfeature.DefaultReason}
}

func invalidKeyDetail(flag string) (openfeature.ProviderResolutionDetail, bool) {
	if err := ValidateKey(flag); err != nil {
		return openfeature.ProviderResolutionDetail{
			ResolutionError: openfeature.NewParseErrorResolutionError(err.Error()),
			Reason:          openfeature.ErrorReason,
		}, true
	}
	return openfeature.ProviderResolutionDetail{}, false
}

func mustValidateKey(key string) {
	if err := ValidateKey(key); err != nil {
		panic("flags/memory: invalid key")
	}
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
