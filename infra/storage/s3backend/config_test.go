package s3backend

import (
	"strings"
	"testing"
)

func TestS3ConfigValidate_Endpoint(t *testing.T) {
	t.Parallel()

	base := S3Config{
		Region:          "eu-central-1",
		Bucket:          "bucket",
		AccessKeyID:     "access-key",
		SecretAccessKey: "S3cur3-S3-Secret-Key-Value-123456",
	}

	tests := []struct {
		name    string
		mutate  func(*S3Config)
		wantErr bool
	}{
		{
			name: "https endpoint",
			mutate: func(cfg *S3Config) {
				cfg.Endpoint = "https://s3.example.com"
			},
		},
		{
			name: "http endpoint requires opt-in",
			mutate: func(cfg *S3Config) {
				cfg.Endpoint = "http://localhost:9000"
			},
			wantErr: true,
		},
		{
			name: "http endpoint with opt-in",
			mutate: func(cfg *S3Config) {
				cfg.Endpoint = "http://localhost:9000"
				cfg.AllowInsecureEndpoint = true
			},
		},
		{
			name: "endpoint credentials rejected",
			mutate: func(cfg *S3Config) {
				cfg.Endpoint = "https://user:pass@s3.example.com"
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

func TestS3ConfigLogValueRedactsResourceHandlesAndEndpointSecrets(t *testing.T) {
	t.Parallel()

	cfg := S3Config{
		Region:          "eu-central-1",
		Bucket:          "tenant-prod-bucket",
		Endpoint:        "https://token-user:endpoint-secret@s3.example.com?token=query-secret#frag",
		URLTemplate:     "https://cdn.example.com/tenant-prod-bucket/",
		AccessKeyID:     "access-key-secret-handle",
		SecretAccessKey: "storage-secret-value",
		SSE:             "aws:kms",
		SSEKMSKeyID:     "arn:aws:kms:eu-central-1:123456789012:key/tenant-prod",
	}

	rendered := cfg.LogValue().String()
	for _, secret := range []string{
		"tenant-prod-bucket",
		"s3.example.com",
		"cdn.example.com",
		"token-user",
		"endpoint-secret",
		"query-secret",
		"access-key-secret-handle",
		"storage-secret-value",
		"tenant-prod",
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q: %s", secret, rendered)
		}
	}
	for _, marker := range []string{
		"bucket_configured=true",
		"endpoint_configured=true",
		"url_template_configured=true",
		"access_key_id_configured=true",
		"secret_access_key_configured=true",
		"sse_kms_key_configured=true",
	} {
		if !strings.Contains(rendered, marker) {
			t.Fatalf("LogValue missing marker %q: %s", marker, rendered)
		}
	}
}

func TestS3ConfigValidate_URLTemplate(t *testing.T) {
	t.Parallel()

	base := S3Config{
		Region:          "eu-central-1",
		Bucket:          "bucket",
		AccessKeyID:     "access-key",
		SecretAccessKey: "S3cur3-S3-Secret-Key-Value-123456",
	}

	tests := []struct {
		name    string
		tpl     string
		wantErr bool
	}{
		{"empty", "", false},
		{"bucket and region", "https://{bucket}.s3.{region}.example.com", false},
		{"path prefix", "https://cdn.example.com/{bucket}", false},
		{"http rejected", "http://cdn.example.com/{bucket}", true},
		{"credentials rejected", "https://user:pass@cdn.example.com/{bucket}", true},
		{"query rejected", "https://cdn.example.com/{bucket}?token=abc", true},
		{"unknown placeholder rejected", "https://{tenant}.cdn.example.com/{bucket}", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			cfg.URLTemplate = tt.tpl
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

func TestS3ConfigValidate_SSE(t *testing.T) {
	t.Parallel()

	base := S3Config{
		Region:          "eu-central-1",
		Bucket:          "bucket",
		AccessKeyID:     "access-key",
		SecretAccessKey: "S3cur3-S3-Secret-Key-Value-123456",
	}

	tests := []struct {
		name    string
		mutate  func(*S3Config)
		wantErr bool
	}{
		{
			name: "empty explicitly opts out",
		},
		{
			name: "AES256 accepted",
			mutate: func(cfg *S3Config) {
				cfg.SSE = "AES256"
			},
		},
		{
			name: "KMS accepted with key",
			mutate: func(cfg *S3Config) {
				cfg.SSE = "aws:kms"
				cfg.SSEKMSKeyID = "arn:aws:kms:eu-central-1:123456789012:key/abc"
			},
		},
		{
			name: "unknown value rejected",
			mutate: func(cfg *S3Config) {
				cfg.SSE = "AES-256"
			},
			wantErr: true,
		},
		{
			name: "KMS requires key",
			mutate: func(cfg *S3Config) {
				cfg.SSE = "aws:kms"
			},
			wantErr: true,
		},
		{
			name: "KMS key requires KMS mode",
			mutate: func(cfg *S3Config) {
				cfg.SSE = "AES256"
				cfg.SSEKMSKeyID = "arn:aws:kms:eu-central-1:123456789012:key/abc"
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
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

func TestNewRejectsInsecureEndpointWithoutOptIn(t *testing.T) {
	t.Parallel()

	_, err := New(S3Config{
		Region:          "eu-central-1",
		Bucket:          "bucket",
		AccessKeyID:     "access-key",
		SecretAccessKey: "S3cur3-S3-Secret-Key-Value-123456",
		Endpoint:        "http://localhost:9000",
	})
	if err == nil {
		t.Fatal("expected insecure endpoint error, got nil")
	}
}

func TestNewRejectsInvalidSSEConfig(t *testing.T) {
	t.Parallel()

	_, err := New(S3Config{
		Region:          "eu-central-1",
		Bucket:          "bucket",
		AccessKeyID:     "access-key",
		SecretAccessKey: "S3cur3-S3-Secret-Key-Value-123456",
		SSE:             "AES-256",
	})
	if err == nil {
		t.Fatal("expected invalid SSE error, got nil")
	}
}
