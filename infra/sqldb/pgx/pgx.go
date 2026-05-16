// Package pgx wraps jackc/pgx with the kit's lifecycle and TLS
// conventions. v2 made pgx the single supported Postgres driver — GORM
// and MySQL/MariaDB are gone. Use the pgxpool directly for queries, or
// reach for sqlc when typed query generation is preferred.
//
// Postgres features available natively through pgx:
//
//   - LISTEN/NOTIFY for low-latency in-cluster pub/sub.
//   - COPY for bulk-loading 100k+ rows in one round trip.
//   - Batched pipelines (multiple statements per network RTT).
//   - Custom binary type encoding for jsonb / arrays.
//
// TLS: Connect always rejects sslmode=disable, prefer, allow, and —
// since 2.0 (audit FR-079) — sslmode=require by default. The accepted
// modes are `verify-ca` and `verify-full`. `require` encrypts but
// does NOT verify the server's identity, so a network attacker with
// any certificate can MITM the connection; many cloud providers
// historically defaulted to it for "easy" TLS at the cost of
// authentication.
//
// Operators on a closed/internal network where verification is
// genuinely intractable can opt back into `require` via
// [Config.AllowSSLModeRequire] — the field name is verbose so a
// reviewer cannot miss it. Loose modes (`prefer`, `allow`) remain
// unconditionally rejected because they fall back to plaintext on a
// TLS handshake error.
package pgx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Config bundles the pgxpool tuning knobs the kit wants to be opinionated
// about. Anything not exposed here can be set on the underlying
// `pgxpool.Config` returned by [ConfigToPgxPool].
type Config struct {
	// DSN is the libpq-style connection string. The sslmode parameter is
	// inspected at Connect time and must be require/verify-ca/verify-full.
	DSN string

	// PasswordProvider, when set, is called before every new physical
	// connection is opened and its return value replaces the password parsed
	// from DSN. Use it for managed database credentials that rotate under a
	// stable host/user/database tuple, such as Vault-issued Postgres passwords
	// or cloud IAM auth tokens. Existing pooled connections keep their current
	// authentication until they are closed; call [Pool.Reset] after a rotation
	// event to force fresh connections.
	PasswordProvider func(context.Context) (string, error)

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

	// AllowSSLModeRequire opts back in to sslmode=require — TLS
	// without server identity verification. Pre-2.0 the kit accepted
	// `require` alongside `verify-ca` / `verify-full`; the audit
	// (FR-079) found that `require` admits MITM with arbitrary
	// certificates in many network environments, so the default is
	// now to reject it.
	//
	// Set this true ONLY when the network path between the service
	// and Postgres is under the operator's control (mesh / private
	// VPC peering / sidecar) AND there is a documented reason why
	// `verify-ca` cannot be used. The field name is deliberately
	// verbose so a reviewer cannot miss it.
	AllowSSLModeRequire bool

	// MaxConns caps the pool. Default: 25.
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

// LogValue implements slog.LogValuer to prevent accidental logging of
// database credentials embedded in DSNs.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("dsn_configured", c.DSN != ""),
		slog.Bool("password_provider_configured", c.PasswordProvider != nil),
		slog.Bool("allow_plaintext_loopback_for_tests", c.AllowPlaintextLoopbackForTests),
		slog.Bool("allow_sslmode_require", c.AllowSSLModeRequire),
		slog.Int("max_conns", int(c.MaxConns)),
		slog.Int("min_conns", int(c.MinConns)),
		slog.Duration("max_conn_lifetime", c.MaxConnLifetime),
		slog.Duration("max_conn_idle_time", c.MaxConnIdleTime),
		slog.Duration("health_check_period", c.HealthCheckPeriod),
	)
}

// Pool wraps *pgxpool.Pool. Use [Pool.Pool] to access the underlying
// pgxpool for advanced operations the kit doesn't expose directly.
type Pool struct {
	pool *pgxpool.Pool
	dsn  string
}

const (
	// MaxCopyRows caps one COPY helper call. Larger imports should chunk so
	// cancellation, retries, and resource usage stay predictable.
	MaxCopyRows = 100_000
	// MaxCopyColumns caps the COPY column list to a portable schema shape.
	MaxCopyColumns = 256
	// maxCopyIdentifierBytes matches PostgreSQL's default identifier length
	// before server-side truncation, avoiding surprising quoted identifiers.
	maxCopyIdentifierBytes = 63
)

