package netutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// ErrServerNameRequired is returned by the [ReloadingClientTLS]
// VerifyConnection callback when the server name is empty. Since the
// reloading client config sets [tls.Config.InsecureSkipVerify] to
// true and replaces verification with VerifyConnection, an empty
// ServerName would let the peer's chain pass with no hostname
// binding — strictly weaker than the stock Go TLS client. Callers
// MUST set [tls.Config.ServerName] (typically via the SDK's
// per-target setter) before dialing.
var ErrServerNameRequired = errors.New("netutil: ReloadingClientTLS requires tls.Config.ServerName to be set before dialing")

// CertificateSource is the read side of a hot-rotatable TLS keypair
// and trust store. Each call returns the freshest material the source
// can provide; a Reloading source caches between reloads so callers can
// invoke these freely on every handshake without paying for I/O.
//
// Implementations MUST be safe for concurrent use — both the kit and
// the goroutine watching for rotation events will read concurrently
// with handshake callbacks. A nil receiver is treated as "no material";
// the kit never holds a nil source after construction succeeds.
type CertificateSource interface {
	// ServerCertificate returns the leaf certificate + chain used by
	// servers to identify themselves to clients. The returned pointer
	// MUST be safe to share across goroutines — implementations may
	// return the same pointer for many calls.
	ServerCertificate() (*tls.Certificate, error)

	// ClientCertificate returns the leaf certificate + chain used by
	// clients to identify themselves to servers (mTLS). Same sharing
	// contract as ServerCertificate.
	ClientCertificate() (*tls.Certificate, error)

	// CAs returns the trust pool used to verify the peer's certificate.
	// Server use: ClientCAs (which CAs the server trusts to sign client
	// certs). Client use: RootCAs (which CAs the client trusts to sign
	// server certs). Both ends share the same pool here because the kit
	// is opinionated about mTLS configuration.
	CAs() (*x509.CertPool, error)
}

// FilesCertificateSource loads cert/key/CA material from files on disk
// and refreshes the cached snapshot when [Reload] is called or when
// the optional poll goroutine fires. Suitable for Kubernetes secret
// mounts and Vault Agent template-rendered files, both of which
// atomically swap files in place after rotation.
//
// Reload failures (typically a half-written file caught mid-rotation)
// keep the previous good snapshot in service. The metrics + log
// surface let on-call distinguish "rotation succeeded" from "we're
// still serving last-good while reload is broken".
//
// Safe for concurrent use after construction. Call [Close] to stop
// the polling goroutine when [WithReloadInterval] enabled polling.
// Close is mandatory in that case: the poll goroutine holds a reference
// to the source, so dropping the last external reference without Close
// does NOT make the source unreachable and leaks the goroutine, ticker,
// and cert snapshot for the process lifetime.
type FilesCertificateSource struct {
	cfg TLSConfig

	// snapshot is the atomically swapped read-side state. Handshake
	// callbacks read snapshot.Load() without any lock — the snapshot
	// pointer is immutable once published.
	snapshot atomic.Pointer[certSnapshot]

	// reloadMu serialises reload work so two triggers in flight don't
	// both call into the file system simultaneously. The snapshot
	// pointer is still atomic so readers never see a torn write.
	reloadMu sync.Mutex

	logger     *slog.Logger
	pollEvery  time.Duration
	stopCh     chan struct{}
	doneCh     chan struct{}
	closeOnce  sync.Once
	startOnce  sync.Once
	reloadErrs atomic.Uint64
	reloads    atomic.Uint64
}

type certSnapshot struct {
	cert   tls.Certificate
	caPEM  []byte // raw PEM for cheap equality checks across reloads
	caPool *x509.CertPool
}

// FilesCertificateSourceOption configures the source at construction.
type FilesCertificateSourceOption func(*filesCertificateSourceOpts)

type filesCertificateSourceOpts struct {
	logger    *slog.Logger
	pollEvery time.Duration
}

// WithReloadLogger pins the slog.Logger used for rotation events.
// A nil logger is normalised to [slog.Default] so the source never
// holds a nil logger and rotation events are always recorded.
func WithReloadLogger(l *slog.Logger) FilesCertificateSourceOption {
	return func(o *filesCertificateSourceOpts) {
		if l == nil {
			o.logger = slog.Default()
			return
		}
		o.logger = l
	}
}

// WithReloadInterval enables periodic background reload of the
// configured cert/key/CA files. Default behaviour (no option) is
// reload-on-demand: callers must invoke [FilesCertificateSource.Reload]
// after rotation (e.g. in a SIGHUP handler).
//
// Polling intervals shorter than 1 second are clamped to 1 second
// — sub-second filesystem polling is wasteful and risks reading a
// half-written file before the orchestrator finishes the atomic
// swap. The duration must be positive.
func WithReloadInterval(d time.Duration) FilesCertificateSourceOption {
	if d <= 0 {
		panic("netutil: WithReloadInterval requires a positive duration")
	}
	if d < time.Second {
		d = time.Second
	}
	return func(o *filesCertificateSourceOpts) { o.pollEvery = d }
}

