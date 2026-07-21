package gcpkms

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// classifyGCPError translates a raw GCP KMS error into an apperror that
// expresses the operational intent. The mapping mirrors awskms's
// classifyAWSError so callers using apperror.IsUnavailable / IsPermanent
// get consistent semantics regardless of which KMS adapter is wired.
//
// The original gRPC error is preserved as the wrapped cause so operators
// can still inspect the raw code/message in logs. Errors that aren't gRPC
// status errors, or that map to codes outside the curated set, are
// returned unchanged. Every classified (and unknown) error is recorded on
// the KEK's metrics counter under the gRPC status code name.
func (k *KEK) classifyGCPError(operation string, err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		if k != nil {
			k.metrics.recordError(operation, "unknown")
		}
		return err
	}
	code := st.Code().String()
	if k != nil {
		k.metrics.recordError(operation, code)
	}
	switch st.Code() {
	case codes.ResourceExhausted, codes.Unavailable, codes.DeadlineExceeded, codes.Aborted:
		// Throttling / outage / quota / transient backend trouble — retryable.
		return apperror.NewDependencyUnavailable("kms", "gcpkms transient: "+code, err)
	case codes.Internal:
		// GCP internal — operationally similar to AWS's KMSInternalException.
		return apperror.NewDependencyUnavailable("kms", "gcpkms internal: "+code, err)
	case codes.PermissionDenied, codes.Unauthenticated, codes.NotFound, codes.FailedPrecondition:
		// AccessDenied / KeyNotReady / KeyDisabled / Deleted analogues.
		// FailedPrecondition is GCP's "key in wrong state" code.
		return apperror.NewPermanentWithCause("gcpkms key not usable: "+code, err)
	default:
		return err
	}
}

// classifyGCPError is retained as a package-level helper for tests that
// exercise the mapping without a full KEK. Production call sites use the
// method form so metrics are recorded.
func classifyGCPError(operation string, err error) error {
	return (*KEK)(nil).classifyGCPError(operation, err)
}
