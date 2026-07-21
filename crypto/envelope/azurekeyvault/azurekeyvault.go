// Package azurekeyvault implements [envelope.KEK] backed by Azure Key
// Vault or Managed HSM. Wrap calls Key Vault WrapKey with the configured
// key; Unwrap calls UnwrapKey using the key name and version recorded in
// the envelope header.
//
// The adapter assumes the caller has configured the Azure client with a
// credential, vault URL, retry policy, and transport. The Key Vault key
// must grant:
//
//   - keys/wrapKey for Wrap
//   - keys/unwrapKey for Unwrap
//
// Wrap returns the Azure KID from Key Vault, normally
// "https://<vault>.vault.azure.net/keys/<name>/<version>", so Unwrap
// targets the exact key version that produced the wrapped DEK. Configure
// KeyName without KeyVersion to wrap with the current primary version while
// still allowing old envelopes to unwrap with their recorded versions.
//
// asvs: V6.2.1, V6.4.1
package azurekeyvault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
)

const fallbackKeyIDScheme = "azurekeyvault"

// KeyClient is the subset of azkeys.Client used by this adapter.
// *azkeys.Client satisfies this interface.
type KeyClient interface {
	WrapKey(context.Context, string, string, azkeys.KeyOperationParameters, *azkeys.WrapKeyOptions) (azkeys.WrapKeyResponse, error)
	UnwrapKey(context.Context, string, string, azkeys.KeyOperationParameters, *azkeys.UnwrapKeyOptions) (azkeys.UnwrapKeyResponse, error)
}

// KEK is the Azure Key Vault-backed [envelope.KEK].
type KEK struct {
	c          KeyClient
	keyName    string
	keyVersion string
	keyID      string
	keyHost    string
	algorithm  azkeys.EncryptionAlgorithm
	metrics    *Metrics
}

// Option configures [NewKEK].
type Option func(*KEK)

// WithMetrics installs a custom [Metrics] for this KEK. When unset, the
// package's DefaultRegisterer-backed metrics are used.
func WithMetrics(m *Metrics) Option {
	if m == nil {
		panic("azurekeyvault: WithMetrics requires non-nil metrics (omit the option for the package default)")
	}
	return func(k *KEK) { k.metrics = m }
}

// Config bundles the kit's Azure Key Vault knobs.
type Config struct {
	// KeyID is the full Azure key identifier:
	// https://<vault>.vault.azure.net/keys/<name>/<version>. KeyID is
	// mutually exclusive with KeyName and KeyVersion.
	KeyID string

	// KeyName is the Key Vault key name used for DEK wrapping. It is
	// required when KeyID is empty and must be a single path segment.
	KeyName string

	// KeyVersion optionally pins wrapping to a specific key version.
	// Leave empty to use Key Vault's current version; Wrap still returns the
	// exact version-qualified KID for future Unwrap calls.
	KeyVersion string

	// VaultURL optionally pins the expected Azure Key Vault host so the
	// Unwrap path rejects envelope key IDs that target a different vault.
	// When KeyID is set the host is derived from it automatically; in the
	// KeyName/KeyVersion construction path Wrap returns whichever KID the
	// SDK client points at, so supplying VaultURL is the defense-in-depth
	// way to keep cross-vault key IDs from silently decrypting. Accepts
	// either the full URL ("https://kv.vault.azure.net") or a bare host.
	VaultURL string

	// Algorithm is the Key Vault wrapping algorithm. Empty defaults to
	// RSA-OAEP-256. RSA1_5 and RSA-OAEP are rejected; use RSA-OAEP-256,
	// AES-KW, or CKM AES key-wrap algorithms.
	//
	// The algorithm is NOT recorded in the envelope key ID: Unwrap always
	// uses the configured Algorithm. Changing Algorithm therefore breaks
	// Unwrap of DEKs that were wrapped under the previous algorithm. To
	// migrate, keep the old Algorithm until every existing blob has been
	// rewrapped under the new one (decrypt-then-reencrypt), then switch
	// the config.
	Algorithm azkeys.EncryptionAlgorithm
}

