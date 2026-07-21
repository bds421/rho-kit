package gcsbackend

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/api/option"
)

func TestConfigValidate_Endpoint(t *testing.T) {
	t.Parallel()

	base := Config{
		Bucket:    "bucket",
		ProjectID: "project",
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name: "https endpoint",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "https://storage.example.com/storage/v1"
			},
		},
		{
			name: "http endpoint requires opt-in",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "http://localhost:4443/storage/v1"
			},
			wantErr: true,
		},
		{
			name: "http endpoint with opt-in",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "http://localhost:4443/storage/v1"
				cfg.AllowInsecureEndpoint = true
			},
		},
		{
			name: "endpoint query rejected",
			mutate: func(cfg *Config) {
				cfg.Endpoint = "https://storage.example.com/storage/v1?token=abc"
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfigLogValueDoesNotExposeCredentialsFile(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Bucket:          "tenant-prod-bucket",
		ProjectID:       "customer-prod-project",
		CredentialsFile: "/var/run/secrets/gcp/service-account.json",
		ClientOptions:   []option.ClientOption{option.WithoutAuthentication()},
		Endpoint:        "https://token-user:endpoint-secret@storage.example.com/storage/v1?token=query-secret#frag",
	}

	rendered := cfg.LogValue().String()
	for _, secret := range []string{
		"tenant-prod-bucket",
		"customer-prod-project",
		cfg.CredentialsFile,
		"storage.example.com",
		"token-user",
		"endpoint-secret",
		"query-secret",
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue exposed secret %q: %s", secret, rendered)
		}
	}
	if !strings.Contains(rendered, "bucket_configured=true") {
		t.Fatalf("LogValue did not report configured bucket: %s", rendered)
	}
	if !strings.Contains(rendered, "project_id_configured=true") {
		t.Fatalf("LogValue did not report configured project: %s", rendered)
	}
	if !strings.Contains(rendered, "credentials_file_configured=true") {
		t.Fatalf("LogValue did not report configured credentials file: %s", rendered)
	}
	if !strings.Contains(rendered, "client_options_configured=true") {
		t.Fatalf("LogValue did not report configured client options: %s", rendered)
	}
	if !strings.Contains(rendered, "endpoint_configured=true") {
		t.Fatalf("LogValue did not report configured endpoint: %s", rendered)
	}
}

func TestNewRejectsInsecureEndpointWithoutOptIn(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), Config{
		Bucket:    "bucket",
		ProjectID: "project",
		Endpoint:  "http://localhost:4443/storage/v1",
	})
	if err == nil {
		t.Fatal("expected insecure endpoint error, got nil")
	}
}

func TestConfigValidate_ProjectIDOptional(t *testing.T) {
	t.Parallel()
	cfg := Config{Bucket: "bucket"} // ProjectID empty
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty ProjectID must be allowed: %v", err)
	}
}
