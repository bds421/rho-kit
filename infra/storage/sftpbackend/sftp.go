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
type Backend struct {
	cfg        Config
	instance   string
	validators []storage.Validator
	logger     *slog.Logger
	metrics    *Metrics

	mu        sync.RWMutex
	client    Client
	sshConn   io.Closer
	connected bool
	lazyConn  bool

	// cleanupGen is the latest cleanup generation. Cleanup goroutines hold
	// the generation they were spawned at; before sleeping for the grace
	// period they re-check whether a newer cleanup has run, and if so close
	// the FD pair immediately. Prevents FD accumulation when the SFTP server
	// flaps every few seconds (each old cleanup holding 2 FDs for 5s).
	cleanupGen atomic.Uint64

	// cleanupWg tracks pending cleanup goroutines that close replaced connections.
	// Close() waits for them to finish to prevent file descriptor leaks on shutdown.
	cleanupWg sync.WaitGroup
}

// Option configures an Backend.
type Option func(*Backend)

// WithInstance sets the Prometheus instance label. Defaults to "default".
func WithInstance(name string) Option {
	return func(b *Backend) {
		if err := storage.ValidateInstanceName(name); err != nil {
			panic("sftpbackend: invalid instance name")
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

// WithRegisterer sets the Prometheus registerer for SFTP metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(b *Backend) {
		b.metrics = NewMetrics(reg)
	}
}

// New creates a new Backend. By default, it connects eagerly.
// Use WithLazyConnect to defer connection.
func New(cfg Config, opts ...Option) (*Backend, error) {
	if err := cfg.Validate(""); err != nil {
		return nil, err
	}

	b := &Backend{
		cfg:      cfg,
		instance: "default",
		logger:   slog.Default(),
		metrics:  defaultMetrics,
	}
	for _, o := range opts {
		if o == nil {
			panic("sftpbackend: option must not be nil")
		}
		o(b)
	}

	if !b.lazyConn {
		if err := b.connect(context.Background()); err != nil {
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
		metrics:   defaultMetrics,
		client:    client,
		connected: true,
	}
	for _, o := range opts {
		if o == nil {
			panic("sftpbackend: option must not be nil")
		}
		o(b)
	}
	return b
}

// connect establishes the SSH and SFTP connection. Caller must not hold b.mu.
func (b *Backend) connect(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.connected {
		return nil
	}

	sshCfg, err := b.buildSSHConfig(ctx)
	if err != nil {
		return storage.WrapSafe("build SSH config failed", err)
	}

	addr := net.JoinHostPort(b.cfg.Host, fmt.Sprintf("%d", b.cfg.Port))
	conn, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
		return fmt.Errorf("SSH dial failed")
	}

	sftpClient, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
		return storage.WrapSafe("SFTP client setup failed", err)
	}

	// Close old connections after replacing to prevent file descriptor leaks.
	// We close asynchronously with a short delay because in-flight operations
	// may still hold a reference to the old client (obtained via getClient()
	// before this write lock was acquired). The delay gives those operations
	// time to complete rather than causing use-after-close panics.
	//
	// The goroutine is tracked via cleanupWg so Close() can wait for it,
	// preventing file descriptor leaks if the process exits within the delay.
	//
	// Note: rapid reconnection flapping (e.g., server bouncing every few seconds)
	// can accumulate multiple cleanup goroutines, each holding old file descriptors
	// for 5 seconds. In practice this is bounded by the reconnect rate, and each
	// goroutine is short-lived. If this becomes a concern, consider canceling the
	// previous cleanup goroutine via context when a new connection is established.
	oldClient := b.client
	oldConn := b.sshConn
	if oldClient != nil || oldConn != nil {
		gen := b.cleanupGen.Add(1)
		b.cleanupWg.Add(1)
		go func() {
			defer b.cleanupWg.Done()
			// Wait up to 5s for in-flight callers to release the old
			// client, OR break early if a newer reconnect bumps the
			// generation. Polling on a 200ms ticker rather than
			// time.Sleep avoids drift and keeps shutdown latency low
			// when a flap chain queues many cleanups.
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			for b.cleanupGen.Load() == gen {
				select {
				case <-ticker.C:
				case <-deadline.C:
					goto closeOld
				}
			}
		closeOld:
			if oldClient != nil {
				_ = oldClient.Close()
			}
			if oldConn != nil {
				_ = oldConn.Close()
			}
		}()
	}

	b.client = sftpClient
	b.sshConn = conn
	b.connected = true
	b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(1)
	b.logger.Info("SFTP connected", redact.String("host", b.cfg.Host), "port", b.cfg.Port)

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

// getClient returns the SFTP client, connecting if needed (lazy connect).
func (b *Backend) getClient(ctx context.Context) (Client, error) {
	b.mu.RLock()
	if b.connected {
		client := b.client
		b.mu.RUnlock()
		return client, nil
	}
	b.mu.RUnlock()

	// connect() acquires the write lock and re-checks b.connected,
	// so concurrent callers safely converge on a single connection.
	if err := b.connect(ctx); err != nil {
		return nil, err
	}

	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()
	return client, nil
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
		return fmt.Errorf("sftpbackend: %w", err)
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
		return fmt.Errorf("sftpbackend: remote disk full: %w (cause: %w)", storage.ErrInsufficientCapacity, err)
	}
	if msg := err.Error(); strings.Contains(strings.ToLower(msg), "no space left") {
		return fmt.Errorf("sftpbackend: remote disk full: %w (cause: %w)", storage.ErrInsufficientCapacity, err)
	}
	return nil
}