// LogValue implements slog.LogValuer without exposing vault URLs, key names,
// or key versions. These identifiers commonly encode tenant, environment, or
// subscription topology.
func (c Config) LogValue() slog.Value {
	alg := c.Algorithm
	if alg == "" {
		alg = azkeys.EncryptionAlgorithmRSAOAEP256
	}
	return slog.GroupValue(
		slog.Bool("key_id_configured", c.KeyID != ""),
		slog.Bool("key_name_configured", c.KeyName != ""),
		slog.Bool("key_version_pinned", c.KeyVersion != ""),
		slog.String("algorithm", string(alg)),
	)
}

// NewKEK builds a KEK from cfg using the given Azure Key Vault client.
func NewKEK(c KeyClient, cfg Config, opts ...Option) (*KEK, error) {
	if c == nil {
		return nil, errors.New("azurekeyvault: client must not be nil")
	}
	keyName, keyVersion, keyID, keyHost, err := keyConfig(cfg)
	if err != nil {
		return nil, err
	}
	algorithm, err := normalizeAlgorithm(cfg.Algorithm)
	if err != nil {
		return nil, err
	}
	k := &KEK{
		c:          c,
		keyName:    keyName,
		keyVersion: keyVersion,
		keyID:      keyID,
		keyHost:    keyHost,
		algorithm:  algorithm,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("azurekeyvault: NewKEK option must not be nil")
		}
		opt(k)
	}
	if k.metrics == nil {
		k.metrics = packageDefaultMetrics()
	}
	return k, nil
}

// KeyID implements [envelope.KEK]. It returns the configured key identity for
// telemetry only. Wrap returns the version-qualified KID from Azure Key Vault
// for actual envelope headers.
func (k *KEK) KeyID() string {
	if k == nil {
		return ""
	}
	return k.keyID
}

// Wrap implements [envelope.KEK]. Calls Key Vault WrapKey and returns the
// version-qualified Azure KID for embedding in the envelope.
func (k *KEK) Wrap(ctx context.Context, dek []byte) (string, []byte, error) {
	if err := k.validate(ctx); err != nil {
		return "", nil, err
	}
	algorithm := k.algorithm
	resp, err := k.c.WrapKey(ctx, k.keyName, k.keyVersion, azkeys.KeyOperationParameters{
		Algorithm: &algorithm,
		Value:     dek,
	}, nil)
	if err != nil {
		return "", nil, fmt.Errorf("azurekeyvault: wrap key: %w", k.classifyAzureError("wrap", err))
	}
	if resp.KID == nil || string(*resp.KID) == "" {
		return "", nil, errors.New("azurekeyvault: wrap response missing KID")
	}
	if len(resp.Result) == 0 {
		return "", nil, errors.New("azurekeyvault: wrap response missing result")
	}
	return string(*resp.KID), resp.Result, nil
}

