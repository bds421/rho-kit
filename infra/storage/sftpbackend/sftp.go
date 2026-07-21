package sftpbackend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

const (
	tracerName                     = "kit/storage/sftp"
	defaultPasswordProviderTimeout = 5 * time.Second
	// dialCooldown is how long after a failed dial subsequent connect
	// attempts fail fast, so N concurrent ops against a dead server do not
	// serialise into N full ssh.Dial timeouts (review-19).
	dialCooldown = 2 * time.Second
)

// Compile-time interface compliance check.
var _ storage.Storage = (*Backend)(nil)

// Client abstracts the sftp.Client methods used by Backend.
//
// Note: Create and Open return *sftp.File (a concrete type) because sftp.File
// combines io.ReadWriteCloser with io.Seeker and Stat — there is no standard
// interface covering all these. This means Put and Get cannot be unit-tested
// with a mock client; use integration tests with a real SFTP server instead.
type Client interface {
	Create(path string) (*sftp.File, error)
	Open(path string) (*sftp.File, error)
	Remove(path string) error
	Rename(oldname, newname string) error
	Lstat(path string) (os.FileInfo, error)
	Stat(path string) (os.FileInfo, error)
	MkdirAll(path string) error
	ReadDir(path string) ([]os.FileInfo, error)
	Close() error
}

// Backend implements [storage.Storage] using SFTP.
//
// Safe for concurrent use — connections are pooled, the per-Backend
// RWMutex guards the close path, and live cleanup goroutines are
// joined on Close via the internal WaitGroup. Once [Backend.Close] has
// been called the backend is terminally closed; all further operations
// return [storage.ErrBackendClosed].
// clientSession is one SSH/SFTP connection pair with a lease reference count.
// Ops call tryAcquire/release around use of client; reconnect retires the
// session and closes FDs only after refs drain (review-19).
type clientSession struct {
	client  Client
	sshConn io.Closer
	refs    atomic.Int64
	retire  atomic.Bool
	// closedOnce ensures Close on client/ssh runs at most once.
	closedOnce sync.Once
	// drained is closed when the session has been fully closed. Waiters
	// (reconnect drain / Backend.Close) select on it.
	drained chan struct{}
}

func newClientSession(client Client, sshConn io.Closer) *clientSession {
	return &clientSession{
		client:  client,
		sshConn: sshConn,
		drained: make(chan struct{}),
	}
}

// tryAcquire increments the lease count if the session is still live.
// Returns false when the session has been retired (reconnect replaced it).
func (s *clientSession) tryAcquire() bool {
	if s == nil {
		return false
	}
	if s.retire.Load() {
		return false
	}
	s.refs.Add(1)
	if s.retire.Load() {
		// Lost the race with markRetired; drop the speculative ref.
		s.release()
		return false
	}
	return true
}

func (s *clientSession) release() {
	if s == nil {
		return
	}
	if s.refs.Add(-1) != 0 {
		return
	}
	if s.retire.Load() {
		s.closeFDs()
	}
}

// markRetired prevents new leases and closes FDs once refs hit zero.
func (s *clientSession) markRetired() {
	if s == nil {
		return
	}
	s.retire.Store(true)
	if s.refs.Load() == 0 {
		s.closeFDs()
	}
}

func (s *clientSession) closeFDs() {
	if s == nil {
		return
	}
	s.closedOnce.Do(func() {
		if s.client != nil {
			_ = s.client.Close()
		}
		if s.sshConn != nil {
			_ = s.sshConn.Close()
		}
		close(s.drained)
	})
}

// inflight reports active leases (for tests and drain waits).
func (s *clientSession) inflight() int64 {
	if s == nil {
		return 0
	}
	return s.refs.Load()
}

// reconnectDrainGrace is how long replaceSession waits for in-flight leases
// before returning a drain error. The new session is still installed so new
// ops proceed; the old session keeps serving leased ops until they release.
const reconnectDrainGrace = 30 * time.Second

type Backend struct {
	cfg        Config
	instance   string
	validators []storage.Validator
	logger     *slog.Logger
	metrics    *Metrics

	mu sync.RWMutex
	// session is the current live client; nil when disconnected.
	session   *clientSession
	connected bool
	lazyConn  bool

	// closed is the terminal latch. Set by Close to fail every subsequent
	// operation closed rather than silently reconnecting.
	closed atomic.Bool

	// cleanupWg tracks pending drain/close work for retired sessions.
	// Close() waits for them to finish to prevent file descriptor leaks on shutdown.
	cleanupWg sync.WaitGroup

	// dialMu serialises reconnect attempts without holding b.mu across
	// ssh.Dial (review-19). dialCooldown gates rapid retries after failure.
	dialMu       sync.Mutex
	lastDialFail time.Time
	// dialingWait is non-nil while a dial is in flight; waiters park on it
	// under dialMu then re-check connected under b.mu.
	dialingWait chan struct{}
}

