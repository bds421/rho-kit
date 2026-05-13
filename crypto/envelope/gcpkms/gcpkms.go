// Package gcpkms implements [envelope.KEK] backed by GCP KMS. Wrap
// calls Encrypt with the configured key resource path; Unwrap calls
// Decrypt. GCP KMS handles version rotation through key versions
// scoped under a parent key resource — Wrap returns the
// version-qualified resource name so Unwrap targets the same
// version.
//
// The adapter assumes the caller has set up GCP credentials
// (Application Default Credentials) and the KMS key grants:
//
//   - cloudkms.cryptoKeyVersions.useToEncrypt for Wrap
//   - cloudkms.cryptoKeyVersions.useToDecrypt for Unwrap
//
// # Transit integrity (CRC32C)
//
// Every request sends a CRC32C checksum of the plaintext and AAD
// (or ciphertext + AAD on decrypt). KMS echoes verification flags
// confirming Google computed the checksum and it matched on receipt.
// Every response carries a CRC32C of the value it produced, which
// the adapter verifies against the bytes it actually received.
// This catches in-flight bit-flips between the client and Google's
// edge that would otherwise pass silently — TLS protects against
// active tampering, but proxies, NICs, and middleboxes occasionally
// corrupt bytes in ways that survive the TLS frame check (rare but
// observed). The ChecksumMismatchError surfaces these so callers
// can retry with a clear signal rather than confused-deputy with
// the body AEAD layer.
//
// asvs: V6.2.1, V6.4.1
package gcpkms

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"strings"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
)

// crc32cTable is the Castagnoli polynomial — what Google KMS uses for
// its CRC32C field. Allocated once at package load so per-call
// hash.Sum32 has no allocation overhead.
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// ErrChecksumMismatch indicates a CRC32C verification failed on a
// KMS request or response. Either KMS observed a different checksum
// than the adapter computed (request corrupted in flight) or the
// adapter observed a different checksum than KMS computed (response
// corrupted in flight). Callers should retry on this error — it is
// distinct from cryptographic auth failure and indicates a transient
// transport problem.
var ErrChecksumMismatch = errors.New("gcpkms: CRC32C verification failed; retry the request")

// KEK is the GCP KMS-backed [envelope.KEK].
type KEK struct {
	c   *kms.KeyManagementClient
	key string // full resource path: projects/.../locations/.../keyRings/.../cryptoKeys/...
	aad []byte
}

// Config bundles the kit's GCP KMS knobs.
type Config struct {
	// KeyResource is the key path. Use the parent key (without
	// version) so GCP KMS auto-selects the primary version on
	// Encrypt; Wrap returns the version-qualified resource for
	// Unwrap.
	KeyResource string

	// AdditionalAuthenticatedData is the AAD passed on every Wrap
	// and Unwrap. Best practice: include tenant or table identifier
	// to prevent ciphertext replay across contexts.
	AdditionalAuthenticatedData []byte
}

// LogValue implements slog.LogValuer to avoid logging cloud key resources or
// AAD bytes, which often contain project, tenant, table, or row identifiers.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("key_resource_configured", c.KeyResource != ""),
		slog.Bool("aad_configured", len(c.AdditionalAuthenticatedData) > 0),
		slog.Int("aad_bytes", len(c.AdditionalAuthenticatedData)),
	)
}

// NewKEK builds a KEK from cfg using the given KMS client. Returns an
// error if KeyResource is empty.
func NewKEK(c *kms.KeyManagementClient, cfg Config) (*KEK, error) {
	if c == nil {
		return nil, errors.New("gcpkms: client must not be nil")
	}
	if cfg.KeyResource == "" {
		return nil, errors.New("gcpkms: Config.KeyResource must not be empty")
	}
	return &KEK{c: c, key: cfg.KeyResource, aad: append([]byte(nil), cfg.AdditionalAuthenticatedData...)}, nil
}

// KeyID implements [envelope.KEK]. Returns the parent key resource
// — telemetry only.
func (k *KEK) KeyID() string {
	if k == nil {
		return ""
	}
	return k.key
}