// Unwrap implements [envelope.KEK]. It rejects key IDs for a different key
// name so a blob cannot silently decrypt under the wrong Azure key. Unwrap
// uses the configured [Config.Algorithm]; it is not recorded in the key ID,
// so changing the algorithm requires rewrapping existing blobs first.
func (k *KEK) Unwrap(ctx context.Context, keyID string, wrapped []byte) ([]byte, error) {
	if err := k.validate(ctx); err != nil {
		return nil, err
	}
	if keyID == "" {
		return nil, errors.New("azurekeyvault: keyID must not be empty")
	}
	if len(wrapped) == 0 {
		return nil, errors.New("azurekeyvault: wrapped DEK must not be empty")
	}
	keyName, keyVersion, keyHost, err := parseEnvelopeKeyID(keyID)
	if err != nil {
		return nil, err
	}
	if keyName != k.keyName {
		return nil, errors.New("azurekeyvault: unknown keyID")
	}
	if keyVersion == "" {
		return nil, errors.New("azurekeyvault: keyID must include key version")
	}
	// DNS hostnames are case-insensitive (RFC 1035 §2.3.3); compare via
	// EqualFold so a Key Vault host spelled "MyVault.vault.azure.net"
	// is treated as equal to "myvault.vault.azure.net". Wave 66 caught
	// that case-sensitive comparison rejected legitimate uppercase
	// envelope blobs.
	if k.keyHost != "" && !strings.EqualFold(keyHost, k.keyHost) {
		return nil, errors.New("azurekeyvault: unknown keyID")
	}
	algorithm := k.algorithm
	resp, err := k.c.UnwrapKey(ctx, keyName, keyVersion, azkeys.KeyOperationParameters{
		Algorithm: &algorithm,
		Value:     wrapped,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("azurekeyvault: unwrap key: %w", k.classifyAzureError("unwrap", err))
	}
	if len(resp.Result) == 0 {
		return nil, errors.New("azurekeyvault: unwrap response missing result")
	}
	return resp.Result, nil
}

// Compile-time guard.
var _ envelope.KEK = (*KEK)(nil)

func (k *KEK) validate(ctx context.Context) error {
	if k == nil || k.c == nil || k.keyName == "" || k.keyID == "" || k.algorithm == "" {
		return errors.New("azurekeyvault: KEK is not initialized")
	}
	if ctx == nil {
		return errors.New("azurekeyvault: context must not be nil")
	}
	return nil
}

func keyConfig(cfg Config) (string, string, string, string, error) {
	if cfg.KeyID != "" {
		if cfg.KeyName != "" || cfg.KeyVersion != "" {
			return "", "", "", "", errors.New("azurekeyvault: Config.KeyID is mutually exclusive with KeyName and KeyVersion")
		}
		keyName, keyVersion, keyHost, err := parseAzureKeyID(cfg.KeyID)
		if err != nil {
			return "", "", "", "", err
		}
		if cfg.VaultURL != "" {
			vaultHost, err := parseVaultHost(cfg.VaultURL)
			if err != nil {
				return "", "", "", "", err
			}
			if !strings.EqualFold(vaultHost, keyHost) {
				return "", "", "", "", errors.New("azurekeyvault: Config.VaultURL host must match Config.KeyID host")
			}
		}
		return keyName, keyVersion, cfg.KeyID, keyHost, nil
	}
	if err := validateKeySegment("Config.KeyName", cfg.KeyName, false); err != nil {
		return "", "", "", "", err
	}
	if err := validateKeySegment("Config.KeyVersion", cfg.KeyVersion, true); err != nil {
		return "", "", "", "", err
	}
	vaultHost := ""
	if cfg.VaultURL != "" {
		host, err := parseVaultHost(cfg.VaultURL)
		if err != nil {
			return "", "", "", "", err
		}
		vaultHost = host
	}
	keyID := fallbackKeyID(cfg.KeyName, cfg.KeyVersion)
	return cfg.KeyName, cfg.KeyVersion, keyID, vaultHost, nil
}

// parseVaultHost extracts the lowercased host from a Key Vault URL or
// a bare host string. Empty input is rejected by callers — this helper
// is only invoked when cfg.VaultURL is set.
func parseVaultHost(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed != raw || trimmed == "" {
		return "", errors.New("azurekeyvault: Config.VaultURL must be a clean non-empty value")
	}
	if strings.Contains(trimmed, "://") {
		u, err := url.Parse(trimmed)
		if err != nil {
			return "", errors.New("azurekeyvault: Config.VaultURL must be a valid URL")
		}
		if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("azurekeyvault: Config.VaultURL must be an https Key Vault URL")
		}
		if strings.Trim(u.Path, "/") != "" {
			return "", errors.New("azurekeyvault: Config.VaultURL must not include a path")
		}
		return strings.ToLower(u.Host), nil
	}
	if strings.ContainsAny(trimmed, "/?#") {
		return "", errors.New("azurekeyvault: Config.VaultURL must be an https URL or bare host")
	}
	return strings.ToLower(trimmed), nil
}

