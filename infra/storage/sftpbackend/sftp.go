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
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/bds421/rho-kit/infra/storage"
)

const tracerName = "kit/storage/sftp"

// Compile-time interface compliance check.
var _ storage.Storage = (*SFTPBackend)(nil)

// SFTPClient abstracts the sftp.Client methods used by SFTPBackend.
//
// Note: Create and Open return *sftp.File (a concrete type) because sftp.File
// combines io.ReadWriteCloser with io.Seeker and Stat — there is no standard
// interface covering all these. This means Put and Get cannot be unit-tested
// with a mock client; use integration tests with a real SFTP server instead.
type SFTPClient interface {
	Create(path string) (*sftp.File, error)
	Open(path string) (*sftp.File, error)
	Remove(path string) error
	Rename(oldname, newname string) error
	Stat(path string) (os.FileInfo, error)
	MkdirAll(path string) error
	ReadDir(path string) ([]os.FileInfo, error)
	Close() error
}

// SFTPBackend implements [storage.Storage] using SFTP.
type SFTPBackend struct {
	cfg        SFTPConfig
	instance   string
	validators []storage.Validator
	logger     *slog.Logger
	metrics    *SFTPMetrics

	mu         sync.RWMutex
	client     SFTPClient
	sshConn    io.Closer
	connected  bool
	lazyConn   bool

	// cleanupWg tracks pending cleanup goroutines that close replaced connections.
	// Close() waits for them to finish to prevent file descriptor leaks on shutdown.
	cleanupWg sync.WaitGroup
}

// Option configures an SFTPBackend.
type Option func(*SFTPBackend)

// WithInstance sets the Prometheus instance label. Defaults to "default".
func WithInstance(name string) Option {
	return func(b *SFTPBackend) {
		if name == "" {
			panic("sftpbackend: instance name must not be empty")
		}
		b.instance = name
	}
}

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	return func(b *SFTPBackend) {
		b.validators = append(b.validators, validators...)
	}
}

// WithLazyConnect defers the SSH/SFTP connection until the first operation.
// By default, New connects eagerly and returns an error if the connection fails.
func WithLazyConnect() Option {
	return func(b *SFTPBackend) {
		b.lazyConn = true
	}
}

// WithLogger sets a structured logger. Defaults to slog.Default().
func WithLogger(logger *slog.Logger) Option {
	return func(b *SFTPBackend) {
		b.logger = logger
	}
}

// WithRegisterer sets the Prometheus registerer for SFTP metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(b *SFTPBackend) {
		b.metrics = NewSFTPMetrics(reg)
	}
}

// New creates a new SFTPBackend. By default, it connects eagerly.
// Use WithLazyConnect to defer connection.
func New(cfg SFTPConfig, opts ...Option) (*SFTPBackend, error) {
	if cfg.Host == "" {
		panic("sftpbackend: SFTPConfig.Host is required")
	}

	b := &SFTPBackend{
		cfg:      cfg,
		instance: "default",
		logger:   slog.Default(),
		metrics:  defaultSFTPMetrics,
	}
	for _, o := range opts {
		o(b)
	}

	if !b.lazyConn {
		if err := b.connect(); err != nil {
			return nil, fmt.Errorf("sftpbackend: initial connect: %w", err)
		}
	}

	return b, nil
}

