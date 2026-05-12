// Package awskms implements [envelope.KEK] backed by AWS KMS. Wrap
// calls KMS Encrypt with the configured KeyID; Unwrap calls Decrypt.
// AWS KMS handles key rotation internally — the returned KeyId in
// the Encrypt response is the version-qualified key ARN, which we
// pass through to the envelope so Decrypt later targets the same
// version.
//
// The adapter assumes the caller has set up AWS credentials (env
// vars, IRSA, EC2 role) and the KMS key has appropriate IAM grants:
//
//   - kms:Encrypt for Wrap
//   - kms:Decrypt for Unwrap
//
// Encryption context (AAD) support is intentionally omitted from this
// scaffold — adding it requires aligning the kit's envelope format
// with KMS's encryption_context map in a future minor release.
//
// asvs: V6.2.1, V6.4.1
package awskms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
)

// KEK is the AWS KMS-backed [envelope.KEK].
type KEK struct {
	c       *kms.Client
	keyID   string
	context map[string]string
}

// Config bundles the kit's KMS knobs.
type Config struct {
	// KeyID is the ARN, key ID, or alias of the KMS key to use.
	// Aliases (alias/name) point at the latest key version.
	KeyID string

	// EncryptionContext is the AAD (Additional Authenticated Data)
	// passed to KMS on every Wrap and Unwrap. Best practice per AWS
	// guidance: include tenant ID, table name, or other binding
	// data so a stolen ciphertext cannot be replayed in a different
	// context.
	EncryptionContext map[string]string
}

// LogValue implements slog.LogValuer to avoid logging cloud key identifiers or
// encryption-context values. Key IDs and aliases commonly reveal account IDs,
// environment names, tenant names, or key-ring layout.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("key_id_configured", c.KeyID != ""),
		slog.Any("encryption_context", redactedContext(c.EncryptionContext)),
	)
}

// NewKEK builds a KEK from cfg using the given KMS client. Returns an
// error if KeyID is empty.
func NewKEK(c *kms.Client, cfg Config) (*KEK, error) {
	if c == nil {
		return nil, errors.New("awskms: client must not be nil")
	}
	if cfg.KeyID == "" {
		return nil, errors.New("awskms: Config.KeyID must not be empty")
	}
	return &KEK{c: c, keyID: cfg.KeyID, context: cloneContext(cfg.EncryptionContext)}, nil
}

// KeyID implements [envelope.KEK]. Returns the configured AWS key
// identifier — telemetry only; envelope writes use the ID returned
// by Wrap to avoid TOCTOU races.
func (k *KEK) KeyID() string {
	if k == nil {
		return ""
	}
	return k.keyID
}

// Wrap implements [envelope.KEK]. Calls KMS Encrypt and returns the
// version-qualified KeyId for embedding in the envelope.
func (k *KEK) Wrap(ctx context.Context, dek []byte) (string, []byte, error) {
	if err := k.validate(ctx); err != nil {
		return "", nil, err
	}
	out, err := k.c.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(k.keyID),
		Plaintext:         dek,
		EncryptionContext: k.context,
	})
	if err != nil {
		return "", nil, fmt.Errorf("awskms: encrypt: %w", err)
	}
	if out.KeyId == nil {
		return "", nil, errors.New("awskms: encrypt response missing KeyId")
	}
	return *out.KeyId, out.CiphertextBlob, nil
}

// Unwrap implements [envelope.KEK]. Calls KMS Decrypt with KeyId
// pinned to the keyID returned at Wrap time so a misconfigured
// alias cannot silently retarget decryption. Rejects keyIDs that
// do not match this adapter's configured KEK before any KMS call
// so an attacker-controlled blob cannot redirect the decrypt
// request at a different AWS key.
func (k *KEK) Unwrap(ctx context.Context, keyID string, wrapped []byte) ([]byte, error) {
	if err := k.validate(ctx); err != nil {
		return nil, err
	}
	if keyID == "" {
		return nil, errors.New("awskms: keyID must not be empty")
	}
	decryptKeyID, err := k.decryptKeyIDFor(keyID)
	if err != nil {
		return nil, err
	}
	out, err := k.c.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    wrapped,
		KeyId:             aws.String(decryptKeyID),
		EncryptionContext: k.context,
	})
	if err != nil {
		return nil, fmt.Errorf("awskms: decrypt: %w", err)
	}
	return out.Plaintext, nil
}