// Option configures an Backend.
type Option func(*Backend)

// WithInstance sets the Prometheus instance label. Defaults to "default".
func WithInstance(name string) Option {
	return func(b *Backend) {
		if err := storage.ValidateInstanceName(name); err != nil {
			panic("sftpbackend: WithInstance invalid instance name")
		}
		b.instance = name
	}
}

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	copied := storage.CloneValidators(validators...)
	return func(b *Backend) {
		b.validators = storage.AppendValidators(b.validators, copied...)
	}
}

// WithLazyConnect defers the SSH/SFTP connection until the first operation.
// By default, New connects eagerly and returns an error if the connection fails.
func WithLazyConnect() Option {
	return func(b *Backend) {
		b.lazyConn = true
	}
}

// WithLogger sets a structured logger. Defaults to slog.Default().
// Passing nil falls back to slog.Default() rather than creating a latent
// nil-deref panic on connect or health-failure paths.
func WithLogger(logger *slog.Logger) Option {
	return func(b *Backend) {
		if logger == nil {
			logger = slog.Default()
		}
		b.logger = logger
	}
}

// WithMetricsRegisterer sets the Prometheus registerer for SFTP
// metrics. If not set, prometheus.DefaultRegisterer is used. Replaces
// the v1 WithRegisterer spelling so it no longer collides with the
// metrics-level option of the same name and matches the kit-wide
// convention for component-level metric registerer options.
func WithMetricsRegisterer(reg prometheus.Registerer) Option {
	return func(b *Backend) {
		if reg == nil {
			b.metrics = NewMetrics()
			return
		}
		b.metrics = NewMetrics(WithRegisterer(reg))
	}
}

// New creates a new Backend. By default, it connects eagerly.
// Use WithLazyConnect to defer connection.
//
// New uses [context.Background] for the initial SSH/SFTP dial and any
// [Config.PasswordProvider] fetch. Production services that need a
// startup deadline should call [NewContext] with a bounded ctx instead.
func New(cfg Config, opts ...Option) (*Backend, error) {
	return NewContext(context.Background(), cfg, opts...)
}

// NewContext is the ctx-aware variant of [New]. The supplied ctx bounds
// the eager SSH dial (when [WithLazyConnect] is not set) and any
// PasswordProvider invocation during connect. Prefer this constructor
// over [New] when startup must observe a deadline or cancellation.
func NewContext(ctx context.Context, cfg Config, opts ...Option) (*Backend, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := cfg.Validate(""); err != nil {
		return nil, err
	}

	b := &Backend{
		cfg:      cfg,
		instance: "default",
		logger:   slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("sftpbackend: NewContext option must not be nil")
		}
		o(b)
	}
	if b.metrics == nil {
		b.metrics = defaultMetrics()
	}

	if !b.lazyConn {
		if err := b.connect(ctx); err != nil {
			return nil, storage.WrapSafe("sftpbackend: initial connect failed", err)
		}
	}

	return b, nil
}

// NewWithClient creates an Backend with a custom client, for testing.
func NewWithClient(client Client, cfg Config, opts ...Option) *Backend {
	if client == nil {
		panic("sftpbackend: NewWithClient requires a non-nil Client")
	}
	if cfg.RootPath == "" {
		cfg.RootPath = "/"
	}
	if !path.IsAbs(cfg.RootPath) {
		panic("sftpbackend: Config.RootPath must be absolute")
	}
	cfg.RootPath = path.Clean(cfg.RootPath)
	b := &Backend{
		cfg:       cfg,
		instance:  "default",
		logger:    slog.Default(),
		session:   newClientSession(client, nil),
		connected: true,
	}
	for _, o := range opts {
		if o == nil {
			panic("sftpbackend: NewWithClient option must not be nil")
		}
		o(b)
	}
	if b.metrics == nil {
		b.metrics = defaultMetrics()
	}
	return b
}

