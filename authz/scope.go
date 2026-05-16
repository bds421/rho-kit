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
// Then pass ScopeUsersRead to httpx middleware constructors — the
// compiler enforces that you used a real registered scope and not
// a typo of one.
type Scope string

// scopePattern bounds Scope to safe characters: lowercase ASCII, digits,
// dot, and underscore. Mirrors the OAuth2 RFC 6749 §3.3 production but
// stricter (no spaces; spaces are the on-wire separator already).
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)

type registry struct {
	mu     sync.RWMutex
	scopes map[Scope]string // scope → description
}

var globalScopes = &registry{scopes: map[Scope]string{}}

// Register catalogues scope with a human-readable description for
// OpenAPI generation. Returns ErrScopeAlreadyRegistered if the same
// scope name was registered with a different description (re-registering
// with the same description is a no-op so package init blocks survive
// re-execution under hot-reload tests). Returns an error if scope does
// not match [scopePattern].
//
// Concurrency-safe: callers can register from multiple init() blocks
// without external locking.
func Register(scope Scope, description string) (Scope, error) {
	if !scopePattern.MatchString(string(scope)) {
		return "", fmt.Errorf("authz: Register scope %q must match %s", scope, scopePattern)
	}
	if description == "" {
		return "", fmt.Errorf("authz: Register scope %q requires a non-empty description for OpenAPI generation", scope)
	}
	globalScopes.mu.Lock()
	defer globalScopes.mu.Unlock()
	if existing, ok := globalScopes.scopes[scope]; ok {
		if existing != description {
			return "", fmt.Errorf("authz: Register scope %q already registered with different description", scope)
		}
		return scope, nil
	}
	globalScopes.scopes[scope] = description
	return scope, nil
}

// MustRegister is the panicking wrapper of [Register] for use in
// package-level var declarations.
func MustRegister(scope Scope, description string) Scope {
	got, err := Register(scope, description)
	if err != nil {
		panic(err)
	}
	return got
}

// RegisteredScopes returns every Register-d scope in deterministic
// order (alphabetical by scope name) along with its description.
// OpenAPI security-scheme generators consume this to populate the
// scopes section of a Bearer/OAuth2 spec without each service
// hand-maintaining a parallel list.
func RegisteredScopes() []ScopeEntry {
	globalScopes.mu.RLock()
	defer globalScopes.mu.RUnlock()
	out := make([]ScopeEntry, 0, len(globalScopes.scopes))
	for scope, description := range globalScopes.scopes {
		out = append(out, ScopeEntry{Scope: scope, Description: description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope < out[j].Scope })
	return out
}

// IsRegistered reports whether scope has been registered. Useful
// in tests to confirm a service's required scope catalogue is
// initialised before request handling begins.
func IsRegistered(scope Scope) bool {
	globalScopes.mu.RLock()
	defer globalScopes.mu.RUnlock()
	_, ok := globalScopes.scopes[scope]
	return ok
}

// ScopeEntry pairs a registered Scope with its human-readable
// description. Returned by [RegisteredScopes].
type ScopeEntry struct {
	Scope       Scope
	Description string
}

// resetScopesForTest clears the global registry. Used only in tests.
func resetScopesForTest() {
	globalScopes.mu.Lock()
	defer globalScopes.mu.Unlock()
	globalScopes.scopes = map[Scope]string{}
}
