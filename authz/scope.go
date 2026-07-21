package authz

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Scope is a typed string for OAuth2-style scope identifiers.
// Wave 147 introduced this so services declare scopes once via
// [Register] and downstream tooling (httpx middleware, OpenAPI
// security generation) consumes the registered set instead of
// string-matching freeform names.
//
// The Scope value itself is the wire form (e.g. "users.read")
// preserved on tokens; the kit doesn't transform it, only catalogues
// it. Use the typed constructor pattern in your service:
//
//	var (
//	    ScopeUsersRead  = authz.MustRegister("users.read",  "List and inspect users")
//	    ScopeUsersWrite = authz.MustRegister("users.write", "Create or update users")
//	)
//
// Then pass ScopeUsersRead to httpx middleware constructors. Scope is a
// distinct named type so call sites cannot mix arbitrary strings with
// registered constants without an explicit conversion — but conversion
// from a string literal (or authz.Scope("users.raed")) still compiles.
// Registration defaults to a process-global catalogue ([DefaultRegistry]);
// use an explicit [Registry] when catalogues must be partitioned, and
// [IsRegistered] at startup if you need to assert the expected set is present.
type Scope string

// scopePattern bounds Scope to safe characters: lowercase ASCII, digits,
// dot, and underscore. Mirrors the OAuth2 RFC 6749 §3.3 production but
// stricter (no spaces; spaces are the on-wire separator already).
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

// Registry is an instantiable scope catalogue. Multi-service binaries
// and tests that must isolate scope sets should construct their own
// Registry rather than using the process-global [Register] helpers.
// The package-level functions delegate to [DefaultRegistry].
type Registry struct {
	mu     sync.RWMutex
	scopes map[Scope]string // scope → description
}

// NewRegistry returns an empty scope registry.
func NewRegistry() *Registry {
	return &Registry{scopes: map[Scope]string{}}
}

// DefaultRegistry is the process-global scope catalogue used by
// [Register], [MustRegister], [RegisteredScopes], and [IsRegistered].
// Prefer an explicit [Registry] when two libraries in one binary need
// isolated catalogues, or when tests must reset state between cases.
var DefaultRegistry = NewRegistry()

// Register catalogues scope on this registry with a human-readable
// description for OpenAPI generation. Same contract as the package-level
// [Register].
func (r *Registry) Register(scope Scope, description string) (Scope, error) {
	if r == nil {
		return "", fmt.Errorf("authz: Register requires a non-nil Registry")
	}
	if !scopePattern.MatchString(string(scope)) {
		return "", fmt.Errorf("authz: Register scope %q must match %s", scope, scopePattern)
	}
	if description == "" {
		return "", fmt.Errorf("authz: Register scope %q requires a non-empty description for OpenAPI generation", scope)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.scopes == nil {
		r.scopes = map[Scope]string{}
	}
	if existing, ok := r.scopes[scope]; ok {
		if existing != description {
			return "", fmt.Errorf("authz: Register scope %q already registered with different description", scope)
		}
		return scope, nil
	}
	r.scopes[scope] = description
	return scope, nil
}

// MustRegister is the panicking wrapper of [Registry.Register].
func (r *Registry) MustRegister(scope Scope, description string) Scope {
	got, err := r.Register(scope, description)
	if err != nil {
		panic(err)
	}
	return got
}

// Scopes returns every registered scope in deterministic order
// (alphabetical by scope name) along with its description.
func (r *Registry) Scopes() []ScopeEntry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ScopeEntry, 0, len(r.scopes))
	for scope, description := range r.scopes {
		out = append(out, ScopeEntry{Scope: scope, Description: description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope < out[j].Scope })
	return out
}

// IsRegistered reports whether scope has been registered on this registry.
func (r *Registry) IsRegistered(scope Scope) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.scopes[scope]
	return ok
}

// Reset clears every registered scope. Intended for tests that need to
// isolate the catalogue between cases; production code should not call it.
func (r *Registry) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes = map[Scope]string{}
}

// Register catalogues scope on [DefaultRegistry]. Returns a non-nil error
// if the same scope name was already registered with a different
// description (re-registering with the same description is a no-op so
// package init blocks survive re-execution under hot-reload tests).
// Returns an error if scope does not match [scopePattern]. These errors
// are plain (fmt.Errorf) values not exported sentinels, so do not
// errors.Is against them.
//
// Concurrency-safe: callers can register from multiple init() blocks
// without external locking.
func Register(scope Scope, description string) (Scope, error) {
	return DefaultRegistry.Register(scope, description)
}

// MustRegister is the panicking wrapper of [Register] for use in
// package-level var declarations.
func MustRegister(scope Scope, description string) Scope {
	return DefaultRegistry.MustRegister(scope, description)
}

// RegisteredScopes returns every Register-d scope in deterministic
// order (alphabetical by scope name) along with its description.
// OpenAPI security-scheme generators consume this to populate the
// scopes section of a Bearer/OAuth2 spec without each service
// hand-maintaining a parallel list.
func RegisteredScopes() []ScopeEntry {
	return DefaultRegistry.Scopes()
}

// IsRegistered reports whether scope has been registered. Useful
// in tests to confirm a service's required scope catalogue is
// initialised before request handling begins.
func IsRegistered(scope Scope) bool {
	return DefaultRegistry.IsRegistered(scope)
}

// ResetScopes clears [DefaultRegistry]. Exported for external tests that
// exercise Register and need isolation between cases; production code
// should not call it.
func ResetScopes() {
	DefaultRegistry.Reset()
}

// ScopeEntry pairs a registered Scope with its human-readable
// description. Returned by [RegisteredScopes].
type ScopeEntry struct {
	Scope       Scope
	Description string
}

// resetScopesForTest clears the global registry. Used only in this
// package's tests; external packages should call [ResetScopes].
func resetScopesForTest() {
	ResetScopes()
}