// NewFilesCertificateSource constructs a [FilesCertificateSource] from
// the given TLS config. The initial cert/key/CA load runs synchronously
// so misconfigured files surface as a construction error rather than a
// silent serve-the-old-snapshot. Pass [WithReloadInterval] to enable
// background polling.
func NewFilesCertificateSource(cfg TLSConfig, opts ...FilesCertificateSourceOption) (*FilesCertificateSource, error) {
	if !cfg.Enabled() {
		return nil, errors.New("netutil: NewFilesCertificateSource requires fully-configured TLSConfig (CACert+Cert+Key)")
	}
	o := filesCertificateSourceOpts{logger: slog.Default()}
	for _, opt := range opts {
		if opt == nil {
			panic("netutil: NewFilesCertificateSource option must not be nil")
		}
		opt(&o)
	}
	src := &FilesCertificateSource{
		cfg:       cfg,
		logger:    o.logger,
		pollEvery: o.pollEvery,
	}
	if err := src.loadInitial(); err != nil {
		return nil, err
	}
	if o.pollEvery > 0 {
		src.stopCh = make(chan struct{})
		src.doneCh = make(chan struct{})
		src.startOnce.Do(func() {
			go src.poll()
		})
	}
	return src, nil
}

func (s *FilesCertificateSource) loadInitial() error {
	snap, err := loadCertSnapshot(s.cfg)
	if err != nil {
		return err
	}
	s.snapshot.Store(snap)
	s.reloads.Add(1)
	return nil
}

// Reload re-reads the configured cert/key/CA files and atomically
// publishes the new snapshot. A reload error keeps the previous good
// snapshot in place and bumps [FilesCertificateSource.ReloadErrors].
// Safe to call from a SIGHUP handler, an fsnotify event loop, or
// arbitrary management endpoints.
func (s *FilesCertificateSource) Reload() error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	snap, err := loadCertSnapshot(s.cfg)
	if err != nil {
		s.reloadErrs.Add(1)
		// redact.Error sanitizes the wrapped error so cert/key/CA
		// file paths from os.ReadFile and tls.LoadX509KeyPair do not
		// leak the deployment's filesystem topology into structured
		// logs.
		s.logger.Error("netutil: TLS reload failed — keeping previous good snapshot",
			slog.String("reason", TLSLoadErrorReason(err)),
			redact.Error(err))
		return err
	}
	prev := s.snapshot.Load()
	s.snapshot.Store(snap)
	s.reloads.Add(1)
	if prev == nil || !certEqual(prev, snap) {
		s.logger.Info("netutil: TLS material reloaded")
	}
	return nil
}

func (s *FilesCertificateSource) poll() {
	defer close(s.doneCh)
	t := time.NewTicker(s.pollEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			_ = s.Reload()
		}
	}
}

// Close stops the background poll goroutine. Safe to call multiple
// times; subsequent reads still return the last-published snapshot.
func (s *FilesCertificateSource) Close() error {
	s.closeOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
			<-s.doneCh
		}
	})
	return nil
}

// Reloads returns the number of successful loads (including the
// initial load at construction). Useful for /readyz and dashboards.
func (s *FilesCertificateSource) Reloads() uint64 { return s.reloads.Load() }

// ReloadErrors returns the number of reload attempts that failed
// while the source is alive. Non-zero with no recent Reloads bump is
// the operational signal that rotation is broken.
func (s *FilesCertificateSource) ReloadErrors() uint64 { return s.reloadErrs.Load() }

// ServerCertificate implements [CertificateSource].
func (s *FilesCertificateSource) ServerCertificate() (*tls.Certificate, error) {
	snap := s.snapshot.Load()
	if snap == nil {
		return nil, errors.New("netutil: FilesCertificateSource snapshot is empty")
	}
	return &snap.cert, nil
}

// ClientCertificate implements [CertificateSource]. The same cert is
// returned for both directions because the kit configuration is
// symmetric — operators that need split server/client identities
// should compose two sources.
func (s *FilesCertificateSource) ClientCertificate() (*tls.Certificate, error) {
	return s.ServerCertificate()
}

// CAs implements [CertificateSource].
func (s *FilesCertificateSource) CAs() (*x509.CertPool, error) {
	snap := s.snapshot.Load()
	if snap == nil {
		return nil, errors.New("netutil: FilesCertificateSource snapshot is empty")
	}
	return snap.caPool, nil
}

