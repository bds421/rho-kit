package vaulttransit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
)

var _ slog.LogValuer = Config{}

func TestConfigLogValueRedactsVaultTopologyAndContext(t *testing.T) {
	cfg := Config{
		MountPath:  "team-secret/transit",
		KeyName:    "billing-dek",
		Context:    []byte("tenant=tenant-secret"),
		KeyVersion: 7,
	}

	rendered := cfg.LogValue().String()

	for _, secret := range []string{"team-secret", "billing-dek", "tenant-secret"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q in %q", secret, rendered)
		}
	}
	for _, expected := range []string{
		"mount_path_configured=true",
		"key_name_configured=true",
		"context_configured=true",
		"key_version_pinned=true",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("LogValue %q missing %q", rendered, expected)
		}
	}
}

func TestNewDefaultsAndCopiesContext(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("unexpected Vault request")
	}))
	aad := []byte("tenant=acme")
	k, err := New(client, Config{
		KeyName: "orders-dek",
		Context: aad,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	aad[0] = 'T'

	if got, want := k.mountPath, defaultMountPath; got != want {
		t.Fatalf("mountPath = %q, want %q", got, want)
	}
	if got, want := k.KeyID(), "vault://transit/keys/orders-dek"; got != want {
		t.Fatalf("KeyID = %q, want %q", got, want)
	}
	if got := string(k.contextAAD); got != "tenant=acme" {
		t.Fatalf("contextAAD = %q, want copied original", got)
	}
}

func TestNewRejectsUnsafePaths(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("unexpected Vault request")
	}))
	cases := []Config{
		{KeyName: ""},
		{KeyName: "a/b"},
		{MountPath: "/transit", KeyName: "orders"},
		{MountPath: "../transit", KeyName: "orders"},
		{MountPath: "transit//prod", KeyName: "orders"},
		{MountPath: "transit", KeyName: ".."},
		{MountPath: "transit", KeyName: "bad\nkey"},
		{MountPath: "transit", KeyName: "orders", KeyVersion: -1},
	}

	for _, cfg := range cases {
		if _, err := New(client, cfg); err == nil {
			t.Fatalf("New(%+v) expected error", cfg)
		}
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

func TestKEKRejectsNilContextAndUnknownKey(t *testing.T) {
	k, err := New(newTestClient(t, http.NotFoundHandler()), Config{KeyName: "orders"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := nilContextForTest()
	if _, _, err := k.Wrap(ctx, make([]byte, 32)); err == nil {
		t.Fatal("Wrap nil context expected error")
	}
	if _, err := k.Unwrap(ctx, k.KeyID(), []byte("wrapped")); err == nil {
		t.Fatal("Unwrap nil context expected error")
	}
	if _, err := k.Unwrap(context.Background(), "", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap empty keyID expected error")
	}
	if _, err := k.Unwrap(context.Background(), "vault://transit/keys/other", []byte("wrapped")); err == nil {
		t.Fatal("Unwrap unknown keyID expected error")
	}
	if _, err := k.Unwrap(context.Background(), k.KeyID(), nil); err == nil {
		t.Fatal("Unwrap empty wrapped DEK expected error")
	}
}

func TestEnvelopeRoundTripThroughVaultTransit(t *testing.T) {
	client := newTestClient(t, newTransitHandler(t, "tenant=acme"))
	k, err := New(client, Config{
		MountPath:  "platform/transit",
		KeyName:    "orders",
		Context:    []byte("tenant=acme"),
		KeyVersion: 3,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	enc := envelope.New(k)
	blob, err := enc.Encrypt(context.Background(), []byte("secret payload"), []byte("row=42"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	plaintext, err := enc.Decrypt(context.Background(), blob, []byte("row=42"))
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got, want := string(plaintext), "secret payload"; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}
}

func newTransitHandler(t *testing.T, expectedContext string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/platform/transit/encrypt/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("encrypt method = %s, want PUT", r.Method)
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode encrypt request: %v", err)
		}
		if got := decodeTransitField(t, req, "context"); got != expectedContext {
			t.Fatalf("encrypt context = %q, want %q", got, expectedContext)
		}
		if got := req["key_version"]; got != float64(3) {
			t.Fatalf("key_version = %#v, want 3", got)
		}
		plaintext := decodeTransitField(t, req, "plaintext")
		ciphertext := "vault:v3:" + base64.StdEncoding.EncodeToString([]byte(plaintext))
		writeVaultData(t, w, map[string]interface{}{"ciphertext": ciphertext})
	})
	mux.HandleFunc("/v1/platform/transit/decrypt/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("decrypt method = %s, want PUT", r.Method)
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode decrypt request: %v", err)
		}
		if got := decodeTransitField(t, req, "context"); got != expectedContext {
			t.Fatalf("decrypt context = %q, want %q", got, expectedContext)
		}
		ciphertext, ok := req["ciphertext"].(string)
		if !ok || !strings.HasPrefix(ciphertext, "vault:v3:") {
			t.Fatalf("ciphertext = %#v, want vault:v3 prefix", req["ciphertext"])
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "vault:v3:"))
		if err != nil {
			t.Fatalf("decode fake ciphertext: %v", err)
		}
		writeVaultData(t, w, map[string]interface{}{
			"plaintext": base64.StdEncoding.EncodeToString(raw),
		})
	})
	return mux
}

func decodeTransitField(t *testing.T, req map[string]interface{}, field string) string {
	t.Helper()
	value, ok := req[field].(string)
	if !ok || value == "" {
		t.Fatalf("%s = %#v, want non-empty string", field, req[field])
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode %s: %v", field, err)
	}
	return string(decoded)
}

func writeVaultData(t *testing.T, w http.ResponseWriter, data map[string]interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"data": data}); err != nil {
		t.Fatalf("write response: %v", err)
	}
}

func newTestClient(t *testing.T, handler http.Handler) *vaultapi.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := vaultapi.DefaultConfig()
	cfg.Address = srv.URL
	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.SetToken("test-token")
	return client
}

func nilContextForTest() context.Context { return nil }
