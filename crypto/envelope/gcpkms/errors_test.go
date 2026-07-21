package gcpkms

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

func TestClassifyGCPError(t *testing.T) {
	for _, code := range []codes.Code{codes.ResourceExhausted, codes.Unavailable, codes.DeadlineExceeded, codes.Aborted, codes.Internal} {
		if err := classifyGCPError("wrap", status.Error(code, "test")); !apperror.IsUnavailable(err) {
			t.Fatalf("code %s = %v, want unavailable", code, err)
		}
	}
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated, codes.NotFound, codes.FailedPrecondition} {
		if err := classifyGCPError("unwrap", status.Error(code, "test")); !apperror.IsPermanent(err) {
			t.Fatalf("code %s = %v, want permanent", code, err)
		}
	}
	raw := status.Error(codes.InvalidArgument, "bad request")
	if got := classifyGCPError("wrap", raw); got != raw {
		t.Fatalf("unclassified code must pass through, got %v", got)
	}
	plain := errors.New("dial failed")
	if got := classifyGCPError("wrap", plain); got != plain {
		t.Fatalf("non-status error must pass through, got %v", got)
	}
	if got := classifyGCPError("wrap", nil); got != nil {
		t.Fatalf("nil = %v, want nil", got)
	}
}
