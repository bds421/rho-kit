// Package vaulttransit implements [envelope.KEK] backed by the
// HashiCorp Vault Transit secrets engine. Wrap calls
// `<mount>/encrypt/<key>` with a base64-encoded DEK; Unwrap calls
// `<mount>/decrypt/<key>` and decodes the returned plaintext.
//
// The adapter assumes the caller has configured the Vault client with
// address, token, namespace, TLS, and retry policy. The Transit key
// must grant:
//
//   - update on `<mount>/encrypt/<key>` for Wrap
//   - update on `<mount>/decrypt/<key>` for Unwrap
//
// Vault ciphertexts embed the Transit key version (`vault:vN:...`), so
// key rotation inside the same Transit key works without changing the
// envelope key ID. Moving to a different Transit key should be done by
// decrypting with the old KEK and writing new envelopes under a new KEK.
//
// asvs: V6.2.1, V6.4.1
//
// Observability: request-error Prometheus metrics and NewKEK opts ...Option
// currently ship in awskms and gcpkms. vaulttransit intentionally defers the same
// Option/Metrics surface until a shared envelope-level metrics hook lands;
// error classification already mirrors awskms via classify* helpers.
package vaulttransit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
)

const defaultMountPath = "transit"

// KEK is the HashiCorp Vault Transit-backed [envelope.KEK].
type KEK struct {
	c          *vaultapi.Client
	mountPath  string
	keyName    string
	keyID      string
	contextAAD []byte
	keyVersion int
}

// Config bundles the kit's Vault Transit knobs.
type Config struct {
	// MountPath is the Vault Transit engine mount path. Defaults to
	// "transit". Nested mount paths such as "team/transit" are allowed;
	// absolute paths, parent traversal, and control characters are rejected.
	MountPath string

	// KeyName is the Transit key name used for DEK wrapping. It is
	// required and must be a single path segment.
	KeyName string

	// Context is optional KEK-level AAD passed as Vault Transit
	// "context" on every encrypt/decrypt call. Vault requires derived
	// Transit keys for context-bound operations, so leave this empty unless
	// the key was created with `derived=true`.
	Context []byte

	// KeyVersion optionally pins encryption to a Transit key version.
	// Zero uses Vault's current primary version. Decrypt does not need this
	// because Vault ciphertexts carry their version.
	KeyVersion int
}

// LogValue implements slog.LogValuer without exposing Vault paths, key names,
// or context bytes. Mount paths and key names often encode environment,
// tenant, or service topology.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("mount_path_configured", c.MountPath != ""),
		slog.Bool("key_name_configured", c.KeyName != ""),
		slog.Bool("context_configured", len(c.Context) > 0),
		slog.Int("context_bytes", len(c.Context)),
		slog.Bool("key_version_pinned", c.KeyVersion > 0),
	)
}

// NewKEK builds a KEK from cfg using the given Vault client.
func NewKEK(c *vaultapi.Client, cfg Config) (*KEK, error) {
	if c == nil {
		return nil, errors.New("vaulttransit: client must not be nil")
	}
	mountPath, err := normalizeMountPath(cfg.MountPath)
	if err != nil {
		return nil, err
	}
	if err := validateKeyName(cfg.KeyName); err != nil {
		return nil, err
	}
	if cfg.KeyVersion < 0 {
		return nil, errors.New("vaulttransit: Config.KeyVersion must not be negative")
	}
	keyID := "vault://" + mountPath + "/keys/" + cfg.KeyName
	return &KEK{
		c:          c,
		mountPath:  mountPath,
		keyName:    cfg.KeyName,
		keyID:      keyID,
		contextAAD: append([]byte(nil), cfg.Context...),
		keyVersion: cfg.KeyVersion,
	}, nil
}

// KeyID implements [envelope.KEK]. Returns the Vault Transit key identity
// (mount + key name), not a secret-bearing Vault token or ciphertext.
func (k *KEK) KeyID() string {
	if k == nil {
		return ""
	}
	return k.keyID
}

