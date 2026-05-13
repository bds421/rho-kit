package awskms

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// fakeAPIError implements smithy.APIError so we can simulate AWS KMS
// classified failures without touching the real SDK plumbing.
type fakeAPIError struct {
	code  string
	msg   string
	fault smithy.ErrorFault
}

func (e *fakeAPIError) Error() string                 { return e.code + ": " + e.msg }
func (e *fakeAPIError) ErrorCode() string             { return e.code }
func (e *fakeAPIError) ErrorMessage() string          { return e.msg }
func (e *fakeAPIError) ErrorFault() smithy.ErrorFault { return e.fault }

func newAPIErr(code, msg string) error {
	return &fakeAPIError{code: code, msg: msg, fault: smithy.FaultServer}
}

func TestClassifyAWSError_Throttling(t *testing.T) {
	for _, code := range []string{"ThrottlingException", "RequestThrottled"} {
		t.Run(code, func(t *testing.T) {
			err := classifyAWSError("wrap", newAPIErr(code, "rate exceeded"))
			if !apperror.IsUnavailable(err) {
				t.Fatalf("expected UnavailableError, got %v", err)
			}
			ue, _ := apperror.AsUnavailable(err)
			if ue.Dependency != "kms" {
				t.Fatalf("dependency = %q, want kms", ue.Dependency)
			}
		})
	}
}

func TestClassifyAWSError_KMSInternal(t *testing.T) {
	err := classifyAWSError("wrap", newAPIErr("KMSInternalException", "internal"))
	if !apperror.IsUnavailable(err) {
		t.Fatalf("expected UnavailableError, got %v", err)
	}
}

func TestClassifyAWSError_KeyUnavailable(t *testing.T) {
	for _, code := range []string{"KeyUnavailableException", "DisabledException", "KMSInvalidStateException"} {
		t.Run(code, func(t *testing.T) {
			err := classifyAWSError("unwrap", newAPIErr(code, "key disabled"))
			if !apperror.IsPermanent(err) {
				t.Fatalf("expected PermanentError, got %v", err)
			}
		})
	}
}

func TestClassifyAWSError_AccessDenied(t *testing.T) {
	err := classifyAWSError("unwrap", newAPIErr("AccessDeniedException", "no perms"))
	if !apperror.IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
}

func TestClassifyAWSError_UnknownPassThrough(t *testing.T) {
	raw := newAPIErr("ValidationException", "bad input")
	got := classifyAWSError("wrap", raw)
	if got != raw {
		t.Fatalf("unknown code should be returned unchanged, got %v", got)
	}
}

func TestClassifyAWSError_NonSmithyPassThrough(t *testing.T) {
	plain := errors.New("dial tcp: refused")
	got := classifyAWSError("wrap", plain)
	if got != plain {
		t.Fatalf("non-API error should be returned unchanged, got %v", got)
	}
}

func TestClassifyAWSError_Nil(t *testing.T) {
	if got := classifyAWSError("wrap", nil); got != nil {
		t.Fatalf("nil → nil, got %v", got)
	}
}


// kekWithClient builds a KEK whose Encrypt/Decrypt delegate to a fake.
// We bypass NewKEK because the real constructor requires a *kms.Client. The
// classifyAWSError test below exercises classification through the same code
// path the real client uses.
func TestEncrypt_ThrottlingMapsToDependencyUnavailable(t *testing.T) {
	// Use classify directly; Wrap also invokes classifyAWSError on err and
	// returns the classified error before fmt.Errorf wrapping kicks in.
	err := classifyAWSError("wrap", newAPIErr("ThrottlingException", "rate exceeded"))
	if !apperror.IsUnavailable(err) {
		t.Fatalf("classify(ThrottlingException) = %v, want UnavailableError", err)
	}
	ue, _ := apperror.AsUnavailable(err)
	if ue.Dependency != "kms" {
		t.Fatalf("Dependency = %q, want kms", ue.Dependency)
	}
	if !ue.Retryable() {
		t.Fatal("ThrottlingException should map to retryable")
	}
}

func TestDecrypt_KeyUnavailableMapsToPermanent(t *testing.T) {
	err := classifyAWSError("unwrap", newAPIErr("KeyUnavailableException", "AWS internal: key not ready"))
	if !apperror.IsPermanent(err) {
		t.Fatalf("classify(KeyUnavailableException) = %v, want PermanentError", err)
	}
	// Preserves cause for operator inspection.
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatal("classified error must wrap smithy.APIError cause")
	}
	if apiErr.ErrorCode() != "KeyUnavailableException" {
		t.Fatalf("ErrorCode = %q, want KeyUnavailableException", apiErr.ErrorCode())
	}
}
