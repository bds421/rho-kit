package pyroscope

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/grafana/pyroscope-go"
)

// Config configures a [Component].
type Config struct {
	// ServerAddress is the Pyroscope ingest endpoint (e.g.
	// "http://pyroscope:4040" or "https://pyroscope.example.com").
	// Required.
	ServerAddress string
	// AppName identifies the service in Pyroscope. Required.
	// Recommended shape: "{tenant}.{service}" or just "{service}".
	AppName string
	// Tags are attached to every uploaded profile. Tag cardinality
	// directly affects storage; tag by environment / version, NEVER
	// by user-ID, request-ID, or trace-ID.
	Tags map[string]string
	// UploadRate is the profile upload interval. Default 15s. The
	// pyroscope-go runtime keeps a rolling in-memory sample buffer
	// and flushes on this tick.
	UploadRate time.Duration
	// ProfileTypes selects which profile kinds to capture. Defaults
	// to CPU + AllocObjects + InuseObjects, which covers the common
	// "where is CPU going?" and "what is allocating?" questions
	// without the goroutine-trace overhead.
	ProfileTypes []pyroscope.ProfileType
	// AuthToken is sent as a bearer token (Grafana Cloud / hosted
	// Pyroscope). Empty for self-hosted open-source Pyroscope.
	AuthToken string
	// TenantID is sent as the X-Scope-OrgID header for multi-tenant
	// Pyroscope deployments. Empty for single-tenant.
	TenantID string
}

// Option configures a [Component] beyond [Config].
type Option func(*componentConfig)

type componentConfig struct {
	logger *slog.Logger
}

// WithLogger overrides the slog.Logger used for start/stop and
// pyroscope-internal error logging. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *componentConfig) { c.logger = l }
}

// DefaultProfileTypes returns the kit's recommended profile set:
// CPU, allocation count, allocation bytes, in-use objects, in-use
// space. Skips goroutine traces because they're expensive and rarely
// useful for the common "why is this slow?" question.
func DefaultProfileTypes() []pyroscope.ProfileType {
	return []pyroscope.ProfileType{
		pyroscope.ProfileCPU,
		pyroscope.ProfileAllocObjects,
		pyroscope.ProfileAllocSpace,
		pyroscope.ProfileInuseObjects,
		pyroscope.ProfileInuseSpace,
	}
}

// Component returns a [lifecycle.Component]-compatible profiler. It
// starts the pyroscope-go session on Start and stops it on Stop.
//
// Returns a validation error for missing/invalid config; never panics
// on user input. Panics only on nil options (programmer error).
func Component(cfg Config, opts ...Option) (*Profiler, error) {
	if cfg.ServerAddress == "" {
		return nil, errors.New("pyroscope: Config.ServerAddress is required")
	}
	if cfg.AppName == "" {
		return nil, errors.New("pyroscope: Config.AppName is required")
	}
	cc := componentConfig{}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("pyroscope: option must not be nil")
		}
		opt(&cc)
	}
	if cc.logger == nil {
		cc.logger = slog.Default()
	}
	if cfg.UploadRate <= 0 {
		cfg.UploadRate = 15 * time.Second
	}
	if len(cfg.ProfileTypes) == 0 {
		cfg.ProfileTypes = DefaultProfileTypes()
	}
	return &Profiler{cfg: cfg, logger: cc.logger}, nil
}

// Profiler implements the kit's lifecycle.Component contract. Not
// goroutine-safe — Start/Stop are intended to be called once per
// process lifetime by the Runner.
type Profiler struct {
	cfg     Config
	logger  *slog.Logger
	session *pyroscope.Profiler
}

// Start initialises the pyroscope-go session. Returns immediately
// after the upload goroutine is launched; the function blocks only
// until ctx is cancelled, then returns. This matches the kit's
// lifecycle.Component contract ("Start blocks until done or ctx
// cancelled").
func (p *Profiler) Start(ctx context.Context) error {
	if p.session != nil {
		return errors.New("pyroscope: Start called more than once")
	}
	cfg := pyroscope.Config{
		ApplicationName: p.cfg.AppName,
		ServerAddress:   p.cfg.ServerAddress,
		Tags:            p.cfg.Tags,
		ProfileTypes:    p.cfg.ProfileTypes,
		UploadRate:      p.cfg.UploadRate,
		AuthToken:       p.cfg.AuthToken,
		TenantID:        p.cfg.TenantID,
		Logger:          slogPyroscopeAdapter{l: p.logger},
	}
	session, err := pyroscope.Start(cfg)
	if err != nil {
		return fmt.Errorf("pyroscope: Start: %w", err)
	}
	p.session = session
	p.logger.Info("pyroscope profiler started",
		slog.String("app", p.cfg.AppName),
		slog.String("server", p.cfg.ServerAddress),
		slog.Duration("upload_rate", p.cfg.UploadRate),
	)
	<-ctx.Done()
	return nil
}

// Stop ends the pyroscope session and flushes the final profile batch.
// Idempotent.
func (p *Profiler) Stop(_ context.Context) error {
	if p.session == nil {
		return nil
	}
	session := p.session
	p.session = nil
	if err := session.Stop(); err != nil {
		return fmt.Errorf("pyroscope: Stop: %w", err)
	}
	p.logger.Info("pyroscope profiler stopped")
	return nil
}

// slogPyroscopeAdapter bridges pyroscope-go's small logger interface
// to slog.Logger. pyroscope-go expects an Infof / Debugf / Errorf
// shape; we route each to the matching slog level.
type slogPyroscopeAdapter struct {
	l *slog.Logger
}

func (s slogPyroscopeAdapter) Infof(format string, args ...any) {
	s.l.Info(fmt.Sprintf(format, args...))
}

func (s slogPyroscopeAdapter) Debugf(format string, args ...any) {
	s.l.Debug(fmt.Sprintf(format, args...))
}

func (s slogPyroscopeAdapter) Errorf(format string, args ...any) {
	s.l.Error(fmt.Sprintf(format, args...))
}
