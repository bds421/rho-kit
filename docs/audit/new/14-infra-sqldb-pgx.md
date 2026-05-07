# NEW: infra/sqldb/pgx

**Phase**: 5 (Tier‑2 infrastructure)
**Module path**: `github.com/bds421/rho-kit/infra/sqldb/pgx`

## Why

The kit uses `database/sql` via GORM. Some Postgres workloads need features that `database/sql` can't expose:

- **`LISTEN`/`NOTIFY`** — for low-latency event delivery within a single Postgres deployment (cheap alternative to Redis pub/sub for in-cluster needs).
- **`COPY`** — bulk-load 100k+ rows in a single round-trip.
- **Batched pipelines** — multiple statements in one network round-trip.
- **Custom types** — direct binary encoding for `jsonb`, arrays, etc.

A `pgx`-native option lets services that need these reach for them without abandoning the rest of the kit.

## Public API

```go
package pgx

type Config struct {
    DSN              string
    MaxConns         int32
    MinConns         int32
    MaxConnLifetime  time.Duration
    MaxConnIdleTime  time.Duration
    HealthCheckPeriod time.Duration
}

// Pool wraps pgxpool.Pool with lifecycle.Component conformance.
type Pool struct { /* ... */ }

func New(ctx context.Context, cfg Config) (*Pool, error)

// Listener is a long-lived helper for LISTEN/NOTIFY.
type Listener struct { /* ... */ }

// Listen subscribes to a channel; received notifications are delivered via the
// returned chan. The Listener owns a single dedicated connection from the pool.
func (p *Pool) Listen(ctx context.Context, channel string) (*Listener, error)

// Bulk loader for COPY-based ingest.
type Copier struct { /* ... */ }

func (p *Pool) Copy(ctx context.Context, table string, columns []string, rows [][]any) (int64, error)
```

## Builder integration

```go
// app.Builder gains:
func (b *Builder) WithPgx(cfg pgx.Config) *Builder
```

`WithPgx` and `WithPostgres` are mutually exclusive (mirroring the existing `WithMariaDB`/`WithPostgres` rule). Document this.

## Defaults (mirror sqldb hardening)

- TLS required by default (reject sslmode=disable in non-dev).
- Sensible pool defaults (MaxConns 25, MaxConnLifetime 30m).
- Health check period 1m.

## Definition of done

- [x] Pool wrapper + Close. ✅ this PR (`infra/sqldb/pgx.Pool`)
- [x] LISTEN/NOTIFY helper. ✅ `Listen(ctx, channels...)` + `Notify(ctx, channel, payload)`
- [x] COPY helper. ✅ `Copy(ctx, table, columns, rows)`
- [x] TLS-required default in non-dev (`require`/`verify-ca`/`verify-full` only; `prefer`/`allow`/`disable` rejected; URL + key=value DSN forms supported).
- [x] Unit tests for sslmode parsing + production-env enforcement.
- [x] Integration tests behind `//go:build integration` covering Ping, COPY, and LISTEN/NOTIFY round trip via testcontainers postgres.
- [ ] Mutual-exclusion check vs `WithPostgres` in Builder (deferred — primitive ships first; Builder integration is a separate audit item).
- [ ] Recipe in `docs/ai/sqldb.md` (deferred to docs sweep).