// connect establishes the SSH and SFTP connection. Caller must not hold b.mu.
// Network dial happens outside b.mu so Healthy/Get/Close are not blocked for
// the full sshConnectTimeout on every concurrent reconnect (review-19).
func (b *Backend) connect(ctx context.Context) error {
	// Fast path: already connected.
	b.mu.RLock()
	if b.closed.Load() {
		b.mu.RUnlock()
		return fmt.Errorf("sftpbackend: %w", storage.ErrBackendClosed)
	}
	if b.connected && b.session != nil && b.session.client != nil {
		b.mu.RUnlock()
		return nil
	}
	b.mu.RUnlock()

	// Singleflight + cooldown under dialMu (not b.mu).
	b.dialMu.Lock()
	if b.closed.Load() {
		b.dialMu.Unlock()
		return fmt.Errorf("sftpbackend: %w", storage.ErrBackendClosed)
	}
	// Re-check under dialMu after another dialer may have finished.
	b.mu.RLock()
	if b.connected && b.session != nil && b.session.client != nil {
		b.mu.RUnlock()
		b.dialMu.Unlock()
		return nil
	}
	b.mu.RUnlock()

	if !b.lastDialFail.IsZero() && time.Since(b.lastDialFail) < dialCooldown {
		b.dialMu.Unlock()
		return fmt.Errorf("sftpbackend: dial cooling down after recent failure")
	}
	if b.dialingWait != nil {
		wait := b.dialingWait
		b.dialMu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return redact.WrapError("sftpbackend", ctx.Err())
		}
		// Winner finished; re-enter for connected/error state.
		return b.connect(ctx)
	}
	wait := make(chan struct{})
	b.dialingWait = wait
	b.dialMu.Unlock()

	// Dial outside both locks.
	err := b.dialAndInstall(ctx)
	b.dialMu.Lock()
	close(wait)
	b.dialingWait = nil
	if err != nil {
		b.lastDialFail = time.Now()
	} else {
		b.lastDialFail = time.Time{}
	}
	b.dialMu.Unlock()
	return err
}

// dialAndInstall performs ssh.Dial + sftp.NewClient then installs the
// result under b.mu. Failures never leave partial state installed.
func (b *Backend) dialAndInstall(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return redact.WrapError("sftpbackend", err)
	}
	if b.closed.Load() {
		return fmt.Errorf("sftpbackend: %w", storage.ErrBackendClosed)
	}

	sshCfg, err := b.buildSSHConfig(ctx)
	if err != nil {
		return storage.WrapSafe("build SSH config failed", err)
	}

	addr := net.JoinHostPort(b.cfg.Host, fmt.Sprintf("%d", b.cfg.Port))
	conn, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
		return storage.WrapSafe("sftpbackend: SSH dial failed", err)
	}

	sftpClient, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
		return storage.WrapSafe("SFTP client setup failed", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed.Load() {
		_ = sftpClient.Close()
		_ = conn.Close()
		return fmt.Errorf("sftpbackend: %w", storage.ErrBackendClosed)
	}
	if b.connected && b.session != nil && b.session.client != nil {
		// Lost the race to another installer; discard ours.
		_ = sftpClient.Close()
		_ = conn.Close()
		return nil
	}

	newSess := newClientSession(sftpClient, conn)
	old := b.session
	b.session = newSess
	b.connected = true
	b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(1)
	b.logger.Info("SFTP connected", redact.String("host", b.cfg.Host), "port", b.cfg.Port)

	// Retire the previous session: close FDs only after in-flight leases drain
	// so Put/Get mid-transfer are not corrupted by SSH teardown (review-19).
	if old != nil {
		old.markRetired()
		b.cleanupWg.Add(1)
		go func(sess *clientSession) {
			defer b.cleanupWg.Done()
			timer := time.NewTimer(reconnectDrainGrace)
			defer timer.Stop()
			select {
			case <-sess.drained:
				return
			case <-timer.C:
				// Grace expired with leases still held. Do NOT force-close:
				// that would corrupt in-flight Put/Get. Wait for natural drain.
				if b.logger != nil {
					b.logger.Warn("SFTP reconnect drain grace elapsed; waiting for in-flight leases",
						redact.String("host", b.cfg.Host),
						"inflight", sess.inflight())
				}
				<-sess.drained
			}
		}(old)
	}
	return nil
}

