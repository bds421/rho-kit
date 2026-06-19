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
	client := newTestClient(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// t.Error (not t.Fatal) from the server goroutine: t.FailNow is only
		// valid from the test goroutine. NewKEK must not touch the network, so
		// any request here is a failure recorded for the test goroutine.
		t.Error("unexpected Vault request during NewKEK")
	}))
	aad := []byte("tenant=acme")
	k, err := NewKEK(client, Config{
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
	client := newTestClient(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// t.Error (not t.Fatal) from the server goroutine; see
		// TestNewDefaultsAndCopiesContext.
		t.Error("unexpected Vault request during NewKEK")
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
		if _, err := NewKEK(client, cfg); err == nil {
			t.Fatalf("NewKEK(%+v) expected error", cfg)
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
	k, err := NewKEK(newTestClient(t, http.NotFoundHandler()), Config{KeyName: "orders"})
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
	k, err := NewKEK(client, Config{
		MountPath:  "platform/transit",
		KeyName:    "orders",
		Context:    []byte("tenant=acme"),
		KeyVersion: 3,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	enc := envelope.NewEncryptor(k)
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

// newTransitHandler returns a fake Vault Transit mux. It runs on httptest
// server goroutines, so it records assertion failures with t.Error* (never
// t.Fatal*): testing.T.FailNow is only valid from the test goroutine, and
// calling it here would runtime.Goexit the server goroutine mid-response. On
// any failure the handler returns early without writing a body so the client
// observes a clean error instead of partial data.
func newTransitHandler(t *testing.T, expectedContext string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/platform/transit/encrypt/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("encrypt method = %s, want PUT", r.Method)
			return
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode encrypt request: %v", err)
			return
		}
		got, ok := decodeTransitField(t, req, "context")
		if !ok {
			return
		}
		if got != expectedContext {
			t.Errorf("encrypt context = %q, want %q", got, expectedContext)
			return
		}
		if got := req["key_version"]; got != float64(3) {
			t.Errorf("key_version = %#v, want 3", got)
			return
		}
		plaintext, ok := decodeTransitField(t, req, "plaintext")
		if !ok {
			return
		}
		ciphertext := "vault:v3:" + base64.StdEncoding.EncodeToString([]byte(plaintext))
		writeVaultData(t, w, map[string]interface{}{"ciphertext": ciphertext})
	})
	mux.HandleFunc("/v1/platform/transit/decrypt/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("decrypt method = %s, want PUT", r.Method)
			return
		}
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode decrypt request: %v", err)
			return
		}
		got, ok := decodeTransitField(t, req, "context")
		if !ok {
			return
		}
		if got != expectedContext {
			t.Errorf("decrypt context = %q, want %q", got, expectedContext)
			return
		}
		ciphertext, ok := req["ciphertext"].(string)
		if !ok || !strings.HasPrefix(ciphertext, "vault:v3:") {
			t.Errorf("ciphertext = %#v, want vault:v3 prefix", req["ciphertext"])
			return
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "vault:v3:"))
		if err != nil {
			t.Errorf("decode fake ciphertext: %v", err)
			return
		}
		writeVaultData(t, w, map[string]interface{}{
			"plaintext": base64.StdEncoding.EncodeToString(raw),
		})
	})
	return mux
}

// decodeTransitField reads a base64 Transit field. It is called from server
// goroutines, so it reports failures via t.Error* and signals success through
// the boolean rather than aborting with t.Fatal*.
func decodeTransitField(t *testing.T, req map[string]interface{}, field string) (string, bool) {
	t.Helper()
	value, ok := req[field].(string)
	if !ok || value == "" {
		t.Errorf("%s = %#v, want non-empty string", field, req[field])
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Errorf("decode %s: %v", field, err)
		return "", false
	}
	return string(decoded), true
}

// writeVaultData encodes a Vault-style {"data": ...} response. It runs on a
// server goroutine, so an encode failure is reported via t.Error*.
func writeVaultData(t *testing.T, w http.ResponseWriter, data map[string]interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{"data": data}); err != nil {
		t.Errorf("write response: %v", err)
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
