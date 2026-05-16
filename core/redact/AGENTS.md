# AGENTS.md — `core/redact`

## When to use this package

- Wrapping ANY error that came from a backend (database, broker, cloud SDK) before it leaves the function.
- Logging an error with `slog` — use `redact.Error(err)` not `slog.Any("error", err)`.
- Recovering from a panic — `redact.PanicValue(rec)` instead of `fmt.Sprintf("%v", rec)`.

## When to use something else

- **Domain errors raised by your own code (apperror.*):** these are designed to be safe-by-default; redaction is unnecessary.
- **Validation errors carrying field-level context:** preserve the field metadata; redaction would obscure useful signal.

## Key APIs

- `redact.WrapError(prefix, err)` — wraps `err` so `err.Error()` returns a redacted message (`"prefix: <redacted error: *type>"`). `errors.Is` / `errors.As` still unwrap the chain — only the rendered text is opaque.
- `redact.Error(err)` — returns a `slog.Attr` that renders the wrapped form.
- `redact.String(key, value)` — wraps a string in a redacted slog attr. Use for ALL caller-controlled string fields in logs (user IDs, paths, tokens, hostnames).
- `redact.Panic(rec)` — `slog.Attr` for panic values.
- `redact.PanicValue(rec)` — string form for embedding in error messages.
- `redact.WrapSentinel(sentinel, cause)` — wraps a cause behind a typed sentinel so `errors.Is(err, sentinel)` works without leaking the cause text.

## Common mistakes

- **`fmt.Errorf("op failed: %w", err)` on a backend error** — the inner text is now in your error message. Use `redact.WrapError("op", err)` instead.
- **`slog.Any("error", err)` in handler logs** — same problem. Use `redact.Error(err)`.
- **Asserting on inner error text in tests** — once redaction is applied, the inner text is hidden. Assert via `errors.Is(err, sentinel)` or compare error chains via `errors.Unwrap`.
- **`redact.WrapError(prefix, nil)`** — returns nil. Don't write `if redact.WrapError(...) != nil` thinking it's a builder; it's a function that conditionally returns a wrapper.

## The kit-wide rule

EVERY new function that handles a backend error must wrap it with `redact.WrapError` before returning. The `make check-fmt-errorf-wrap` tool (wave 143) is the enforcement mechanism — it flags `%w` over locals that look like backend errors.
