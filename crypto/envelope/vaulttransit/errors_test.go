package vaulttransit

import (
	"errors"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// respErr builds a *vaultapi.ResponseError carrying just the HTTP status,
// which is the only surface classifyVaultError inspects.
func respErr(status int) error {
	return &vaultapi.ResponseError{
		HTTPMethod: "PUT",
		URL:        "https://vault.example/v1/transit/encrypt/orders",
		StatusCode: status,
		Errors:     []string{"boom"},
	}
}

func TestClassifyVaultError_Throttled(t *testing.T) {
	err := classifyVaultError("encrypt", respErr(429))
	if !apperror.IsUnavailable(err) {
		t.Fatalf("status 429 = %v, want UnavailableError", err)
	}
	ue, ok := apperror.AsUnavailable(err)
	if !ok {
		t.Fatalf("AsUnavailable(%v) = false", err)
	}
	if ue.Dependency != "kms" {
		t.Fatalf("Dependency = %q, want kms", ue.Dependency)
	}
}

func TestClassifyVaultError_Transient(t *testing.T) {
	// 412/472/473 are Vault-specific HA/replication transient statuses that
	// must map to a retryable UnavailableError so caller backoff engages
	// during routine failovers. The generic 408/5xx set is also covered.
	for _, status := range []int{408, 412, 472, 473, 500, 502, 503, 504} {
		status := status
		t.Run(statusString(status), func(t *testing.T) {
			err := classifyVaultError("encrypt", respErr(status))
			if !apperror.IsUnavailable(err) {
				t.Fatalf("status %d = %v, want UnavailableError", status, err)
			}
			ue, ok := apperror.AsUnavailable(err)
			if !ok {
				t.Fatalf("AsUnavailable(status %d) = false", status)
			}
			if ue.Dependency != "kms" {
				t.Fatalf("status %d Dependency = %q, want kms", status, ue.Dependency)
			}
		})
	}
}

func TestClassifyVaultError_AccessDenied(t *testing.T) {
	for _, status := range []int{401, 403} {
		status := status
		t.Run(statusString(status), func(t *testing.T) {
			err := classifyVaultError("decrypt", respErr(status))
			if !apperror.IsPermanent(err) {
				t.Fatalf("status %d = %v, want PermanentError", status, err)
			}
			if apperror.IsUnavailable(err) {
				t.Fatalf("status %d should not be retryable/unavailable", status)
			}
		})
	}
}

func TestClassifyVaultError_NotFound(t *testing.T) {
	err := classifyVaultError("decrypt", respErr(404))
	if !apperror.IsPermanent(err) {
		t.Fatalf("status 404 = %v, want PermanentError", err)
	}
}

func TestClassifyVaultError_UnknownStatusPassThrough(t *testing.T) {
	// A 400 (e.g. malformed ciphertext) is neither transient nor an auth/
	// not-found permanent case; it must be returned unchanged so callers can
	// inspect the original *vaultapi.ResponseError.
	raw := respErr(400)
	got := classifyVaultError("encrypt", raw)
	if got != raw {
		t.Fatalf("status 400 = %v, want unchanged passthrough", got)
	}
	if apperror.IsUnavailable(got) || apperror.IsPermanent(got) {
		t.Fatalf("status 400 should not be classified, got %v", got)
	}
}

func TestClassifyVaultError_NonResponseErrorPassThrough(t *testing.T) {
	// Network-class failures arrive as plain errors, not *ResponseError, and
	// must pass through untouched (and stay errors.As-discoverable).
	plain := errors.New("dial tcp: connection refused")
	got := classifyVaultError("encrypt", plain)
	if got != plain {
		t.Fatalf("non-ResponseError = %v, want unchanged passthrough", got)
	}
}

func TestClassifyVaultError_PreservesCause(t *testing.T) {
	cause := respErr(503)
	err := classifyVaultError("encrypt", cause)
	var respErr *vaultapi.ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("classified error must wrap *ResponseError cause, got %v", err)
	}
	if respErr.StatusCode != 503 {
		t.Fatalf("wrapped StatusCode = %d, want 503", respErr.StatusCode)
	}
}

func TestClassifyVaultError_Nil(t *testing.T) {
	if got := classifyVaultError("encrypt", nil); got != nil {
		t.Fatalf("nil -> %v, want nil", got)
	}
}

func TestStatusString(t *testing.T) {
	cases := map[int]string{
		200:  "200",
		429:  "429",
		503:  "503",
		99:   "unknown",
		1000: "unknown",
	}
	for code, want := range cases {
		if got := statusString(code); got != want {
			t.Fatalf("statusString(%d) = %q, want %q", code, got, want)
		}
	}
}
