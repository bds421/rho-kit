// Package awskms implements [envelope.KEK] backed by AWS KMS. Wrap
// calls KMS Encrypt with the configured KeyID; Unwrap calls Decrypt.
// AWS KMS handles key rotation internally — the returned KeyId in
// the Encrypt response is the key ARN of the KMS key used (AWS KMS
// does not expose per-version ARNs); KMS resolves the correct rotated
// key material on Decrypt. We pass the ARN through to the envelope so
// Decrypt later targets the same KMS key. When Config.KeyID is an alias,
// Unwrap forwards the envelope's key ARN (after partition/region/account
// scope checks) so repointing the alias for rotation does not make prior
// envelopes undecryptable.
//
// The adapter assumes the caller has set up AWS credentials (env
// vars, IRSA, EC2 role) and the KMS key has appropriate IAM grants:
//
//   - kms:Encrypt for Wrap
//   - kms:Decrypt for Unwrap
//
// EncryptionContext is supported as a static, adapter-level KMS
// encryption_context map applied to every Wrap and Unwrap. It must
// remain stable for the lifetime of stored envelopes — changing it
// makes existing ciphertexts undecryptable. Treat it as a constant
// audit attribute (service name, environment), not a per-row binding;
// per-envelope AAD belongs in the envelope caller's aad argument.
// See also the parent envelope package doc on EncryptionContext vs
// caller AAD.
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

