package azurekeyvault

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
)

var _ slog.LogValuer = Config{}

func TestConfigLogValueRedactsKeyIdentifiers(t *testing.T) {
	cfg := Config{
		KeyID:     "https://prod-vault.vault.azure.net/keys/tenant-secret/version-secret",
		Algorithm: azkeys.EncryptionAlgorithmA256KW,
	}

	rendered := cfg.LogValue().String()

	for _, secret := range []string{"prod-vault", "tenant-secret", "version-secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q in %q", secret, rendered)
		}
	}
	for _, expected := range []string{"key_id_configured=true", "algorithm=A256KW"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("LogValue %q missing %q", rendered, expected)
		}
	}
}

func TestNewParsesAzureKeyID(t *testing.T) {
	k, err := New(&fakeKeyClient{}, Config{
		KeyID: "https://vault.vault.azure.net/keys/wrap-key/123",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if k.keyName != "wrap-key" || k.keyVersion != "123" {
		t.Fatalf("parsed key = %q/%q, want wrap-key/123", k.keyName, k.keyVersion)
	}
	if k.keyHost != "vault.vault.azure.net" {
		t.Fatalf("parsed key host = %q, want vault.vault.azure.net", k.keyHost)
	}
	if got := k.KeyID(); got != "https://vault.vault.azure.net/keys/wrap-key/123" {
		t.Fatalf("KeyID = %q", got)
	}
}

func TestNewBuildsFallbackKeyIDFromName(t *testing.T) {
	k, err := New(&fakeKeyClient{}, Config{KeyName: "wrap-key", KeyVersion: "123"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := k.KeyID(); got != "azurekeyvault://keys/wrap-key/123" {
		t.Fatalf("KeyID = %q", got)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"nil key", Config{}},
		{"conflicting key identity", Config{KeyID: "https://vault.vault.azure.net/keys/k/1", KeyName: "k"}},
		{"http key ID", Config{KeyID: "http://vault.vault.azure.net/keys/k/1"}},
		{"query key ID", Config{KeyID: "https://vault.vault.azure.net/keys/k/1?token=secret"}},
		{"wrong path", Config{KeyID: "https://vault.vault.azure.net/secrets/k/1"}},
		{"slash key name", Config{KeyName: "team/key"}},
		{"control version", Config{KeyName: "key", KeyVersion: "bad\nversion"}},
		{"legacy RSA1_5", Config{KeyName: "key", Algorithm: azkeys.EncryptionAlgorithmRSA15}},
		{"legacy RSA-OAEP", Config{KeyName: "key", Algorithm: azkeys.EncryptionAlgorithmRSAOAEP}},
		{"unsupported encrypt alg", Config{KeyName: "key", Algorithm: azkeys.EncryptionAlgorithmA256GCM}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(&fakeKeyClient{}, tt.cfg); err == nil {
				t.Fatal("New expected error")
			}
		})
	}

	if _, err := New(nil, Config{KeyName: "key"}); err == nil {
		t.Fatal("New nil client expected error")
	}
}

func TestKEKInvalidStateReturnsErrors(t *testing.T) {
	var nilKEK *KEK
	if got := nilKEK.KeyID(); got != "" {
		t.Fatalf("nil KeyID = %q, want empty", got)
	}
	if _, _, err := nilKEK.Wrap(context.Background(), make([]byte, 32)); err == nil {
		t.Fatal("nil Wrap expected error")
	}
	if _, err := nilKEK.Unwrap(context.Background(), "key", []byte("wrapped")); err == nil {
		t.Fatal("nil Unwrap expected error")
	}

	zero := &KEK{}
	if _, _, err := zero.Wrap(context.Background(), make([]byte, 32)); err == nil {
		t.Fatal("zero Wrap expected error")
	}
	if _, err := zero.Unwrap(context.Background(), "key", []byte("wrapped")); err == nil {
		t.Fatal("zero Unwrap expected error")
	}
}

func TestKEKRejectsNilContextAndBadUnwrapInputs(t *testing.T) {
	k, err := New(&fakeKeyClient{}, Config{KeyName: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := nilContextForTest()
	if _, _, err := k.Wrap(ctx, make([]byte, 32)); err == nil {
		t.Fatal("Wrap nil context expected error")
	}
	if _, err := k.Unwrap(ctx, "azurekeyvault://keys/key/1", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap nil context expected error")
	}
	if _, err := k.Unwrap(context.Background(), "", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap empty keyID expected error")
	}
	if _, err := k.Unwrap(context.Background(), "azurekeyvault://keys/other/1", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap unknown keyID expected error")
	}
	if _, err := k.Unwrap(context.Background(), "azurekeyvault://keys/key", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap versionless keyID expected error")
	}
	if _, err := k.Unwrap(context.Background(), "azurekeyvault://keys/key/1", nil); err == nil {
		t.Fatal("Unwrap empty wrapped DEK expected error")
	}
}

func TestKEKRejectsEnvelopeKeyIDFromUnexpectedVault(t *testing.T) {
	k, err := New(&fakeKeyClient{}, Config{
		KeyID: "https://expected.vault.azure.net/keys/key",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := k.Unwrap(context.Background(), "https://other.vault.azure.net/keys/key/1", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap mismatched vault host expected error")
	}
	if _, err := k.Unwrap(context.Background(), "azurekeyvault://keys/key/1", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap fallback keyID with configured Azure KeyID expected error")
	}
}

func TestWrapUnwrapRoundTripUsesVersionQualifiedKID(t *testing.T) {
	fake := &fakeKeyClient{}
	k, err := New(fake, Config{
		KeyName:    "wrap-key",
		KeyVersion: "primary",
		Algorithm:  azkeys.EncryptionAlgorithmA256KW,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	dek := []byte("0123456789abcdef0123456789abcdef")
	keyID, wrapped, err := k.Wrap(context.Background(), dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if keyID != "https://vault.vault.azure.net/keys/wrap-key/v1" {
		t.Fatalf("keyID = %q", keyID)
	}
	if !bytes.Equal(fake.wrapValue, dek) {
		t.Fatal("Wrap did not pass original DEK")
	}
	if fake.wrapName != "wrap-key" || fake.wrapVersion != "primary" {
		t.Fatalf("wrap target = %q/%q", fake.wrapName, fake.wrapVersion)
	}
	if fake.wrapAlgorithm != azkeys.EncryptionAlgorithmA256KW {
		t.Fatalf("wrap algorithm = %q", fake.wrapAlgorithm)
	}

	unwrapped, err := k.Unwrap(context.Background(), keyID, wrapped)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(unwrapped, dek) {
		t.Fatalf("Unwrap = %q, want original DEK", unwrapped)
	}
	if fake.unwrapName != "wrap-key" || fake.unwrapVersion != "v1" {
		t.Fatalf("unwrap target = %q/%q", fake.unwrapName, fake.unwrapVersion)
	}
}

func TestWrapAndUnwrapSurfaceProviderErrors(t *testing.T) {
	boom := errors.New("provider boom")
	fake := &fakeKeyClient{err: boom}
	k, err := New(fake, Config{KeyName: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, _, err := k.Wrap(context.Background(), make([]byte, 32)); !errors.Is(err, boom) {
		t.Fatalf("Wrap error = %v, want provider boom", err)
	}
	if _, err := k.Unwrap(context.Background(), "azurekeyvault://keys/key/1", []byte("wrapped")); !errors.Is(err, boom) {
		t.Fatalf("Unwrap error = %v, want provider boom", err)
	}
}

func TestWrapRejectsIncompleteProviderResponses(t *testing.T) {
	k, err := New(&fakeKeyClient{missingKID: true}, Config{KeyName: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, err := k.Wrap(context.Background(), make([]byte, 32)); err == nil {
		t.Fatal("Wrap missing KID expected error")
	}

	k, err = New(&fakeKeyClient{missingResult: true}, Config{KeyName: "key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, err := k.Wrap(context.Background(), make([]byte, 32)); err == nil {
		t.Fatal("Wrap missing result expected error")
	}
}

func nilContextForTest() context.Context { return nil }

type fakeKeyClient struct {
	err           error
	missingKID    bool
	missingResult bool

	wrapName      string
	wrapVersion   string
	wrapAlgorithm azkeys.EncryptionAlgorithm
	wrapValue     []byte

	unwrapName    string
	unwrapVersion string
}

func (f *fakeKeyClient) WrapKey(_ context.Context, name string, version string, parameters azkeys.KeyOperationParameters, _ *azkeys.WrapKeyOptions) (azkeys.WrapKeyResponse, error) {
	if f.err != nil {
		return azkeys.WrapKeyResponse{}, f.err
	}
	f.wrapName = name
	f.wrapVersion = version
	if parameters.Algorithm != nil {
		f.wrapAlgorithm = *parameters.Algorithm
	}
	f.wrapValue = append([]byte(nil), parameters.Value...)

	resp := azkeys.WrapKeyResponse{}
	if !f.missingKID {
		kid := azkeys.ID("https://vault.vault.azure.net/keys/" + name + "/v1")
		resp.KID = &kid
	}
	if !f.missingResult {
		resp.Result = append([]byte("wrapped:"), parameters.Value...)
	}
	return resp, nil
}

func (f *fakeKeyClient) UnwrapKey(_ context.Context, name string, version string, parameters azkeys.KeyOperationParameters, _ *azkeys.UnwrapKeyOptions) (azkeys.UnwrapKeyResponse, error) {
	if f.err != nil {
		return azkeys.UnwrapKeyResponse{}, f.err
	}
	f.unwrapName = name
	f.unwrapVersion = version

	resp := azkeys.UnwrapKeyResponse{}
	resp.Result = bytes.TrimPrefix(parameters.Value, []byte("wrapped:"))
	return resp, nil
}