// Wrap implements [envelope.KEK]. Calls Vault Transit encrypt and returns the
// configured Transit key identity for the envelope header.
func (k *KEK) Wrap(ctx context.Context, dek []byte) (string, []byte, error) {
	if err := k.validate(ctx); err != nil {
		return "", nil, err
	}
	data := map[string]any{
		"plaintext": base64.StdEncoding.EncodeToString(dek),
	}
	if len(k.contextAAD) > 0 {
		data["context"] = base64.StdEncoding.EncodeToString(k.contextAAD)
	}
	if k.keyVersion > 0 {
		data["key_version"] = k.keyVersion
	}

	secret, err := k.c.Logical().WriteWithContext(ctx, k.encryptPath(), data)
	if err != nil {
		return "", nil, fmt.Errorf("vaulttransit: encrypt: %w", classifyVaultError("encrypt", err))
	}
	ciphertext, err := secretString(secret, "ciphertext")
	if err != nil {
		return "", nil, fmt.Errorf("vaulttransit: encrypt: %w", err)
	}
	return k.keyID, []byte(ciphertext), nil
}

// Unwrap implements [envelope.KEK]. It rejects key IDs other than this
// adapter's Transit key so a blob cannot silently decrypt under the wrong
// Vault mount or key.
func (k *KEK) Unwrap(ctx context.Context, keyID string, wrapped []byte) ([]byte, error) {
	if err := k.validate(ctx); err != nil {
		return nil, err
	}
	if keyID == "" {
		return nil, errors.New("vaulttransit: keyID must not be empty")
	}
	if keyID != k.keyID {
		return nil, errors.New("vaulttransit: unknown keyID")
	}
	if len(wrapped) == 0 {
		return nil, errors.New("vaulttransit: wrapped DEK must not be empty")
	}
	data := map[string]any{
		"ciphertext": string(wrapped),
	}
	if len(k.contextAAD) > 0 {
		data["context"] = base64.StdEncoding.EncodeToString(k.contextAAD)
	}

	secret, err := k.c.Logical().WriteWithContext(ctx, k.decryptPath(), data)
	if err != nil {
		return nil, fmt.Errorf("vaulttransit: decrypt: %w", classifyVaultError("decrypt", err))
	}
	plaintext, err := secretString(secret, "plaintext")
	if err != nil {
		return nil, fmt.Errorf("vaulttransit: decrypt: %w", err)
	}
	dek, err := base64.StdEncoding.DecodeString(plaintext)
	if err != nil {
		return nil, fmt.Errorf("vaulttransit: decode plaintext: %w", err)
	}
	return dek, nil
}

// Compile-time guard.
var _ envelope.KEK = (*KEK)(nil)

func (k *KEK) validate(ctx context.Context) error {
	if k == nil || k.c == nil || k.mountPath == "" || k.keyName == "" || k.keyID == "" {
		return errors.New("vaulttransit: KEK is not initialized")
	}
	if ctx == nil {
		return errors.New("vaulttransit: context must not be nil")
	}
	return nil
}

func (k *KEK) encryptPath() string {
	return path.Join(k.mountPath, "encrypt", k.keyName)
}

func (k *KEK) decryptPath() string {
	return path.Join(k.mountPath, "decrypt", k.keyName)
}

func secretString(secret *vaultapi.Secret, field string) (string, error) {
	if secret == nil || secret.Data == nil {
		return "", errors.New("missing secret data")
	}
	raw, ok := secret.Data[field]
	if !ok {
		return "", fmt.Errorf("response missing %s", field)
	}
	value, ok := raw.(string)
	if !ok || value == "" {
		return "", fmt.Errorf("response %s must be a non-empty string", field)
	}
	return value, nil
}

func normalizeMountPath(mountPath string) (string, error) {
	if mountPath == "" {
		return defaultMountPath, nil
	}
	if !utf8.ValidString(mountPath) {
		return "", errors.New("vaulttransit: Config.MountPath must be valid UTF-8")
	}
	for _, r := range mountPath {
		if r == 0 || unicode.IsControl(r) {
			return "", errors.New("vaulttransit: Config.MountPath contains control characters")
		}
	}
	cleaned := path.Clean(mountPath)
	if strings.HasPrefix(mountPath, "/") || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != mountPath {
		return "", errors.New("vaulttransit: Config.MountPath must be a clean relative path")
	}
	return cleaned, nil
}

func validateKeyName(keyName string) error {
	if keyName == "" {
		return errors.New("vaulttransit: Config.KeyName must not be empty")
	}
	if !utf8.ValidString(keyName) {
		return errors.New("vaulttransit: Config.KeyName must be valid UTF-8")
	}
	if keyName == "." || keyName == ".." || strings.Contains(keyName, "/") {
		return errors.New("vaulttransit: Config.KeyName must be a single path segment")
	}
	for _, r := range keyName {
		if r == 0 || unicode.IsControl(r) {
			return errors.New("vaulttransit: Config.KeyName contains control characters")
		}
	}
	return nil
}