// Connect parses cfg, enforces TLS, and constructs a pool. Validation
// errors include the offending knob so misconfigurations surface at
// boot rather than at first query.
//
// DSN parsing is delegated to pgxpool.ParseConfig — the authoritative
// parse the runtime will actually use. The previous hand-rolled
// extractors disagreed with pgxpool on `last-wins` for repeated keys
// in libpq form and on `?host=` query-string in URL form, so a
// crafted DSN could pass the kit's checks while pgxpool used different
// values. Parsing first, enforcing on the parsed config, eliminates
// that class of bug.
func Connect(ctx context.Context, cfg Config) (*Pool, error) {
	if ctx == nil {
		return nil, errors.New("pgx: Connect requires a non-nil context")
	}
	if cfg.DSN == "" {
		return nil, errors.New("pgx: DSN must not be empty")
	}

	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, redact.WrapError("pgx: parse DSN", err)
	}

	if cfg.AllowPlaintextLoopbackForTests {
		// Defence-in-depth: an operator may have flipped the bool by
		// accident or copy-pasted a test config. Refuse to honour the
		// opt-out unless EVERY host pgx might dial is a loopback — pgx
		// supports comma-separated multi-host DSNs (libpq HA syntax)
		// where additional hosts land on ConnConfig.Fallbacks; without
		// the loop a DSN like
		//   host=localhost,evil.example.com sslmode=disable
		// would pass the gate via the primary "localhost" while pgx
		// failed over to "evil.example.com" sending plaintext
		// credentials (audit finding N-6).
		hosts := []string{pcfg.ConnConfig.Host}
		for _, fb := range pcfg.ConnConfig.Fallbacks {
			hosts = append(hosts, fb.Host)
		}
		for _, h := range hosts {
			if err := requireLoopbackHost(h); err != nil {
				return nil, redact.WrapError("pgx: AllowPlaintextLoopbackForTests is set but DSN host is not loopback", err)
			}
		}
	} else {
		if err := requireTLSOnParsedConfig(pcfg, cfg.DSN, cfg.AllowSSLModeRequire); err != nil {
			return nil, err
		}
	}
	applyPoolDefaults(pcfg, cfg)
	applyPasswordProvider(pcfg, cfg.PasswordProvider)

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, redact.WrapError("pgx: connect", err)
	}
	return &Pool{pool: pool, dsn: cfg.DSN}, nil
}

// Pool returns the underlying pgxpool. Use sparingly — anything the
// kit wants to be opinionated about should grow a method on [Pool]
// instead.
func (p *Pool) Pool() *pgxpool.Pool {
	if p == nil {
		return nil
	}
	return p.pool
}

// Close releases all pool connections. Safe to call multiple times.
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	if p.pool != nil {
		p.pool.Close()
	}
	return nil
}

// Reset closes all currently-open pool connections. The pool remains usable and
// opens fresh connections on demand. Pair this with [Config.PasswordProvider]
// after a credential-rotation signal so old authenticated connections do not
// survive until their normal max lifetime.
func (p *Pool) Reset() error {
	if p == nil || p.pool == nil {
		return nil
	}
	p.pool.Reset()
	return nil
}

// Ping issues a no-op query to verify the pool is live. Use in
// readiness probes.
func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return errors.New("pgx: pool is closed")
	}
	return p.pool.Ping(ctx)
}

// Copy loads rows into table via Postgres COPY (one round-trip
// regardless of row count). Returns the number of rows copied.
//
// table accepts either a bare name ("users") or a schema-qualified
// name ("public.users"). The dot is split into a two-component
// pgx.Identifier so the wire identifier is "public"."users" rather
// than the single quoted literal "public.users", which Postgres
// would reject as an unknown table.
//
// Use this for bulk-load ingest paths (CSV import, batch backfill).
// For < 1000 rows, a parameterized INSERT is usually faster because
// it amortizes connection setup.
func (p *Pool) Copy(ctx context.Context, table string, columns []string, rows [][]any) (int64, error) {
	if p == nil || p.pool == nil {
		return 0, errors.New("pgx: pool is closed")
	}
	if table == "" {
		return 0, errors.New("pgx: COPY table must not be empty")
	}
	if len(columns) == 0 {
		return 0, errors.New("pgx: COPY columns must not be empty")
	}
	ident, err := parseCopyIdentifier(table)
	if err != nil {
		return 0, err
	}
	if err := validateCopyColumns(columns); err != nil {
		return 0, err
	}
	if err := validateCopyRows(columns, rows); err != nil {
		return 0, err
	}
	return p.pool.CopyFrom(ctx,
		ident,
		columns,
		pgx.CopyFromRows(rows),
	)
}

