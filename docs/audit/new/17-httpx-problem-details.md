# NEW: httpx/problemdetails

**Phase**: 3 (DX)
**Module path**: `github.com/bds421/rho-kit/httpx/problemdetails`

## Why

`httpx.WriteError` writes a kit-specific envelope (`{error, code}`). Public-facing APIs targeting interop typically want **RFC 7807** problem-details (`application/problem+json`) — `type`, `title`, `status`, `detail`, `instance` plus arbitrary extension fields.

A separate writer keeps the envelope tidy and adds the standard format for consumers that need it.

## Public API

```go
package problemdetails

// Problem is the RFC 7807 envelope.
type Problem struct {
    Type     string         `json:"type"`              // URI identifying problem type
    Title    string         `json:"title"`             // short, human-readable summary
    Status   int            `json:"status"`            // HTTP status code
    Detail   string         `json:"detail,omitempty"`  // human-readable explanation
    Instance string         `json:"instance,omitempty"` // URI of the specific occurrence
    Extensions map[string]any `json:"-"`                // marshaled inline (custom MarshalJSON)
}

// Write writes a problem-details response with Content-Type application/problem+json.
func Write(w http.ResponseWriter, p Problem)

// FromError maps a core/apperror.Error to a Problem with sensible defaults.
// Type URIs default to "https://errors.bds421.dev/<code>" — override with a
// base URL via WithBaseURL.
func FromError(err error, opts ...Option) Problem

// Option lets callers configure type-URI prefixes, instance generators, etc.
type Option func(*config)

func WithBaseURL(string) Option
func WithInstanceFromRequest(*http.Request) Option
```

## Integration

- Doesn't replace `WriteError`; sits alongside.
- `httpx.WriteServiceError` gains an opt-in mode: `WithProblemDetails()` causes it to delegate to `problemdetails.Write(FromError(...))`.
- Service authors pick per-route which envelope to use.

## Definition of done

- [ ] Package with Problem + Write + FromError.
- [ ] Tests: round-trip + extensions; correct Content-Type; status code set.
- [ ] `httpx.WriteServiceError` opt-in delegation.
- [ ] Recipe in `docs/ai/http.md`.
