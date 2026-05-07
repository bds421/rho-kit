// Package pgx wraps jackc/pgx with the kit's lifecycle and TLS
// conventions. Use it when a service needs Postgres features that
// `database/sql` (the default infra/sqldb path) cannot expose:
//
//   - LISTEN/NOTIFY for low-latency in-cluster pub/sub.
//   - COPY for bulk-loading 100k+ rows in one round trip.
//   - Batched pipelines (multiple statements per network RTT).
//   - Custom binary type encoding for jsonb / arrays.
//
// For ordinary CRUD against Postgres, prefer infra/sqldb/gormdb/gormpostgres.
// pgx and the gorm driver are mutually exclusive in app.Builder — pick one
// per service.
//
// TLS: Connect always rejects sslmode=disable. Pass an explicit
// sslmode in the DSN — `require`, `verify-ca`, or `verify-full`.
// Loose modes (`prefer`, `allow`) are rejected too because they fall
// back to plaintext on a TLS handshake error. There is no KIT_ENV
// escape hatch — production-safe defaults are unconditional.
package pgx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config bundles the pgxpool tuning knobs the kit wants to be opinionated
// about. Anything not exposed here can be set on the underlying
// `pgxpool.Config` returned by [ConfigToPgxPool].
type Config struct {
	// DSN is the libpq-style connection string. The sslmode parameter is
	// inspected at Connect time and must be require/verify-ca/verify-full.
	DSN string

	// AllowPlaintextLoopbackForTests opts out of the unconditional
	// sslmode check, but ONLY when the DSN's host resolves to a
	// loopback address (127.0.0.0/8 or ::1) — the connection literally
	// cannot leave the host. The loopback gate makes this safe to
	// expose on Config without a build tag: an operator who copy-pastes
	// the integration_test.go pattern into production gets an error
	// at Connect time the moment the DSN points at a non-loopback
	// host, instead of silently sending plaintext credentials over
	// the wire.
	//
	// Tests against testcontainers / embedded postgres land squarely
	// in the loopback case. Production deployments must leave this
	// false; the field's name is deliberately verbose so a code
	// reviewer cannot miss it.
	AllowPlaintextLoopbackForTests bool

	// MaxConns caps the pool. Default: 25 (mirrors gormpostgres).
	MaxConns int32
	// MinConns floor — connections kept warm. Default: 2.
	MinConns int32
	// MaxConnLifetime caps how long a single connection lives. Default: 30m.
	MaxConnLifetime time.Duration
	// MaxConnIdleTime caps idle-before-close. Default: 10m.
	MaxConnIdleTime time.Duration
	// HealthCheckPeriod is how often pgx pings idle conns. Default: 1m.
	HealthCheckPeriod time.Duration
}

// Pool wraps *pgxpool.Pool. Use [Pool.Pool] to access the underlying
// pgxpool for advanced operations the kit doesn't expose directly.
type Pool struct {
	pool *pgxpool.Pool
	dsn  string
}

// Connect parses cfg, enforces TLS, and constructs a pool. Validation
// errors include the offending knob so misconfigurations surface at
// boot rather than at first query.
func Connect(ctx context.Context, cfg Config) (*Pool, error) {
	if cfg.DSN == "" {
		return nil, errors.New("pgx: DSN must not be empty")
	}
	if cfg.AllowPlaintextLoopbackForTests {
		// Defense-in-depth: an operator may have flipped the bool by
		// accident or copy-pasted a test config. Refuse to honour the
		// opt-out unless the DSN points at a loopback host — at that
		// point the network risk is mechanically zero.
		if err := requireLoopbackDSN(cfg.DSN); err != nil {
			return nil, fmt.Errorf("pgx: AllowPlaintextLoopbackForTests is set but DSN is not loopback: %w", err)
		}
	} else {
		if err := requireTLS(cfg.DSN); err != nil {
			return nil, err
		}
	}

	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pgx: parse DSN: %w", err)
	}
	applyPoolDefaults(pcfg, cfg)

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("pgx: connect: %w", err)
	}
	return &Pool{pool: pool, dsn: cfg.DSN}, nil
}

// Pool returns the underlying pgxpool. Use sparingly — anything the
// kit wants to be opinionated about should grow a method on [Pool]
// instead.
func (p *Pool) Pool() *pgxpool.Pool { return p.pool }

// Close releases all pool connections. Safe to call multiple times.
func (p *Pool) Close() error {
	if p.pool != nil {
		p.pool.Close()
	}
	return nil
}

// Ping issues a no-op query to verify the pool is live. Use in
// readiness probes.
func (p *Pool) Ping(ctx context.Context) error {
	if p.pool == nil {
		return errors.New("pgx: pool is closed")
	}
	return p.pool.Ping(ctx)
}

// Copy loads rows into table via Postgres COPY (one round-trip
// regardless of row count). Returns the number of rows copied.
//
// Use this for bulk-load ingest paths (CSV import, batch backfill).
// For < 1000 rows, a parameterized INSERT is usually faster because
// it amortizes connection setup.
func (p *Pool) Copy(ctx context.Context, table string, columns []string, rows [][]any) (int64, error) {
	if p.pool == nil {
		return 0, errors.New("pgx: pool is closed")
	}
	if table == "" {
		return 0, errors.New("pgx: COPY table must not be empty")
	}
	if len(columns) == 0 {
		return 0, errors.New("pgx: COPY columns must not be empty")
	}
	return p.pool.CopyFrom(ctx,
		pgx.Identifier{table},
		columns,
		pgx.CopyFromRows(rows),
	)
}