func (b *Backend) buildSSHConfig(ctx context.Context) (*ssh.ClientConfig, error) {
	if b.cfg.Password == "" && b.cfg.KeyFile == "" && b.cfg.PasswordProvider == nil {
		return nil, fmt.Errorf("no SSH authentication method configured (need Password, PasswordProvider, or KeyFile)")
	}

	cfg := &ssh.ClientConfig{
		User:    b.cfg.User,
		Timeout: sshConnectTimeout,
	}

	if b.cfg.KnownHostsFile == "" {
		return nil, fmt.Errorf("SSH host key verification requires KnownHostsFile")
	}
	hostKeyCallback, err := knownhosts.New(b.cfg.KnownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts failed")
	}
	cfg.HostKeyCallback = hostKeyCallback

	password := b.cfg.Password
	if b.cfg.PasswordProvider != nil {
		providerCtx := ctx
		if providerCtx == nil {
			providerCtx = context.Background()
		}
		timeout := b.passwordProviderTimeout()
		if timeout > 0 {
			var cancel context.CancelFunc
			providerCtx, cancel = context.WithTimeout(providerCtx, timeout)
			defer cancel()
		}
		var err error
		password, err = b.cfg.PasswordProvider(providerCtx)
		if err != nil {
			return nil, storage.WrapSafe("load SFTP password failed", err)
		}
		if err := providerCtx.Err(); err != nil {
			return nil, storage.WrapSafe("load SFTP password failed", err)
		}
		if password == "" {
			return nil, fmt.Errorf("SFTP password provider returned an empty password")
		}
	}

	if password != "" {
		cfg.Auth = []ssh.AuthMethod{ssh.Password(password)}
	} else {
		key, err := os.ReadFile(b.cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("read SSH key file failed")
		}
		// Zero the in-memory file bytes after ParsePrivateKey extracts
		// what it needs; ssh.ParsePrivateKey copies the parsed material
		// into its own structs (audited against golang.org/x/crypto/ssh
		// in the workspace tree), so the original `key` slice is safe
		// to wipe before the signer is used. This shortens the window
		// during which the on-disk private-key plaintext sits on the
		// process heap (L123).
		defer func() {
			for i := range key {
				key[i] = 0
			}
		}()
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse SSH key failed")
		}
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	}

	return cfg, nil
}

func (b *Backend) passwordProviderTimeout() time.Duration {
	if b == nil || b.cfg.PasswordProviderTimeout == 0 {
		return defaultPasswordProviderTimeout
	}
	return b.cfg.PasswordProviderTimeout
}

// getClient returns a leased SFTP client and a release function. Callers must
// invoke release exactly once when the lease is no longer needed (after the
// op completes, or after the Get body ReadCloser is closed). After Close has
// been called, getClient returns [storage.ErrBackendClosed] rather than
// reconnecting — a closed backend is terminally closed.
func (b *Backend) getClient(ctx context.Context) (Client, func(), error) {
	releaseNop := func() {}
	if b.closed.Load() {
		return nil, releaseNop, fmt.Errorf("sftpbackend: %w", storage.ErrBackendClosed)
	}

	// Fast path: live session with a successful lease.
	if client, release, ok := b.tryLease(); ok {
		return client, release, nil
	}

	// connect() dials outside b.mu and installs a new session.
	if err := b.connect(ctx); err != nil {
		return nil, releaseNop, err
	}

	if client, release, ok := b.tryLease(); ok {
		return client, release, nil
	}
	// Close may have raced between connect success and this re-read.
	return nil, releaseNop, fmt.Errorf("sftpbackend: %w", storage.ErrBackendClosed)
}

// tryLease attempts to take a reference on the current session.
func (b *Backend) tryLease() (Client, func(), bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed.Load() || !b.connected || b.session == nil {
		return nil, nil, false
	}
	sess := b.session
	if !sess.tryAcquire() {
		return nil, nil, false
	}
	return sess.client, sess.release, true
}

// replaceSessionForTest retires the current session and installs client as the
// live session. Used by unit tests to exercise lease drain without a real dial.
func (b *Backend) replaceSessionForTest(client Client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	newSess := newClientSession(client, nil)
	old := b.session
	b.session = newSess
	b.connected = true
	if old != nil {
		old.markRetired()
		b.cleanupWg.Add(1)
		go func(sess *clientSession) {
			defer b.cleanupWg.Done()
			<-sess.drained
		}(old)
	}
}

// remotePath joins the root path with the given key.
func (b *Backend) remotePath(key string) string {
	return path.Join(b.cfg.RootPath, key)
}

func (b *Backend) ensureRemotePathUnderRoot(remotePath string) error {
	rel, err := relPath(b.cfg.RootPath, remotePath)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	if strings.HasPrefix(rel, "../") || rel == ".." || path.IsAbs(rel) {
		return fmt.Errorf("remote path escapes root")
	}
	return nil
}