// kmsAPI is the subset of *kms.Client this adapter calls. It exists so
// the Wrap/Unwrap KMS round-trips can be exercised with a fake in tests
// without constructing a live *kms.Client. *kms.Client satisfies it.
type kmsAPI interface {
	Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// KEK is the AWS KMS-backed [envelope.KEK].
type KEK struct {
	c       kmsAPI
	keyID   string
	context map[string]string
	// region is captured from the KMS client at construction time so
	// the bare-keyID decrypt path can reject envelope ARNs from a
	// different region. Empty when the SDK client did not surface a
	// region (best-effort).
	region  string
	metrics *Metrics
}

// Option configures [NewKEK].
type Option func(*KEK)

// WithMetrics installs a custom [Metrics] for this KEK. When unset,
// the package's lazily-initialised DefaultRegisterer-backed Metrics is
// used. Pass [WithMetrics] explicitly with a [NewMetrics]-constructed
// instance so awskms collectors land on a non-default registerer.
//
// Panics if m is nil — a nil Metrics would defeat the purpose of an
// "observability enabled" toggle. Omit the option entirely to fall
// back to the package default.
func WithMetrics(m *Metrics) Option {
	if m == nil {
		panic("awskms: WithMetrics requires non-nil metrics (omit the option for the package default)")
	}
	return func(k *KEK) { k.metrics = m }
}

// Config bundles the kit's KMS knobs.
type Config struct {
	// KeyID is the ARN, key ID, or alias of the KMS key to use.
	// Aliases (alias/name) point at the latest key version.
	KeyID string

	// EncryptionContext is the static KMS encryption_context map
	// passed on every Wrap and Unwrap. Keep it constant for the
	// lifetime of stored envelopes (service/environment audit
	// attributes). Do not put per-row identifiers here — use the
	// envelope Encryptor's per-call AAD for row-level binding; a
	// reconfigured EncryptionContext cannot unwrap existing blobs.
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
// error if KeyID is empty. The client's region is captured (best-effort)
// so the bare-keyID decrypt path can reject envelope ARNs from a
// different region.
func NewKEK(c *kms.Client, cfg Config, opts ...Option) (*KEK, error) {
	if c == nil {
		return nil, errors.New("awskms: client must not be nil")
	}
	if cfg.KeyID == "" {
		return nil, errors.New("awskms: Config.KeyID must not be empty")
	}
	k := &KEK{
		c:       c,
		keyID:   cfg.KeyID,
		context: cloneContext(cfg.EncryptionContext),
		region:  c.Options().Region,
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("awskms: NewKEK option must not be nil")
		}
		opt(k)
	}
	if k.metrics == nil {
		k.metrics = packageDefaultMetrics()
	}
	return k, nil
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
// key ARN reported by KMS for embedding in the envelope.
//
// Errors from AWS KMS are routed through [classifyAWSError] so retryable
// failures (Throttling, KMSInternalException) become
// [apperror.UnavailableError] and permanent failures (KeyUnavailable,
// Disabled, AccessDenied) become [apperror.PermanentError]. The raw AWS
// error is preserved as the wrapped cause.
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
		return "", nil, fmt.Errorf("awskms: encrypt: %w", k.classifyAWSError("wrap", err))
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
		return nil, fmt.Errorf("awskms: decrypt: %w", k.classifyAWSError("unwrap", err))
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
// bytes can be modified.
//
// Alias-configured KEKs:
//   - When the envelope carries a key ARN produced by Wrap (AWS Encrypt
//     returns the concrete key ARN), forward that ARN after a scope check
//     so repointing the alias (the standard AWS manual-rotation workflow)
//     does not orphan envelopes written under the previous target key.
//   - Alias-form envelope IDs still decrypt through the configured alias.
func (k *KEK) decryptKeyIDFor(keyID string) (string, error) {
	if keyID == k.keyID {
		return keyID, nil
	}

	if isKMSAliasID(k.keyID) {
		// Forward a scoped key ARN so alias retargeting keeps prior
		// envelopes decryptable via their recorded key ARN.
		if isKMSKeyARN(keyID) {
			if isKMSAliasARN(k.keyID) {
				if !sameKMSARNScope(k.keyID, keyID) {
					return "", errors.New("awskms: keyID does not match this KEK")
				}
			} else if k.region != "" {
				region, ok := kmsARNRegion(keyID)
				if !ok || region != k.region {
					return "", errors.New("awskms: keyID region does not match this KEK")
				}
			}
			return keyID, nil
		}
		// Alias-form envelope IDs: decrypt through the configured alias only
		// when the envelope alias is in scope (or the config is a bare alias
		// name — then only exact match is accepted via the keyID==k.keyID
		// short-circuit above, or reject foreign alias ARNs).
		if isKMSAliasARN(keyID) && isKMSAliasARN(k.keyID) && sameKMSARNScope(k.keyID, keyID) {
			// Same account/region alias ARN family — still pin to configured
			// alias resource rather than a foreign alias name.
			if kmsARNResourceEqual(k.keyID, keyID) {
				return k.keyID, nil
			}
		}
		return "", errors.New("awskms: keyID does not match this KEK")
	}

	// Bare key UUID configuration: accept only a well-formed key ARN whose
	// resource is exactly "key/<configured-id>" (not a loose suffix match that
	// could accept crafted nested resources). When the client region is known,
	// pin it; when unknown, reject ARN forms fail-closed rather than forwarding
	// an attacker-controlled ARN to Decrypt.
	if isKMSKeyARN(keyID) {
		resource, ok := kmsARNResource(keyID)
		if !ok || resource != "key/"+k.keyID {
			return "", errors.New("awskms: keyID does not match this KEK")
		}
		if k.region == "" {
			return "", errors.New("awskms: keyID ARN requires known client region for bare key ID config")
		}
		region, ok := kmsARNRegion(keyID)
		if !ok || region != k.region {
			return "", errors.New("awskms: keyID region does not match this KEK")
		}
		return keyID, nil
	}

	return "", errors.New("awskms: keyID does not match this KEK")
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

func kmsARNResourceEqual(a, b string) bool {
	ar, okA := kmsARNResource(a)
	br, okB := kmsARNResource(b)
	return okA && okB && ar == br
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

func kmsARNRegion(s string) (string, bool) {
	parts := strings.SplitN(s, ":", 6)
	if len(parts) != 6 {
		return "", false
	}
	if parts[0] != "arn" || parts[2] != "kms" || parts[3] == "" {
		return "", false
	}
	return parts[3], true
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
