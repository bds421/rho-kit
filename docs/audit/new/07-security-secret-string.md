# NEW: core/secret

**Phase**: 4 (Tier‑1 missing primitive)
**Module path**: `github.com/bds421/rho-kit/core/secret`

## Why

Today every consumer that loads a secret from env-or-file holds it as a `string` or `[]byte`. Accidental `fmt.Printf("%v", cfg)`, `slog.Info("config", "cfg", cfg)`, or `json.Marshal(cfg)` all leak the value into logs / pages / responses. The kit needs an opinionated wrapper that *refuses* to print or marshal.

## Public API

```go
package secret

// String wraps a sensitive string value. It refuses to render via String(),
// MarshalJSON, MarshalYAML, MarshalText, or %v/%+v formatters — they all emit
// the literal "<redacted>". Callers must call .Reveal() to access the value.
type String struct { /* ... */ }

// New takes ownership of the underlying bytes. The input may be zeroed by the
// caller after construction (the bytes are copied internally).
func New(b []byte) *String
func NewFromString(s string) *String

// Reveal returns the underlying value. The returned []byte must NOT be
// retained or modified by the caller — it's a copy used to discourage
// accidental long-term storage.
func (s *String) Reveal() []byte

// RevealString is the string variant.
func (s *String) RevealString() string

// Close zeroes the internal buffer. Call this when the secret is no longer
// needed (e.g., during graceful shutdown).
func (s *String) Close() error

// String, GoString, MarshalJSON, MarshalText, MarshalYAML, Format all return
// "<redacted>".
func (s *String) String() string                  { return "<redacted>" }
func (s *String) GoString() string                { return "<redacted>" }
func (s *String) MarshalJSON() ([]byte, error)    { return []byte(`"<redacted>"`), nil }
func (s *String) MarshalText() ([]byte, error)    { return []byte("<redacted>"), nil }
func (s *String) Format(f fmt.State, c rune)      { f.Write([]byte("<redacted>")) }
```

## Integration with config loader

`core/config.Load` already supports `_FILE` suffix for mounted secrets. Add a struct-tag `secret:"true"` (or detect the `*secret.String` field type) and have `Load` populate the field with `secret.New(...)` directly.

```go
type Config struct {
    DBPassword *secret.String `env:"DB_PASSWORD"`
}
```

## Integration with `logattr`

`logattr.Secret(key, val)` (proposed in [existing/16](../existing/16-observability.md)) becomes a thin wrapper around `secret.String` for cases where consumers don't want a long-lived wrapper but do want to log redacted.

## Definition of done

- [ ] Package + tests covering all marshal/format paths emit `<redacted>`.
- [ ] `Reveal` returns a copy (not the internal buffer).
- [ ] `Close` zeroes the buffer; subsequent `Reveal` returns empty.
- [ ] `core/config.Load` integrates with `*secret.String` typed fields.
- [ ] `logattr.Secret` updated to delegate.
- [ ] Recipe in `docs/ai/security.md`.
