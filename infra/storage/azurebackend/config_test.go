package azurebackend

import (
	"strings"
	"testing"
)

func TestAzureConfigValidate_Endpoint(t *testing.T) {
	t.Parallel()

	base := AzureConfig{
		AccountName:   "account",
		AccountKey:    "S3cur3-Azure-Account-Key-Value-123456",
		ContainerName: "container",
	}

	tests := []struct {
		name    string
		mutate  func(*AzureConfig)
		wantErr bool
	}{
		{
			name: "https endpoint",
			mutate: func(cfg *AzureConfig) {
				cfg.Endpoint = "https://account.blob.core.windows.net"
			},
		},
		{
			name: "http endpoint requires opt-in",
			mutate: func(cfg *AzureConfig) {
				cfg.Endpoint = "http://127.0.0.1:10000/account"
			},
			wantErr: true,
		},
		{
			name: "http endpoint with opt-in",
			mutate: func(cfg *AzureConfig) {
				cfg.Endpoint = "http://127.0.0.1:10000/account"
				cfg.AllowInsecureEndpoint = true
			},
		},
		{
			name: "endpoint fragment rejected",
			mutate: func(cfg *AzureConfig) {
				cfg.Endpoint = "https://account.blob.core.windows.net#frag"
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tt.mutate(&cfg)
			err := cfg.Validate("production")
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAzureConfigLogValueRedactsEndpointSecrets(t *testing.T) {
	t.Parallel()

	cfg := AzureConfig{
		AccountName:   "tenantprodaccount",
		AccountKey:    "azure-secret-value",
		ContainerName: "tenant-prod-container",
		Endpoint:      "https://token-user:endpoint-secret@account.blob.core.windows.net?token=query-secret#frag",
	}

	rendered := cfg.LogValue().String()
	for _, secret := range []string{
		"tenantprodaccount",
		"tenant-prod-container",
		"account.blob.core.windows.net",
		"token-user",
		"endpoint-secret",
		"query-secret",
		"azure-secret-value",
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q: %s", secret, rendered)
		}
	}
	for _, marker := range []string{
		"account_name_configured=true",
		"account_key_configured=true",
		"container_name_configured=true",
		"endpoint_configured=true",
	} {
		if !strings.Contains(rendered, marker) {
			t.Fatalf("LogValue missing marker %q: %s", marker, rendered)
		}
	}
}

func TestNewRejectsInsecureEndpointWithoutOptIn(t *testing.T) {
	t.Parallel()

	_, err := New(AzureConfig{
		AccountName:   "account",
		AccountKey:    "S3cur3-Azure-Account-Key-Value-123456",
		ContainerName: "container",
		Endpoint:      "http://127.0.0.1:10000/account",
	})
	if err == nil {
		t.Fatal("expected insecure endpoint error, got nil")
	}
}