func (b *Backend) rejectSymlinkAncestors(client Client, remotePath string) error {
	if err := b.ensureRemotePathUnderRoot(remotePath); err != nil {
		return err
	}
	rel, err := relPath(b.cfg.RootPath, remotePath)
	if err != nil {
		return err
	}

	cur := path.Clean(b.cfg.RootPath)
	if err := rejectSFTPSymlinkAt(client, cur); err != nil {
		if isNotExist(err) {
			return nil
		}
		return err
	}
	if rel == "." {
		return nil
	}

	for _, part := range strings.Split(path.Dir(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		cur = path.Join(cur, part)
		if err := rejectSFTPSymlinkAt(client, cur); err != nil {
			if isNotExist(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

// rejectSymlinkPath rejects remotePath if it or any ancestor under the root is a
// symlink, defending against a hostile/confused SFTP server redirecting reads,
// writes or deletes outside the configured root.
//
// The check is best-effort, not atomic: it uses Lstat, but the follow-up
// operation (Open/Stat/Remove/Rename) follows symlinks. A server that swaps a
// regular file for a symlink between the Lstat here and the follow-up op (a
// TOCTOU race) can still redirect that single operation. pkg/sftp exposes no
// O_NOFOLLOW open, so this residual race cannot be closed at the protocol level;
// the symlink rejection narrows, but does not eliminate, the hostile-server
// window.
func (b *Backend) rejectSymlinkPath(client Client, remotePath string) error {
	if err := b.rejectSymlinkAncestors(client, remotePath); err != nil {
		return err
	}
	if err := rejectSFTPSymlinkAt(client, remotePath); err != nil {
		if isNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func rejectSFTPSymlinkAt(client Client, remotePath string) error {
	info, err := client.Lstat(remotePath)
	if err != nil {
		if isNotExist(err) {
			return err
		}
		return sftpRemoteError("inspect remote path", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("path component is a symlink")
	}
	return nil
}

func sftpRemoteError(op string, err error) error {
	if errors.Is(err, storage.ErrValidation) {
		return redact.WrapError("sftpbackend", err)
	}
	// Preserve cancellation/deadline identity so callers can distinguish
	// a cancelled op from a generic remote failure (retry/backoff).
	// Other causes stay redacted to avoid leaking remote topology.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("sftpbackend: %s failed: %w", op, err) // kit:ok-fmt-errorf-wrap
	}
	return fmt.Errorf("sftpbackend: %s failed", op)
}

// translateSFTPCapacity inspects a write error from the SFTP server and
// returns [storage.ErrInsufficientCapacity] when the remote filesystem
// signaled ENOSPC. The kit's underlying sftp client surfaces the server's
// SSH_FX_FAILURE response with the originating syscall.Errno on Unix
// peers; some servers also return a sftp.StatusCode == "Failure" with an
// error message containing "no space".
func translateSFTPCapacity(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENOSPC) {
		return fmt.Errorf("sftpbackend: remote disk full: %w (cause: %w)", storage.ErrInsufficientCapacity, err) // kit:ok-fmt-errorf-wrap
	}
	if msg := err.Error(); strings.Contains(strings.ToLower(msg), "no space left") {
		return fmt.Errorf("sftpbackend: remote disk full: %w (cause: %w)", storage.ErrInsufficientCapacity, err) // kit:ok-fmt-errorf-wrap
	}
	return nil
}

// Put writes content from r to the remote path. Validators run before upload.
//
// ctx governs validation, connection setup, and the trace span, but mid-transfer
// cancellation is not honored: once the body io.Copy to the remote temp file is
// underway, a cancelled or deadline-exceeded ctx does not abort the streaming
// transfer — it runs until the underlying SSH connection's own timeouts fire.
// This is inherent to pkg/sftp, which does not thread a context through its
// blocking I/O. Bound transfer time via the SSH connection timeouts rather than
// relying on ctx to interrupt an in-flight upload.
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Put")
	defer span.End()
	span.SetAttributes(attribute.Int("storage.key_len", len(key)))

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(ctx, r, &meta, b.validators)
	if err != nil {
		span.SetStatus(codes.Error, storage.SpanErrorDescription(err))
		return err
	}
	if len(b.validators) > 0 {
		defer func() { _ = storage.CloseValidatedReader(validated) }()
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		span.SetStatus(codes.Error, storage.SpanErrorDescription(err))
		return err
	}

	client, release, err := b.getClient(ctx)
	if err != nil {
		opErr := storage.WrapSafe("sftpbackend: put connection failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	defer release()

	remotePath := b.remotePath(key)
	suffix, err := randomSuffix()
	if err != nil {
		span.SetStatus(codes.Error, storage.SpanErrorDescription(err))
		return err
	}
	tmpPath := remotePath + ".tmp-" + suffix

	// Ensure parent directory exists.
	dir := path.Dir(remotePath)
	if err := b.rejectSymlinkAncestors(client, remotePath); err != nil {
		opErr := redact.WrapError("sftpbackend: unsafe parent", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	if err := client.MkdirAll(dir); err != nil {
		opErr := sftpRemoteError("mkdir", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	if err := b.rejectSymlinkAncestors(client, remotePath); err != nil {
		opErr := redact.WrapError("sftpbackend: unsafe parent", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		opErr := redact.WrapError("sftpbackend: unsafe target", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	start := now()

	// Write to a temp file and rename on success for atomic Put.
	// This prevents readers from seeing partially-written content.
	f, err := client.Create(tmpPath)
	if err != nil {
		b.metrics.observeOp(b.instance, "put", start, err)
		opErr := sftpRemoteError("create", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	if _, err := io.Copy(f, validated); err != nil {
		_ = f.Close()
		_ = client.Remove(tmpPath)
		b.metrics.observeOp(b.instance, "put", start, err)
		if capacity := translateSFTPCapacity(err); capacity != nil {
			span.SetStatus(codes.Error, storage.SpanErrorDescription(capacity))
			return capacity
		}
		opErr := sftpRemoteError("write", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	if err := f.Close(); err != nil {
		_ = client.Remove(tmpPath)
		b.metrics.observeOp(b.instance, "put", start, err)
		if capacity := translateSFTPCapacity(err); capacity != nil {
			span.SetStatus(codes.Error, storage.SpanErrorDescription(capacity))
			return capacity
		}
		opErr := sftpRemoteError("close", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		_ = client.Remove(tmpPath)
		b.metrics.observeOp(b.instance, "put", start, err)
		opErr := redact.WrapError("sftpbackend: unsafe target", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	// Spec-compliant SFTP servers refuse Rename when the target already
	// exists (SSH_FX_FILE_ALREADY_EXISTS). Put documents overwrite
	// semantics, so remove any existing object first (see commitPutRename).
	if err := commitPutRename(client, tmpPath, remotePath); err != nil {
		_ = client.Remove(tmpPath)
		b.metrics.observeOp(b.instance, "put", start, err)
		opErr := sftpRemoteError("rename", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	b.metrics.observeOp(b.instance, "put", start, nil)
	return nil
}

// commitPutRename atomically replaces remotePath with tmpPath for Put
// overwrite semantics. Spec-compliant SFTP servers reject Rename when
// the destination exists, so we Remove first then Rename.
func commitPutRename(client Client, tmpPath, remotePath string) error {
	if err := client.Remove(remotePath); err != nil && !isNotExist(err) {
		// Best-effort: continue to Rename; some servers report remove
		// failure for non-files. Rename surfaces a hard error if the
		// target still blocks the replace.
		_ = err
	}
	return client.Rename(tmpPath, remotePath)
}

// Get retrieves file content from the remote path. Caller must close the returned ReadCloser.
//
// ctx governs validation, connection setup, the symlink check, and the trace
// span, but reads from the returned ReadCloser are not bound to ctx: cancelling
// ctx after Get returns does not interrupt an in-flight Read on the body, which
// streams until the underlying SSH connection's own timeouts fire. This is
// inherent to pkg/sftp, which does not thread a context through its blocking I/O.
func (b *Backend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Get")
	defer span.End()
	span.SetAttributes(attribute.Int("storage.key_len", len(key)))

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	client, release, err := b.getClient(ctx)
	if err != nil {
		opErr := storage.WrapSafe("sftpbackend: get connection failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}
	// Lease is held until the returned body is closed (or on error paths below).

	remotePath := b.remotePath(key)
	start := now()
	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		release()
		// Record the operation so symlink-rejection events stay visible in
		// storage_sftp_* and per-operation counts match the happy/remote-error
		// paths. sftpMetricErr keeps an expected not-found out of
		// operation_errors_total, consistent with the Open path below.
		b.metrics.observeOp(b.instance, "get", start, sftpMetricErr(err))
		if isNotExist(err) {
			opErr := fmt.Errorf("sftpbackend: get: %w", storage.ErrObjectNotFound)
			span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
			return nil, storage.ObjectMeta{}, opErr
		}
		opErr := redact.WrapError("sftpbackend: unsafe path", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}

	f, err := client.Open(remotePath)
	// Not-found is expected (cache miss / CAS probe / sweep) and must
	// not inflate operation_errors_total. Route through sftpMetricErr
	// so the dashboard contract matches S3 / Azure / GCS.
	b.metrics.observeOp(b.instance, "get", start, sftpMetricErr(err))

	if err != nil {
		release()
		if isNotExist(err) {
			opErr := fmt.Errorf("sftpbackend: get: %w", storage.ErrObjectNotFound)
			span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
			return nil, storage.ObjectMeta{}, opErr
		}
		opErr := sftpRemoteError("get", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}

	meta := storage.ObjectMeta{}
	if info, statErr := f.Stat(); statErr == nil {
		meta.Size = info.Size()
	}

	return &leasedReadCloser{ReadCloser: f, release: release}, meta, nil
}

// leasedReadCloser holds a client lease until the body is closed so reconnect
// cannot tear down the SSH session under an in-flight download (review-19).
type leasedReadCloser struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (l *leasedReadCloser) Close() error {
	var err error
	if l.ReadCloser != nil {
		err = l.ReadCloser.Close()
	}
	l.once.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
	return err
}

// Delete removes a file at the remote path. Returns nil if the file does not exist.
func (b *Backend) Delete(ctx context.Context, key string) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Delete")
	defer span.End()
	span.SetAttributes(attribute.Int("storage.key_len", len(key)))

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	client, release, err := b.getClient(ctx)
	if err != nil {
		opErr := storage.WrapSafe("sftpbackend: delete connection failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	defer release()
	remotePath := b.remotePath(key)
	start := now()
	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		// Record the operation so symlink-rejection events stay visible in
		// storage_sftp_* and per-operation counts match the remote Remove
		// path below. sftpMetricErr keeps an expected not-found (idempotent
		// delete) out of operation_errors_total.
		b.metrics.observeOp(b.instance, "delete", start, sftpMetricErr(err))
		if isNotExist(err) {
			return nil
		}
		opErr := redact.WrapError("sftpbackend: unsafe path", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	err = client.Remove(remotePath)
	b.metrics.observeOp(b.instance, "delete", start, sftpMetricErr(err))

	if err != nil {
		if isNotExist(err) {
			return nil
		}
		opErr := sftpRemoteError("delete", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	return nil
}

// Exists reports whether the key exists on the remote server.
func (b *Backend) Exists(ctx context.Context, key string) (bool, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Exists")
	defer span.End()
	span.SetAttributes(attribute.Int("storage.key_len", len(key)))

	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	client, release, err := b.getClient(ctx)
	if err != nil {
		opErr := storage.WrapSafe("sftpbackend: exists connection failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return false, opErr
	}
	defer release()
	remotePath := b.remotePath(key)
	start := now()
	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		// Record the operation so symlink-rejection events stay visible in
		// storage_sftp_* and per-operation counts match the remote Stat path
		// below. sftpMetricErr keeps an expected not-found out of
		// operation_errors_total.
		b.metrics.observeOp(b.instance, "exists", start, sftpMetricErr(err))
		if isNotExist(err) {
			return false, nil
		}
		opErr := redact.WrapError("sftpbackend: unsafe path", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return false, opErr
	}

	_, err = client.Stat(remotePath)
	b.metrics.observeOp(b.instance, "exists", start, sftpMetricErr(err))

	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		opErr := sftpRemoteError("exists", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return false, opErr
	}
	return true, nil
}

// Healthy reports whether the SFTP connection is alive.
// If the probe fails, the connection is marked as disconnected so the next
// operation triggers a reconnection via getClient → connect.
//
// When the backend was built with [WithLazyConnect] and has not yet
// connected, Healthy attempts a best-effort connect (bounded by the
// health probe timeout) so wiring the backend into a critical readiness
// check cannot permanently dead-lock readiness waiting for the first
// storage operation that never arrives.
//
// Concurrent use with Close is safe at the Go level (no data race) because
// the client reference is copied under RLock. However, calling Stat on a
// closed sftp.Client returns an error (which correctly makes Healthy return
// false). Do not rely on Healthy returning meaningful results after Close.
func (b *Backend) Healthy() bool {
	if b == nil {
		return false
	}
	if b.closed.Load() {
		return false
	}
	b.mu.RLock()
	connected := b.connected && b.session != nil && b.session.client != nil
	var client Client
	if b.session != nil {
		client = b.session.client
	}
	lazy := b.lazyConn
	b.mu.RUnlock()
	if !connected {
		if !lazy {
			return false
		}
		// Best-effort lazy connect so CriticalHealthCheck + WithLazyConnect
		// can become ready without an intervening storage operation.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := b.connect(ctx)
		cancel()
		if err != nil || b.closed.Load() {
			return false
		}
		b.mu.RLock()
		if !b.connected || b.session == nil || b.session.client == nil {
			b.mu.RUnlock()
			return false
		}
		client = b.session.client
		b.mu.RUnlock()
	}

	// Stat the root path as a lightweight health probe.
	// Performed outside the lock to avoid blocking concurrent operations
	// if the SFTP server is slow to respond. Bound the probe so a hung
	// TCP session cannot stall readiness scrapes indefinitely (review-19).
	const healthStatTimeout = 5 * time.Second
	type statResult struct{ err error }
	ch := make(chan statResult, 1)
	go func() {
		_, err := client.Stat(b.cfg.RootPath)
		ch <- statResult{err: err}
	}()
	var err error
	select {
	case res := <-ch:
		err = res.err
	case <-time.After(healthStatTimeout):
		err = fmt.Errorf("sftpbackend: health probe timed out after %s", healthStatTimeout)
	}
	if err != nil {
		// Mark as disconnected so the next getClient call reconnects.
		// Re-check that the session still owns the client we probed — if
		// connect() ran concurrently and replaced it, we must not overwrite
		// the fresh connection state.
		b.mu.Lock()
		if b.session != nil && b.session.client == client {
			b.connected = false
			// Retire the unhealthy session so its FDs close after leases drain.
			old := b.session
			b.session = nil
			if old != nil {
				old.markRetired()
			}
			if b.metrics != nil {
				b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
			}
			if b.logger != nil {
				b.logger.Warn("SFTP health probe failed, marking disconnected",
					redact.String("host", b.cfg.Host), redact.Error(err))
			}
		}
		b.mu.Unlock()
		return false
	}
	return true
}

// Close closes the SFTP and SSH connections and waits for any pending
// lease drains from previous reconnections to finish. Close is the
// terminal state — subsequent operations return [storage.ErrBackendClosed]
// rather than silently reconnecting. Close is idempotent; calling it on an
// already-closed backend is a no-op.
//
// In-flight leases are retired; their FDs close when the last holder
// releases. Close waits on cleanupWg for those drains so file descriptors
// are not leaked on shutdown.
func (b *Backend) Close() error {
	if b == nil {
		return nil
	}
	if !b.closed.CompareAndSwap(false, true) {
		// Already closed — return nil and skip joining cleanupWg again.
		return nil
	}
	b.mu.Lock()

	sess := b.session
	b.session = nil
	b.connected = false
	if b.metrics != nil {
		b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
	}
	b.mu.Unlock()

	if sess != nil {
		// Force retirement; if no leases remain this closes FDs immediately.
		// With outstanding leases, closeFDs runs on the last release.
		sess.markRetired()
		// Also force-close on shutdown so a leaked Get body cannot hang Close
		// forever — markRetired already closed if refs==0; if not, wait briefly
		// then force.
		select {
		case <-sess.drained:
		case <-time.After(reconnectDrainGrace):
			sess.closeFDs()
		}
	}

	// Wait for pending reconnect drain goroutines outside the lock.
	b.cleanupWg.Wait()

	return nil
}

// isNotExist checks if an error represents a "file not found" condition.
func isNotExist(err error) bool {
	if os.IsNotExist(err) {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	// pkg/sftp returns *sftp.StatusError for SSH_FX_NO_SUCH_FILE.
	var statusErr *sftp.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Code == ssh_FX_NO_SUCH_FILE
	}
	return false
}

func sftpMetricErr(err error) error {
	if isNotExist(err) {
		return nil
	}
	return err
}

// ssh_FX_NO_SUCH_FILE is the SSH FXP status code for "no such file".
const ssh_FX_NO_SUCH_FILE = 2

// sshConnectTimeout prevents indefinite hangs when the SFTP host is
// unreachable (e.g. a firewall silently drops packets).
const sshConnectTimeout = 10 * time.Second

// randomSuffix returns a crypto-random hex string for unique temp file names.
// Using crypto/rand avoids collisions that time.Now().UnixNano() can have
// when concurrent goroutines call Put on the same key.
func randomSuffix() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", storage.WrapSafe("sftpbackend: random temp suffix failed", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
