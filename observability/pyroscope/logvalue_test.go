package pyroscope_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/observability/pyroscope/v2"
)

// TestConfig_LogValue_RedactsAuthToken is the regression pin for review
// MEDIUM: Config.LogValue must never emit AuthToken (or TenantID) as a
// string attribute — only booleans that the secret is configured.
func TestConfig_LogValue_RedactsAuthToken(t *testing.T) {
	const secret = "super-secret-bearer-token-xyz"
	cfg := pyroscope.Config{
		ServerAddress: "https://profiles.example.com",
		AppName:       "svc",
		AuthToken:     secret,
		TenantID:      "tenant-secret-id",
		UploadRate:    time.Second,
		Tags:          map[string]string{"env": "test"},
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("cfg", slog.Any("config", cfg))

	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("LogValue leaked AuthToken into log output: %s", out)
	}
	if strings.Contains(out, "tenant-secret-id") {
		t.Fatalf("LogValue leaked TenantID into log output: %s", out)
	}

	v := cfg.LogValue()
	attrs := v.Group()
	var sawAuth, sawTenant bool
	for _, a := range attrs {
		switch a.Key {
		case "auth_token_configured":
			sawAuth = true
			if !a.Value.Bool() {
				t.Fatal("auth_token_configured should be true when AuthToken set")
			}
		case "tenant_id_configured":
			sawTenant = true
			if !a.Value.Bool() {
				t.Fatal("tenant_id_configured should be true when TenantID set")
			}
		case "auth_token", "AuthToken", "tenant_id", "TenantID":
			t.Fatalf("must not expose raw secret attr %q", a.Key)
		}
	}
	if !sawAuth || !sawTenant {
		t.Fatalf("expected auth_token_configured and tenant_id_configured attrs, got %#v", attrs)
	}
}