// NewWithClient creates an SFTPBackend with a custom client, for testing.
func NewWithClient(client SFTPClient, cfg SFTPConfig, opts ...Option) *SFTPBackend {
	b := &SFTPBackend{
		cfg:       cfg,
		instance:  "default",
		logger:    slog.Default(),
		metrics:   defaultSFTPMetrics,
		client:    client,
		connected: true,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// connect establishes the SSH and SFTP connection. Caller must not hold b.mu.
func (b *SFTPBackend) connect() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.connected {
		return nil
	}

	sshCfg, err := b.buildSSHConfig()
	if err != nil {
		return fmt.Errorf("build SSH config: %w", err)
	}

	addr := net.JoinHostPort(b.cfg.Host, fmt.Sprintf("%d", b.cfg.Port))
	conn, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
		return fmt.Errorf("SSH dial %s: %w", addr, err)
	}

	sftpClient, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
		return fmt.Errorf("SFTP client from SSH conn: %w", err)
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
		b.cleanupWg.Add(1)
		go func() {
			defer b.cleanupWg.Done()
			time.Sleep(5 * time.Second)
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
	b.logger.Info("SFTP connected", "host", b.cfg.Host, "port", b.cfg.Port)

	return nil
}

func (b *SFTPBackend) buildSSHConfig() (*ssh.ClientConfig, error) {
	if b.cfg.Password == "" && b.cfg.KeyFile == "" {
		return nil, fmt.Errorf("no SSH authentication method configured (need Password or KeyFile)")
	}

	cfg := &ssh.ClientConfig{
		User:    b.cfg.User,
		Timeout: sshConnectTimeout,
	}

	switch {
	case b.cfg.InsecureSkipHostKeyVerify:
		cfg.HostKeyCallback = ssh.InsecureIgnoreHostKey() //nolint:gosec // opt-in via config
	case b.cfg.KnownHostsFile != "":
		hostKeyCallback, err := knownhosts.New(b.cfg.KnownHostsFile)
		if err != nil {
			return nil, fmt.Errorf("load known_hosts %q: %w", b.cfg.KnownHostsFile, err)
		}
		cfg.HostKeyCallback = hostKeyCallback
	default:
		return nil, fmt.Errorf("SSH host key verification requires KnownHostsFile or InsecureSkipHostKeyVerify=true")
	}

	if b.cfg.Password != "" {
		cfg.Auth = []ssh.AuthMethod{ssh.Password(b.cfg.Password)}
	} else {
		key, err := os.ReadFile(b.cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("read SSH key file %q: %w", b.cfg.KeyFile, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse SSH key: %w", err)
		}
		cfg.Auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	}

	return cfg, nil
}

// getClient returns the SFTP client, connecting if needed (lazy connect).
func (b *SFTPBackend) getClient() (SFTPClient, error) {
	b.mu.RLock()
	if b.connected {
		client := b.client
		b.mu.RUnlock()
		return client, nil
	}
	b.mu.RUnlock()

	// connect() acquires the write lock and re-checks b.connected,
	// so concurrent callers safely converge on a single connection.
	if err := b.connect(); err != nil {
		return nil, err
	}

	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()
	return client, nil
}

// remotePath joins the root path with the given key.
func (b *SFTPBackend) remotePath(key string) string {
	return path.Join(b.cfg.RootPath, key)
}

// Put writes content from r to the remote path. Validators run before upload.
func (b *SFTPBackend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Put")
	defer span.End()
	span.SetAttributes(attribute.String("storage.key", key))

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(r, &meta, b.validators)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	client, err := b.getClient()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("sftpbackend: put %q: %w", key, err)
	}

	remotePath := b.remotePath(key)
	tmpPath := remotePath + ".tmp-" + randomSuffix()

	// Ensure parent directory exists.
	dir := path.Dir(remotePath)
	if err := client.MkdirAll(dir); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("sftpbackend: mkdir %q: %w", dir, err)
	}

	start := now()

	// Write to a temp file and rename on success for atomic Put.
	// This prevents readers from seeing partially-written content.
	f, err := client.Create(tmpPath)
	if err != nil {
		b.metrics.observeOp(b.instance, "put", start, err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("sftpbackend: create %q: %w", key, err)
	}

	if _, err := io.Copy(f, validated); err != nil {
		_ = f.Close()
		_ = client.Remove(tmpPath)
		b.metrics.observeOp(b.instance, "put", start, err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("sftpbackend: write %q: %w", key, err)
	}

	if err := f.Close(); err != nil {
		_ = client.Remove(tmpPath)
		b.metrics.observeOp(b.instance, "put", start, err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("sftpbackend: close %q: %w", key, err)
	}

	if err := client.Rename(tmpPath, remotePath); err != nil {
		_ = client.Remove(tmpPath)
		b.metrics.observeOp(b.instance, "put", start, err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("sftpbackend: rename %q: %w", key, err)
	}

	b.metrics.observeOp(b.instance, "put", start, nil)
	return nil
}

// Get retrieves file content from the remote path. Caller must close the returned ReadCloser.
func (b *SFTPBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Get")
	defer span.End()
	span.SetAttributes(attribute.String("storage.key", key))

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	client, err := b.getClient()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, storage.ObjectMeta{}, fmt.Errorf("sftpbackend: get %q: %w", key, err)
	}

	start := now()
	f, err := client.Open(b.remotePath(key))
	b.metrics.observeOp(b.instance, "get", start, err)

	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		if isNotExist(err) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("sftpbackend: get %q: %w", key, storage.ErrObjectNotFound)
		}
		return nil, storage.ObjectMeta{}, fmt.Errorf("sftpbackend: get %q: %w", key, err)
	}

	meta := storage.ObjectMeta{}
	if info, statErr := f.Stat(); statErr == nil {
		meta.Size = info.Size()
	}

	return f, meta, nil
}

// Delete removes a file at the remote path. Returns nil if the file does not exist.
func (b *SFTPBackend) Delete(ctx context.Context, key string) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Delete")
	defer span.End()
	span.SetAttributes(attribute.String("storage.key", key))

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	client, err := b.getClient()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("sftpbackend: delete %q: %w", key, err)
	}

	start := now()
	err = client.Remove(b.remotePath(key))
	b.metrics.observeOp(b.instance, "delete", start, err)

	if err != nil {
		if isNotExist(err) {
			return nil
		}
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("sftpbackend: delete %q: %w", key, err)
	}
	return nil
}

// Exists reports whether the key exists on the remote server.
func (b *SFTPBackend) Exists(ctx context.Context, key string) (bool, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "sftp.Exists")
	defer span.End()
	span.SetAttributes(attribute.String("storage.key", key))

	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	client, err := b.getClient()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return false, fmt.Errorf("sftpbackend: exists %q: %w", key, err)
	}

	start := now()
	_, err = client.Stat(b.remotePath(key))
	b.metrics.observeOp(b.instance, "exists", start, err)

	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		span.SetStatus(codes.Error, err.Error())
		return false, fmt.Errorf("sftpbackend: exists %q: %w", key, err)
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
func (b *SFTPBackend) Healthy() bool {
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
			b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
			b.logger.Warn("SFTP health probe failed, marking disconnected",
				"host", b.cfg.Host, "error", err)
		}
		b.mu.Unlock()
		return false
	}
	return true
}

// Close closes the SFTP and SSH connections and waits for any pending
// cleanup goroutines from previous reconnections to finish.
func (b *SFTPBackend) Close() error {
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
	b.metrics.connectionHealthy.WithLabelValues(b.instance).Set(0)
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

// ssh_FX_NO_SUCH_FILE is the SSH FXP status code for "no such file".
const ssh_FX_NO_SUCH_FILE = 2

// sshConnectTimeout prevents indefinite hangs when the SFTP host is
// unreachable (e.g. a firewall silently drops packets).
const sshConnectTimeout = 10 * time.Second

// randomSuffix returns a crypto-random hex string for unique temp file names.
// Using crypto/rand avoids collisions that time.Now().UnixNano() can have
// when concurrent goroutines call Put on the same key.
func randomSuffix() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