func loadCertSnapshot(cfg TLSConfig) (*certSnapshot, error) {
	cert, caPEM, caPool, err := loadTLSMaterial(cfg)
	if err != nil {
		return nil, err
	}
	return &certSnapshot{cert: cert, caPEM: caPEM, caPool: caPool}, nil
}

func certEqual(a, b *certSnapshot) bool {
	if a == nil || b == nil {
		return false
	}
	if len(a.caPEM) != len(b.caPEM) {
		return false
	}
	for i := range a.caPEM {
		if a.caPEM[i] != b.caPEM[i] {
			return false
		}
	}
	if len(a.cert.Certificate) != len(b.cert.Certificate) {
		return false
	}
	for i, chain := range a.cert.Certificate {
		if len(chain) != len(b.cert.Certificate[i]) {
			return false
		}
		for j := range chain {
			if chain[j] != b.cert.Certificate[i][j] {
				return false
			}
		}
	}
	return true
}

// ReloadingServerTLS returns a *tls.Config wired to read cert + CA pool
// from the [CertificateSource] on every handshake, so an atomic-swap
// rotation of the underlying files is picked up without restart.
//
// The returned config sets ClientAuth based on opts (defaults to
// RequireAndVerifyClientCert) and pins TLS 1.3.
func ReloadingServerTLS(src CertificateSource, opts ...ServerTLSOption) *tls.Config {
	if src == nil {
		panic("netutil: ReloadingServerTLS requires a non-nil CertificateSource")
	}
	o := serverTLSOpts{requireClientCert: true}
	for _, opt := range opts {
		if opt == nil {
			panic("netutil: ReloadingServerTLS option must not be nil")
		}
		opt(&o)
	}
	clientAuth := tls.VerifyClientCertIfGiven
	if o.requireClientCert {
		clientAuth = tls.RequireAndVerifyClientCert
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: clientAuth,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return src.ServerCertificate()
		},
		// GetConfigForClient fires on every handshake, so swapping
		// ClientCAs takes effect immediately on the next connection.
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			cert, err := src.ServerCertificate()
			if err != nil {
				return nil, err
			}
			caPool, err := src.CAs()
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				MinVersion:   tls.VersionTLS13,
				ClientAuth:   clientAuth,
				ClientCAs:    caPool,
				Certificates: []tls.Certificate{*cert},
			}, nil
		},
	}
}

// ReloadingClientTLS returns a *tls.Config wired to read the client
// cert and trust pool from the [CertificateSource] on every handshake.
//
// RootCAs is not hot-rotatable through tls.Config alone — Go's TLS
// stack snapshots it at Dial. This config uses
// [tls.Config.GetClientCertificate] for the client identity (which is
// dynamic) and [tls.Config.VerifyConnection] to verify the peer's
// chain against the freshest CA pool on every handshake.
//
// Hostname-verification safety: ServerName MUST be set on every dial
// (either via [tls.Config.ServerName] on a cloned config or via the
// SDK's per-target setter). Since we set [tls.Config.InsecureSkipVerify]
// to true to replace verification with [tls.Config.VerifyConnection],
// an empty ServerName would otherwise let the chain pass without
// hostname binding — strictly weaker than the stock Go TLS client.
// VerifyConnection fails closed in that case and refuses the handshake
// with [ErrServerNameRequired].
func ReloadingClientTLS(src CertificateSource) *tls.Config {
	if src == nil {
		panic("netutil: ReloadingClientTLS requires a non-nil CertificateSource")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // we replace verification with VerifyConnection
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return src.ClientCertificate()
		},
		VerifyConnection: func(state tls.ConnectionState) error {
			if state.ServerName == "" {
				return ErrServerNameRequired
			}
			pool, err := src.CAs()
			if err != nil {
				return fmt.Errorf("netutil: client TLS verify: %w", err)
			}
			if len(state.PeerCertificates) == 0 {
				return errors.New("netutil: client TLS verify: server presented no certificates")
			}
			intermediates := x509.NewCertPool()
			for _, c := range state.PeerCertificates[1:] {
				intermediates.AddCert(c)
			}
			opts := x509.VerifyOptions{
				Roots:         pool,
				Intermediates: intermediates,
				DNSName:       state.ServerName,
			}
			if _, err := state.PeerCertificates[0].Verify(opts); err != nil {
				return fmt.Errorf("netutil: client TLS verify: %w", err)
			}
			return nil
		},
	}
}

// WaitForReload blocks until the next successful reload after the
// caller's snapshot version, or returns ctx.Err() if cancelled.
// Useful in startup probes that need to confirm rotation is wired
// correctly before serving traffic.
func (s *FilesCertificateSource) WaitForReload(ctx context.Context, since uint64) error {
	if ctx == nil {
		return errors.New("netutil: WaitForReload requires a non-nil context")
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.reloads.Load() > since {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
