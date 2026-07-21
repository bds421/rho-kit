package kafkabackend

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/bds421/rho-kit/core/v2/tlsclone"
)

const minimumTLSVersion = tls.VersionTLS12

// Config captures the broker-level connection parameters shared by
// [Publisher] and [Subscriber]. Only the brokers list is required;
// the remaining fields are optional safety overrides.
type Config struct {
	// Brokers is the bootstrap broker list (host:port). At least one
	// entry is required.
	Brokers []string

	// ClientID is propagated to kafka-go's Writer.Client; appears in
	// broker-side logs as the connecting client identifier. Optional —
	// defaults to "rho-kit".
	ClientID string

	// TLS, when non-nil, is cloned, raised to TLS 1.2 minimum, and used
	// for every connection to a broker. The kit-style anti-downgrade
	// guardrail in [tlsclone.ConfigWithFloor] applies.
	TLS *tls.Config

	// SASLMechanism + SASLUsername + SASLPassword configure SASL/PLAIN
	// or SASL/SCRAM-SHA-256 / SCRAM-SHA-512 authentication when set.
	// The mechanism is interpreted by [Connect] at Writer/Reader
	// construction time. Unsupported mechanisms fail-fast at startup.
	SASLMechanism string
	SASLUsername  string
	SASLPassword  string

	// AllowInsecure opts the connection out of the FR-073 production
	// safety check. Without TLS or SASL, [ValidateConfig] refuses to
	// build a Publisher or Subscriber. Set this for genuinely trusted
	// single-host setups (local dev, ephemeral test containers).
	AllowInsecure bool
}

// LogValue keeps broker hostnames and credentials out of slog records;
// only structural booleans (configured / not configured) are surfaced.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("broker_count", len(c.Brokers)),
		slog.Bool("client_id_configured", c.ClientID != ""),
		slog.Bool("tls_configured", c.TLS != nil),
		slog.Bool("sasl_configured", c.SASLMechanism != ""),
		slog.Bool("sasl_username_configured", c.SASLUsername != ""),
		slog.Bool("sasl_password_configured", c.SASLPassword != ""),
		slog.Bool("allow_insecure", c.AllowInsecure),
	)
}

// Clone returns a detached copy of cfg. Slices and TLS state are
// duplicated so callers can store the returned value past their setup
// phase without later mutation altering runtime wiring.
func (c Config) Clone() (Config, error) {
	c.Brokers = append([]string(nil), c.Brokers...)
	if c.TLS != nil {
		cloned, err := cloneTLSConfigWithFloor(c.TLS)
		if err != nil {
			return Config{}, err
		}
		c.TLS = cloned
	}
	return c, nil
}

func cloneTLSConfigWithFloor(cfg *tls.Config) (*tls.Config, error) {
	opts := []tlsclone.Option(nil)
	if cfg != nil && cfg.InsecureSkipVerify && cfg.VerifyConnection != nil {
		opts = append(opts, tlsclone.WithAllowInsecureSkipVerify())
	}
	cloned, err := tlsclone.ConfigWithFloor(cfg, minimumTLSVersion, opts...)
	if err != nil {
		if errors.Is(err, tlsclone.ErrInsecureSkipVerifyNotPermitted) {
			return nil, errors.New("kafkabackend: TLS InsecureSkipVerify=true is not permitted")
		}
		return nil, errors.New("kafkabackend: TLS MaxVersion must allow TLS 1.2 or newer")
	}
	return cloned, nil
}

// ValidateConfig enforces the kit's FR-073 safety contract for Kafka:
// a backend instance must use TLS, SASL, or explicit AllowInsecure.
// It also checks that broker addresses are non-empty host:port pairs
// and surfaces unsupported SASL mechanisms at startup.
func ValidateConfig(cfg Config) error {
	if len(cfg.Brokers) == 0 {
		return errors.New("kafkabackend: Config.Brokers must not be empty")
	}
	for i, b := range cfg.Brokers {
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("kafkabackend: Config.Brokers[%d] must not be blank", i)
		}
	}
	if cfg.SASLMechanism != "" {
		switch strings.ToUpper(cfg.SASLMechanism) {
		case "PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512":
		default:
			return fmt.Errorf("kafkabackend: unsupported SASL mechanism %q (supported: PLAIN, SCRAM-SHA-256, SCRAM-SHA-512)", cfg.SASLMechanism)
		}
		if cfg.SASLUsername == "" || cfg.SASLPassword == "" {
			return errors.New("kafkabackend: SASLMechanism requires SASLUsername and SASLPassword")
		}
	}
	if cfg.AllowInsecure {
		return nil
	}
	// SASL/PLAIN sends credentials in cleartext without TLS — refuse
	// unless the caller explicitly opted into AllowInsecure. SCRAM is
	// also a shared-secret exchange and is treated the same way.
	if cfg.SASLMechanism != "" && cfg.TLS == nil {
		return errors.New("kafkabackend: SASL credentials require TLS (or explicit AllowInsecure) so broker secrets are not sent in cleartext (audit FR-073)")
	}
	if cfg.TLS != nil || cfg.SASLMechanism != "" {
		return nil
	}
	return errors.New("kafkabackend: Config requires TLS, SASL, or explicit AllowInsecure (audit FR-073)")
}