// Notification is the kit-stable shape of a LISTEN/NOTIFY delivery.
type Notification struct {
	Channel string
	Payload string
}

// Listen subscribes to one or more Postgres NOTIFY channels. The
// returned chan yields every notification received until ctx cancels
// or the connection drops; chan close signals the listener has
// exited.
//
// One pgx connection is pinned to the listener for as long as it
// runs — size [Config.MaxConns] accordingly.
//
// On connection drop, the listener exits with the error returned via
// the second result channel; callers that need transparent
// reconnection should wrap Listen in a backoff loop.
func (p *Pool) Listen(ctx context.Context, channels ...string) (<-chan Notification, <-chan error, error) {
	if p.pool == nil {
		return nil, nil, errors.New("pgx: pool is closed")
	}
	if len(channels) == 0 {
		return nil, nil, errors.New("pgx: Listen requires at least one channel")
	}

	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("pgx: acquire LISTEN connection: %w", err)
	}

	for _, ch := range channels {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
			conn.Release()
			return nil, nil, fmt.Errorf("pgx: LISTEN %q: %w", ch, err)
		}
	}

	out := make(chan Notification, 16)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		defer conn.Release()
		for {
			n, waitErr := conn.Conn().WaitForNotification(ctx)
			if waitErr != nil {
				errCh <- waitErr
				return
			}
			select {
			case out <- Notification{Channel: n.Channel, Payload: n.Payload}:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
		}
	}()
	return out, errCh, nil
}

// Notify sends a NOTIFY on channel with the given payload. Acquires
// one connection from the pool for the round trip.
func (p *Pool) Notify(ctx context.Context, channel, payload string) error {
	if p.pool == nil {
		return errors.New("pgx: pool is closed")
	}
	if channel == "" {
		return errors.New("pgx: Notify channel must not be empty")
	}
	// Use SELECT pg_notify so we can pass the payload as a parameter.
	_, err := p.pool.Exec(ctx, "SELECT pg_notify($1, $2)", channel, payload)
	return err
}

func applyPoolDefaults(pcfg *pgxpool.Config, cfg Config) {
	if cfg.MaxConns <= 0 {
		pcfg.MaxConns = 25
	} else {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns <= 0 {
		pcfg.MinConns = 2
	} else {
		pcfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime <= 0 {
		pcfg.MaxConnLifetime = 30 * time.Minute
	} else {
		pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime <= 0 {
		pcfg.MaxConnIdleTime = 10 * time.Minute
	} else {
		pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.HealthCheckPeriod <= 0 {
		pcfg.HealthCheckPeriod = time.Minute
	} else {
		pcfg.HealthCheckPeriod = cfg.HealthCheckPeriod
	}
}

// requireLoopbackDSN parses the DSN's host and rejects anything that
// isn't a loopback address (127.0.0.0/8, ::1, or the literal string
// "localhost"). This is the gate behind Config.AllowPlaintextLoopbackForTests:
// the opt-out is honoured ONLY when the network risk is mechanically
// zero. An operator who flips the bool but points the DSN at a real
// host gets a hard error at Connect time instead of silently sending
// plaintext credentials over the wire.
func requireLoopbackDSN(dsn string) error {
	host := extractDSNHost(dsn)
	if host == "" {
		return fmt.Errorf("DSN does not specify a host")
	}
	low := strings.ToLower(host)
	if low == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("DSN host %q is not a loopback address", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("DSN host %q is not a loopback address", host)
	}
	return nil
}

// extractDSNHost finds the host in either URL form or libpq key=value
// form. Returns "" when absent.
func extractDSNHost(dsn string) string {
	// URL form: postgres://user:pw@host:port/db?... — read between
	// the @ and the first / or ?.
	if u, err := url.Parse(dsn); err == nil && u.Host != "" {
		return u.Hostname()
	}
	// Key=value form: host=... port=... ...
	for _, tok := range strings.Fields(dsn) {
		if eq := strings.Index(tok, "="); eq > 0 && strings.EqualFold(tok[:eq], "host") {
			return tok[eq+1:]
		}
	}
	return ""
}

// requireTLS inspects the DSN's sslmode parameter and rejects unsafe
// values unconditionally — production-safe TLS settings are the kit's
// only mode. Mirrors infra/sqldb's IsTLSEnabled tightening.
func requireTLS(dsn string) error {
	mode := extractSSLMode(dsn)
	switch strings.ToLower(mode) {
	case "require", "verify-ca", "verify-full":
		return nil
	case "":
		return fmt.Errorf("pgx: DSN must set sslmode (require/verify-ca/verify-full)")
	case "allow", "prefer", "disable":
		return fmt.Errorf("pgx: sslmode=%q falls back to plaintext on TLS handshake error; use require/verify-ca/verify-full", mode)
	default:
		return fmt.Errorf("pgx: sslmode=%q is unrecognized", mode)
	}
}

// extractSSLMode finds sslmode= in either URL form or libpq key=value
// form. Returns "" when absent.
func extractSSLMode(dsn string) string {
	// URL form: postgres://user:pw@host/db?sslmode=require
	if i := strings.Index(dsn, "?"); i >= 0 {
		q := dsn[i+1:]
		for _, kv := range strings.Split(q, "&") {
			if eq := strings.Index(kv, "="); eq > 0 && strings.EqualFold(kv[:eq], "sslmode") {
				return kv[eq+1:]
			}
		}
	}
	// Key=value form: host=... sslmode=require ...
	for _, tok := range strings.Fields(dsn) {
		if eq := strings.Index(tok, "="); eq > 0 && strings.EqualFold(tok[:eq], "sslmode") {
			return tok[eq+1:]
		}
	}
	return ""
}