// Put writes content from r to the remote path. Validators run before upload.
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

	client, err := b.getClient(ctx)
	if err != nil {
		opErr := storage.WrapSafe("sftpbackend: put connection failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

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
		opErr := fmt.Errorf("sftpbackend: unsafe parent: %w", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	if err := client.MkdirAll(dir); err != nil {
		opErr := sftpRemoteError("mkdir", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	if err := b.rejectSymlinkAncestors(client, remotePath); err != nil {
		opErr := fmt.Errorf("sftpbackend: unsafe parent: %w", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		opErr := fmt.Errorf("sftpbackend: unsafe target: %w", err)
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
		opErr := fmt.Errorf("sftpbackend: unsafe target: %w", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	if err := client.Rename(tmpPath, remotePath); err != nil {
		_ = client.Remove(tmpPath)
		b.metrics.observeOp(b.instance, "put", start, err)
		opErr := sftpRemoteError("rename", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	b.metrics.observeOp(b.instance, "put", start, nil)
	return nil
}

// Get retrieves file content from the remote path. Caller must close the returned ReadCloser.
func (b *Backend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Get")
	defer span.End()
	span.SetAttributes(attribute.Int("storage.key_len", len(key)))

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	client, err := b.getClient(ctx)
	if err != nil {
		opErr := storage.WrapSafe("sftpbackend: get connection failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}

	remotePath := b.remotePath(key)
	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		if isNotExist(err) {
			opErr := fmt.Errorf("sftpbackend: get: %w", storage.ErrObjectNotFound)
			span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
			return nil, storage.ObjectMeta{}, opErr
		}
		opErr := fmt.Errorf("sftpbackend: unsafe path: %w", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}

	start := now()
	f, err := client.Open(remotePath)
	b.metrics.observeOp(b.instance, "get", start, err)

	if err != nil {
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

	return f, meta, nil
}

// Delete removes a file at the remote path. Returns nil if the file does not exist.
func (b *Backend) Delete(ctx context.Context, key string) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Delete")
	defer span.End()
	span.SetAttributes(attribute.Int("storage.key_len", len(key)))

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	client, err := b.getClient(ctx)
	if err != nil {
		opErr := storage.WrapSafe("sftpbackend: delete connection failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	remotePath := b.remotePath(key)
	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		if isNotExist(err) {
			return nil
		}
		opErr := fmt.Errorf("sftpbackend: unsafe path: %w", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	start := now()
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

	client, err := b.getClient(ctx)
	if err != nil {
		opErr := storage.WrapSafe("sftpbackend: exists connection failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return false, opErr
	}
	remotePath := b.remotePath(key)
	if err := b.rejectSymlinkPath(client, remotePath); err != nil {
		if isNotExist(err) {
			return false, nil
		}
		opErr := fmt.Errorf("sftpbackend: unsafe path: %w", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return false, opErr
	}

	start := now()
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
// Concurrent use with Close is safe at the Go level (no data race) because
// the client reference is copied under RLock. However, calling Stat on a
// closed sftp.Client returns an error (which correctly makes Healthy return
// false). Do not rely on Healthy returning meaningful results after Close.
func (b *Backend) Healthy() bool {
	if b == nil {
		return false
	}
	b.mu.RLock()
	if !b.connected || b.client == nil {
		b.mu.RUnlock()
		return false
	}
	client := b.client
	b.mu.RUnlock()

	// Stat the root path as a lightweight health probe.
	// Performed outside the lock to avoid blocking concurrent operations
	// if the SFTP server is slow to respond.
	_, err := client.Stat(b.cfg.RootPath)
	if err != nil {
		// Mark as disconnected so the next getClient call reconnects.
		// Re-check that b.client is still the same instance we probed — if
		// connect() ran concurrently and replaced it, we must not overwrite
		// the fresh connection state.
		b.mu.Lock()
		if b.client == client {
			b.connected = false
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
// cleanup goroutines from previous reconnections to finish.
func (b *Backend) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()

	var errs []error
	if b.client != nil {
		errs = append(errs, b.client.Close())
		b.client = nil
	}
	if b.sshConn != nil {
		errs = append(errs, b.sshConn.Close())
		b.sshConn = nil
	}
	b.connected = false
	if b.metrics != nil {
		b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
	}
	b.mu.Unlock()

	// Wait for pending cleanup goroutines outside the lock to avoid blocking
	// concurrent operations during the 5-second grace period.
	b.cleanupWg.Wait()

	return errors.Join(errs...)
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