// Wrap implements [envelope.KEK]. Calls GCP KMS Encrypt and returns
// the version-qualified resource name (e.g.
// "projects/.../cryptoKeyVersions/3") for the envelope header so
// Unwrap targets that exact version. Sends and verifies CRC32C
// checksums on both the request (plaintext + AAD) and response
// (ciphertext) per Google's transit-integrity guidelines.
func (k *KEK) Wrap(ctx context.Context, dek []byte) (string, []byte, error) {
	if err := k.validate(ctx); err != nil {
		return "", nil, err
	}
	req := &kmspb.EncryptRequest{
		Name:                              k.key,
		Plaintext:                         dek,
		PlaintextCrc32C:                   crc32c(dek),
		AdditionalAuthenticatedData:       k.aad,
		AdditionalAuthenticatedDataCrc32C: crc32c(k.aad),
	}
	resp, err := k.c.Encrypt(ctx, req)
	if err != nil {
		return "", nil, fmt.Errorf("gcpkms: encrypt: %w", classifyGCPError("encrypt", err))
	}
	if resp.GetName() == "" {
		return "", nil, errors.New("gcpkms: encrypt response missing Name")
	}
	if !resp.GetVerifiedPlaintextCrc32C() {
		return "", nil, fmt.Errorf("%w: KMS did not verify plaintext CRC32C — request bytes corrupted in flight", ErrChecksumMismatch)
	}
	// FR-044 [MED]: only assert AAD verification when the AAD is
	// actually present. An empty []byte AAD is semantically equivalent
	// to nil — both produce a nil CRC wrapper via crc32c() — but a
	// length check on `len(k.aad) > 0` is the correct gate so callers
	// who supply []byte{} do not get every wrap rejected.
	if len(k.aad) > 0 && !resp.GetVerifiedAdditionalAuthenticatedDataCrc32C() {
		return "", nil, fmt.Errorf("%w: KMS did not verify AAD CRC32C — request bytes corrupted in flight", ErrChecksumMismatch)
	}
	ciphertext := resp.GetCiphertext()
	if got, want := crc32.Checksum(ciphertext, crc32cTable), uint32(resp.GetCiphertextCrc32C().GetValue()); got != want {
		return "", nil, responseChecksumMismatchError("ciphertext")
	}
	return resp.GetName(), ciphertext, nil
}

// Unwrap implements [envelope.KEK]. Calls GCP KMS Decrypt with the
// version-qualified Name pinned at Wrap time. Sends and verifies
// CRC32C checksums on both the request (ciphertext + AAD) and
// response (plaintext).
func (k *KEK) Unwrap(ctx context.Context, keyID string, wrapped []byte) ([]byte, error) {
	if err := k.validate(ctx); err != nil {
		return nil, err
	}
	if keyID == "" {
		return nil, errors.New("gcpkms: keyID must not be empty")
	}
	if !allowsKeyID(k.key, keyID) {
		return nil, errors.New("gcpkms: keyID does not match this KEK")
	}
	req := &kmspb.DecryptRequest{
		Name:                              keyID,
		Ciphertext:                        wrapped,
		CiphertextCrc32C:                  crc32c(wrapped),
		AdditionalAuthenticatedData:       k.aad,
		AdditionalAuthenticatedDataCrc32C: crc32c(k.aad),
	}
	resp, err := k.c.Decrypt(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("gcpkms: decrypt: %w", classifyGCPError("decrypt", err))
	}
	plaintext := resp.GetPlaintext()
	if got, want := crc32.Checksum(plaintext, crc32cTable), uint32(resp.GetPlaintextCrc32C().GetValue()); got != want {
		return nil, responseChecksumMismatchError("plaintext")
	}
	return plaintext, nil
}

func responseChecksumMismatchError(kind string) error {
	return fmt.Errorf("%w: response %s CRC32C mismatch; response bytes corrupted in flight", ErrChecksumMismatch, kind)
}

// crc32c returns a wrapped Int64 holding the Castagnoli CRC32C of
// data. Returns nil when data is empty so KMS treats the field as
// unset (the API rejects zero CRC32C values for non-empty payloads
// when ForceSendFields is not set; we sidestep that path by omitting
// the wrapper rather than sending {Value: 0}).
func crc32c(data []byte) *wrapperspb.Int64Value {
	if len(data) == 0 {
		return nil
	}
	return wrapperspb.Int64(int64(crc32.Checksum(data, crc32cTable)))
}

// Compile-time guard.
var _ envelope.KEK = (*KEK)(nil)

func (k *KEK) validate(ctx context.Context) error {
	if k == nil || k.c == nil || k.key == "" {
		return errors.New("gcpkms: KEK is not initialized")
	}
	if ctx == nil {
		return errors.New("gcpkms: context must not be nil")
	}
	return nil
}

// allowsKeyID reports whether keyID matches the configured parent
// key, either exactly or as a version-qualified suffix of the form
// "<parent>/cryptoKeyVersions/<N>" where N is a positive integer.
// Rejects any other shape so a blob written for a different key
// cannot redirect the decrypt call.
func allowsKeyID(parent, keyID string) bool {
	if keyID == parent {
		return true
	}
	const sep = "/cryptoKeyVersions/"
	prefix := parent + sep
	if !strings.HasPrefix(keyID, prefix) {
		return false
	}
	version := keyID[len(prefix):]
	if version == "" {
		return false
	}
	for _, r := range version {
		if r < '0' || r > '9' {
			return false
		}
	}
	return version[0] != '0' || version == "0"
}
