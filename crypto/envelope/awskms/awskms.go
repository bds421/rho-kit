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
// with KMS's encryption_context map. Track in v2.x roadmap.
//
// asvs: V6.2.1, V6.4.1
package awskms

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/bds421/rho-kit/crypto/envelope"
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

// New builds a KEK from cfg using the given KMS client. Returns an
// error if KeyID is empty.
func New(c *kms.Client, cfg Config) (*KEK, error) {
	if c == nil {
		return nil, errors.New("awskms: client must not be nil")
	}
	if cfg.KeyID == "" {
		return nil, errors.New("awskms: Config.KeyID must not be empty")
	}
	return &KEK{c: c, keyID: cfg.KeyID, context: cfg.EncryptionContext}, nil
}

// KeyID implements [envelope.KEK]. Returns the configured AWS key
// identifier — telemetry only; envelope writes use the ID returned
// by Wrap to avoid TOCTOU races.
func (k *KEK) KeyID() string { return k.keyID }

// Wrap implements [envelope.KEK]. Calls KMS Encrypt and returns the
// version-qualified KeyId for embedding in the envelope.
func (k *KEK) Wrap(ctx context.Context, dek []byte) (string, []byte, error) {
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
// alias cannot silently retarget decryption.
func (k *KEK) Unwrap(ctx context.Context, keyID string, wrapped []byte) ([]byte, error) {
	out, err := k.c.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    wrapped,
		KeyId:             aws.String(keyID),
		EncryptionContext: k.context,
	})
	if err != nil {
		return nil, fmt.Errorf("awskms: decrypt: %w", err)
	}
	return out.Plaintext, nil
}

// Compile-time guard.
var _ envelope.KEK = (*KEK)(nil)
