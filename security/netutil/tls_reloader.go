package netutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

const defaultTLSDebounce = 200 * time.Millisecond

// TLSReloader watches TLS certificate files and atomically reloads them
// when they change. It provides dynamic [tls.Config] instances that use
// GetCertificate / GetClientCertificate callbacks, so the TLS stack
// automatically picks up rotated certificates without rebuilding transports.
//
// Kubernetes rotates mounted secrets by atomically swapping a symlink.
// TLSReloader detects this via fsnotify (directory-level watch) and
// reloads all three files (CA, cert, key) together.
//
// # Usage
//
//	reloader, err := netutil.NewTLSReloader(tlsConfig, logger)
//	go reloader.Start(ctx)
//
//	serverTLS := reloader.ServerTLS() // uses GetCertificate callback
//	clientTLS := reloader.ClientTLS() // uses GetClientCertificate callback
type TLSReloader struct {
	cfg    TLSConfig
	logger *slog.Logger

	// cert holds the current certificate atomically.
	cert atomic.Pointer[tls.Certificate]
	// caPool holds the current CA pool atomically.
	caPool atomic.Pointer[x509.CertPool]
}

// NewTLSReloader creates a reloader that watches the files specified in cfg.
// Performs an initial load and returns an error if the certificates are invalid.
// Returns nil if TLS is not enabled (cfg.Enabled() == false).
func NewTLSReloader(cfg TLSConfig, logger *slog.Logger) (*TLSReloader, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	r := &TLSReloader{cfg: cfg, logger: logger}

	if err := r.reload(); err != nil {
		return nil, fmt.Errorf("tls reloader: initial load: %w", err)
	}

	return r, nil
}

// ServerTLS returns a *tls.Config for servers that dynamically reads the
// certificate via GetCertificate. The CA pool for client verification is
// also dynamic.
func (r *TLSReloader) ServerTLS() *tls.Config {
	return &tls.Config{
		GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return r.cert.Load(), nil
		},
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  r.caPool.Load(),
		MinVersion: tls.VersionTLS13,
	}
}

// ClientTLS returns a *tls.Config for clients that dynamically reads the
// certificate via GetClientCertificate. The RootCAs pool is also dynamic.
func (r *TLSReloader) ClientTLS() *tls.Config {
	return &tls.Config{
		GetClientCertificate: func(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return r.cert.Load(), nil
		},
		RootCAs:    r.caPool.Load(),
		MinVersion: tls.VersionTLS13,
	}
}

// Start watches certificate files and reloads on change. Blocks until ctx
// is cancelled. Implements lifecycle.Component-compatible signature.
func (r *TLSReloader) Start(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("tls reloader: create watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// Watch directories containing the cert files. This catches both
	// direct writes and Kubernetes symlink swaps.
	dirs := uniqueDirs(r.cfg.CACert, r.cfg.Cert, r.cfg.Key)
	for _, dir := range dirs {
		if err := watcher.Add(dir); err != nil {
			return fmt.Errorf("tls reloader: watch %s: %w", dir, err)
		}
	}

	r.logger.Info("tls reloader started", "ca", r.cfg.CACert, "cert", r.cfg.Cert, "key", r.cfg.Key)

	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time
	relevantFiles := map[string]bool{
		filepath.Base(r.cfg.CACert): true,
		filepath.Base(r.cfg.Cert):   true,
		filepath.Base(r.cfg.Key):    true,
	}

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if !relevantFiles[filepath.Base(event.Name)] {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if debounceTimer == nil {
				debounceTimer = time.NewTimer(defaultTLSDebounce)
				debounceCh = debounceTimer.C
			} else {
				debounceTimer.Reset(defaultTLSDebounce)
			}

		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			r.logger.Warn("tls reloader watcher error", "error", watchErr)

		case <-debounceCh:
			debounceCh = nil
			if err := r.reload(); err != nil {
				r.logger.Warn("tls reloader: reload failed, keeping previous certs", "error", err)
			} else {
				r.logger.Info("tls certificates reloaded")
			}
		}
	}
}

// reload reads all three cert files and atomically swaps the stored values.
func (r *TLSReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.cfg.Cert, r.cfg.Key)
	if err != nil {
		return fmt.Errorf("load cert/key: %w", err)
	}

	caPEM, err := os.ReadFile(r.cfg.CACert)
	if err != nil {
		return fmt.Errorf("read CA cert: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("failed to parse CA certificate")
	}

	r.cert.Store(&cert)
	r.caPool.Store(caPool)
	return nil
}

// uniqueDirs returns deduplicated directory paths for the given file paths.
func uniqueDirs(paths ...string) []string {
	seen := make(map[string]bool, len(paths))
	var result []string
	for _, p := range paths {
		dir := filepath.Dir(p)
		if !seen[dir] {
			seen[dir] = true
			result = append(result, dir)
		}
	}
	return result
}