func normalizeAlgorithm(algorithm azkeys.EncryptionAlgorithm) (azkeys.EncryptionAlgorithm, error) {
	if algorithm == "" {
		return azkeys.EncryptionAlgorithmRSAOAEP256, nil
	}
	switch algorithm {
	case azkeys.EncryptionAlgorithmRSA15:
		return "", errors.New("azurekeyvault: RSA1_5 wrapping is not allowed")
	case azkeys.EncryptionAlgorithmRSAOAEP:
		return "", errors.New("azurekeyvault: RSA-OAEP wrapping is not allowed; use RSA-OAEP-256")
	case azkeys.EncryptionAlgorithmRSAOAEP256,
		azkeys.EncryptionAlgorithmA128KW,
		azkeys.EncryptionAlgorithmA192KW,
		azkeys.EncryptionAlgorithmA256KW,
		azkeys.EncryptionAlgorithmCKMAESKEYWRAP,
		azkeys.EncryptionAlgorithmCKMAESKEYWRAPPAD:
		return algorithm, nil
	default:
		return "", fmt.Errorf("azurekeyvault: unsupported wrapping algorithm %q", algorithm)
	}
}

func parseEnvelopeKeyID(keyID string) (string, string, string, error) {
	if strings.HasPrefix(keyID, fallbackKeyIDScheme+"://") {
		return parseFallbackKeyID(keyID)
	}
	return parseAzureKeyID(keyID)
}

func parseAzureKeyID(keyID string) (string, string, string, error) {
	if strings.TrimSpace(keyID) != keyID || keyID == "" {
		return "", "", "", errors.New("azurekeyvault: Config.KeyID must be a clean non-empty URL")
	}
	u, err := url.Parse(keyID)
	if err != nil {
		return "", "", "", errors.New("azurekeyvault: Config.KeyID must be a valid URL")
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", "", errors.New("azurekeyvault: Config.KeyID must be an https Key Vault key URL")
	}
	segments := splitCleanPath(u.Path)
	if len(segments) != 2 && len(segments) != 3 {
		return "", "", "", errors.New("azurekeyvault: Config.KeyID path must be /keys/<name>[/<version>]")
	}
	if segments[0] != "keys" {
		return "", "", "", errors.New("azurekeyvault: Config.KeyID path must start with /keys")
	}
	keyName := segments[1]
	keyVersion := ""
	if len(segments) == 3 {
		keyVersion = segments[2]
	}
	if err := validateKeySegment("Config.KeyID key name", keyName, false); err != nil {
		return "", "", "", err
	}
	if err := validateKeySegment("Config.KeyID key version", keyVersion, true); err != nil {
		return "", "", "", err
	}
	return keyName, keyVersion, u.Host, nil
}

func parseFallbackKeyID(keyID string) (string, string, string, error) {
	u, err := url.Parse(keyID)
	if err != nil || u.Scheme != fallbackKeyIDScheme || u.Host != "keys" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", "", errors.New("azurekeyvault: invalid keyID")
	}
	// Fallback scheme IDs always carry a version segment: Unwrap rejects
	// empty versions, and Wrap never emits version-less fallback IDs.
	segments := splitCleanPath(u.Path)
	if len(segments) != 2 {
		return "", "", "", errors.New("azurekeyvault: invalid keyID")
	}
	keyName := segments[0]
	keyVersion := segments[1]
	if err := validateKeySegment("keyID key name", keyName, false); err != nil {
		return "", "", "", err
	}
	if err := validateKeySegment("keyID key version", keyVersion, true); err != nil {
		return "", "", "", err
	}
	return keyName, keyVersion, "", nil
}

func fallbackKeyID(keyName, keyVersion string) string {
	if keyVersion == "" {
		return fallbackKeyIDScheme + "://keys/" + keyName
	}
	return fallbackKeyIDScheme + "://keys/" + keyName + "/" + keyVersion
}

func splitCleanPath(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func validateKeySegment(field, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("azurekeyvault: %s must not be empty", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("azurekeyvault: %s must be valid UTF-8", field)
	}
	if value == "." || value == ".." || strings.Contains(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("azurekeyvault: %s must be a single path segment", field)
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("azurekeyvault: %s contains control characters", field)
		}
	}
	return nil
}