// parseCopyIdentifier splits a possibly schema-qualified table name into
// a pgx.Identifier whose Sanitize() emits "schema"."table". A single
// segment is returned as a one-element identifier. Each segment must be a
// portable PostgreSQL identifier so callers cannot accidentally rely on
// server-side truncation or quoted punctuation.
func parseCopyIdentifier(table string) (pgx.Identifier, error) {
	parts := strings.Split(table, ".")
	if len(parts) > 2 {
		return nil, fmt.Errorf("pgx: COPY table must be either \"name\" or \"schema.name\"")
	}
	for _, p := range parts {
		if !validCopyIdentifierSegment(p) {
			return nil, fmt.Errorf("pgx: COPY table contains an invalid identifier")
		}
	}
	return pgx.Identifier(parts), nil
}

func validateCopyColumns(columns []string) error {
	if len(columns) > MaxCopyColumns {
		return fmt.Errorf("pgx: COPY column count exceeds maximum")
	}
	for _, column := range columns {
		if !validCopyIdentifierSegment(column) {
			return fmt.Errorf("pgx: COPY column contains an invalid identifier")
		}
	}
	return nil
}

func validateCopyRows(columns []string, rows [][]any) error {
	if len(rows) > MaxCopyRows {
		return fmt.Errorf("pgx: COPY row count exceeds maximum")
	}
	for _, row := range rows {
		if len(row) != len(columns) {
			return fmt.Errorf("pgx: COPY row width must match column count")
		}
	}
	return nil
}

func validCopyIdentifierSegment(s string) bool {
	if s == "" || len(s) > maxCopyIdentifierBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
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
	if p == nil || p.pool == nil {
		return nil, nil, errors.New("pgx: pool is closed")
	}
	if ctx == nil {
		return nil, nil, errors.New("pgx: Listen requires a non-nil context")
	}
	if len(channels) == 0 {
		return nil, nil, errors.New("pgx: Listen requires at least one channel")
	}

	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, redact.WrapError("pgx: acquire LISTEN connection", err)
	}

	for _, ch := range channels {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
			conn.Release()
			return nil, nil, redact.WrapError("pgx: LISTEN failed", err)
		}
	}

	out := make(chan Notification, 16)
	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errCh)
		defer func() {
			// UNLISTEN before releasing the connection back to the
			// pool. Without this, the next pool acquirer reuses a
			// connection that is still subscribed to our channels and
			// receives stray notifications. The cleanup context
			// survives parent cancellation while retaining context
			// values for pgx hooks and observability wrappers.
			cleanupCtx, cancel := listenCleanupContext(ctx, 2*time.Second)
			_, _ = conn.Exec(cleanupCtx, "UNLISTEN *")
			cancel()
			conn.Release()
		}()
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

func listenCleanupContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// Notify sends a NOTIFY on channel with the given payload. Acquires
// one connection from the pool for the round trip.
func (p *Pool) Notify(ctx context.Context, channel, payload string) error {
	if p == nil || p.pool == nil {
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

func applyPasswordProvider(pcfg *pgxpool.Config, provider func(context.Context) (string, error)) {
	if pcfg == nil || provider == nil {
		return
	}
	previous := pcfg.BeforeConnect
	pcfg.BeforeConnect = func(ctx context.Context, connCfg *pgx.ConnConfig) error {
		if previous != nil {
			if err := previous(ctx, connCfg); err != nil {
				return err
			}
		}
		password, err := provider(ctx)
		if err != nil {
			return redact.WrapError("pgx: password provider", err)
		}
		if password == "" {
			return errors.New("pgx: password provider returned an empty password")
		}
		connCfg.Password = password
		return nil
	}
}

// requireLoopbackHost rejects any host that isn't a loopback address
// (127.0.0.0/8, ::1, or the literal "localhost"). Used to gate
// Config.AllowPlaintextLoopbackForTests: the opt-out is honoured ONLY
// when the network risk is mechanically zero.
//
// Operates on the host AFTER pgxpool.ParseConfig has resolved the DSN,
// so a crafted DSN cannot trick this check by exploiting first/last-wins
// disagreements between the kit and pgxpool. Bracket-wrapped IPv6
// literals (`[::1]`) are accepted by stripping the brackets before
// the IP parse — pgxpool emits them in some DSN forms.
func requireLoopbackHost(host string) error {
	if host == "" {
		return fmt.Errorf("pgx: DSN does not specify a host")
	}
	low := strings.ToLower(host)
	if low == "localhost" {
		return nil
	}
	// Strip brackets that pgxpool may have left around an IPv6 literal
	// — net.ParseIP rejects "[::1]" but accepts "::1".
	stripped := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	ip := net.ParseIP(stripped)
	if ip == nil {
		return fmt.Errorf("pgx: DSN host is not a loopback address")
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("pgx: DSN host is not a loopback address")
	}
	return nil
}

// requireTLSOnParsedConfig inspects the pgxpool-parsed config for TLS
// posture and rejects anything that admits a plaintext connection or
// (since 2.0) admits unverified TLS unless the caller explicitly
// opted in via Config.AllowSSLModeRequire.
//
// Detection rules — combine pgxpool's parsed config (for plaintext
// admission) with a DSN-string scan (for distinguishing require vs
// verify-ca, which pgx maps to identical TLSConfig fields):
//
//   - sslmode=disable: pgx sets ConnConfig.TLSConfig to nil. Reject.
//   - sslmode=verify-ca / sslmode=verify-full: accept.
//   - sslmode=require: reject unless allowRequire is true (FR-079).
//   - sslmode=prefer / sslmode=allow: pgx populates Fallbacks with a
//     plaintext (TLSConfig=nil) entry. Reject.
//
// Why parse the raw DSN here too: pgx maps both `require` and
// `verify-ca` to TLSConfig{InsecureSkipVerify:true, VerifyConnection:nil}
// — there is no field on the parsed config that distinguishes them.
// Without re-parsing the DSN, the kit cannot enforce a require-vs-
// verify-ca policy. The plaintext-fallback rejection still uses
// pcfg.ConnConfig to track what pgx will actually do at dial time
// (last-wins parsing is honoured by pgxpool itself).
func requireTLSOnParsedConfig(pcfg *pgxpool.Config, dsn string, allowRequire bool) error {
	cc := pcfg.ConnConfig
	if cc.TLSConfig == nil {
		return errors.New("pgx: DSN does not enable TLS (sslmode=disable or unset); set sslmode=verify-ca/verify-full (require opts in via Config.AllowSSLModeRequire)")
	}
	for _, fb := range cc.Fallbacks {
		if fb.TLSConfig == nil {
			return errors.New("pgx: DSN admits a plaintext fallback (sslmode=prefer/allow); use verify-ca/verify-full to enforce TLS unconditionally")
		}
	}
	mode := lastSSLMode(dsn)
	if mode == "require" && !allowRequire {
		return errors.New("pgx: sslmode=require admits MITM (no server identity verification); use sslmode=verify-ca or verify-full, or opt in with Config.AllowSSLModeRequire on a closed network")
	}
	return nil
}

// lastSSLMode returns the LAST sslmode value found in the DSN, or
// "" if none. Mirrors pgxpool's last-wins semantics for repeated
// keys. Handles both libpq URL form (?sslmode=...) and keyword form
// (sslmode=...).
//
// Quoted values (`sslmode='require'`) are unwrapped so the value the
// kit compares is the same value pgx will use. Wave 69 strengthened
// the scanner after a hostile-review finding flagged that the prior
// implementation could misclassify a quoted "require" as `'require'`
// (not matching the policy switch) and bypass FR-079.
func lastSSLMode(dsn string) string {
	const key = "sslmode="
	last := ""
	rest := dsn
	for {
		i := strings.Index(rest, key)
		if i < 0 {
			break
		}
		v := rest[i+len(key):]
		// Handle single-quoted values (libpq keyword form):
		//   sslmode='require'
		if len(v) > 0 && v[0] == '\'' {
			end := -1
			for j := 1; j < len(v); j++ {
				if v[j] == '\'' && v[j-1] != '\\' {
					end = j
					break
				}
			}
			if end < 0 {
				// Unterminated quote; stop scanning to avoid an
				// infinite loop or misleading value.
				return last
			}
			last = v[1:end]
			rest = v[end+1:]
			continue
		}
		// Stop at the first delimiter pgx recognises:
		// whitespace (keyword form) or `&` (URL form).
		end := len(v)
		for j := 0; j < len(v); j++ {
			c := v[j]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '&' {
				end = j
				break
			}
		}
		last = v[:end]
		rest = v[end:]
	}
	return last
}