// Compile-time guard.
var _ envelope.KEK = (*KEK)(nil)

func (k *KEK) validate(ctx context.Context) error {
	if k == nil || k.c == nil || k.keyID == "" {
		return errors.New("awskms: KEK is not initialized")
	}
	if ctx == nil {
		return errors.New("awskms: context must not be nil")
	}
	return nil
}

// decryptKeyIDFor validates the envelope key ID and returns the KeyId to send
// to AWS KMS Decrypt. The envelope header is attacker-controlled once stored
// bytes can be modified, so alias-configured KEKs intentionally decrypt through
// the configured alias rather than forwarding an arbitrary key ARN from the
// envelope.
func (k *KEK) decryptKeyIDFor(keyID string) (string, error) {
	if keyID == k.keyID {
		return keyID, nil
	}

	if isKMSAliasID(k.keyID) {
		if (isKMSKeyARN(keyID) || isKMSAliasARN(keyID)) && !isKMSAliasARN(k.keyID) {
			return k.keyID, nil
		}
		if (isKMSKeyARN(keyID) || isKMSAliasARN(keyID)) && sameKMSARNScope(k.keyID, keyID) {
			return k.keyID, nil
		}
		return "", errors.New("awskms: keyID does not match this KEK")
	}

	if len(keyID) > len(k.keyID) && hasSuffixSegment(keyID, k.keyID) {
		if !isKMSKeyARN(keyID) {
			return "", errors.New("awskms: keyID does not match this KEK")
		}
		return keyID, nil
	}

	return "", errors.New("awskms: keyID does not match this KEK")
}

// hasSuffixSegment reports whether s ends with seg AND seg is
// preceded by a path separator (":", "/") or a digit/letter
// boundary that anchors the match at a segment edge — preventing
// "alias/badkey" from matching when seg is "key".
func hasSuffixSegment(s, seg string) bool {
	if len(s) < len(seg) {
		return false
	}
	if s[len(s)-len(seg):] != seg {
		return false
	}
	if len(s) == len(seg) {
		return true
	}
	prev := s[len(s)-len(seg)-1]
	return prev == '/' || prev == ':'
}

func isKMSAliasID(s string) bool {
	return strings.HasPrefix(s, "alias/") || isKMSAliasARN(s)
}

func isKMSAliasARN(s string) bool {
	resource, ok := kmsARNResource(s)
	return ok && strings.HasPrefix(resource, "alias/")
}

func isKMSKeyARN(s string) bool {
	resource, ok := kmsARNResource(s)
	return ok && strings.HasPrefix(resource, "key/")
}

func sameKMSARNScope(a, b string) bool {
	aParts := strings.SplitN(a, ":", 6)
	bParts := strings.SplitN(b, ":", 6)
	if len(aParts) != 6 || len(bParts) != 6 {
		return false
	}
	for i := 0; i < 5; i++ {
		if aParts[i] != bParts[i] {
			return false
		}
	}
	return aParts[2] == "kms" && bParts[2] == "kms"
}

func kmsARNResource(s string) (string, bool) {
	parts := strings.SplitN(s, ":", 6)
	if len(parts) != 6 {
		return "", false
	}
	if parts[0] != "arn" || parts[2] != "kms" || parts[3] == "" || parts[4] == "" {
		return "", false
	}
	return parts[5], true
}

func cloneContext(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func redactedContext(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k := range in {
		out[k] = "[REDACTED]"
	}
	return out
}
