package awskms

import (
	"errors"

	"github.com/aws/smithy-go"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// classifyAWSError translates a raw AWS KMS error into an apperror that
// expresses the operational intent: throttling and KMSInternalException are
// retryable dependency failures (mapped to 502/Retry-After), while
// disabled / unavailable / deleted key states and access errors are
// permanent failures that retrying cannot fix.
//
// The original AWS error is preserved as the wrapped cause so operators
// can still inspect the raw smithy.APIError code/message in logs.
// Unknown error codes are returned unchanged so existing fmt.Errorf("%w")
// wrapping at the call site continues to work.
func (k *KEK) classifyAWSError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		k.metrics.recordError(operation, "unknown")
		return err
	}
	code := apiErr.ErrorCode()
	k.metrics.recordError(operation, code)
	switch code {
	case "ThrottlingException", "RequestThrottled", "Throttling", "ThrottledException":
		return apperror.NewDependencyUnavailable("kms", "kms throttled: "+code, err)
	case "KMSInternalException":
		return apperror.NewDependencyUnavailable("kms", "kms internal error: "+code, err)
	case "KeyUnavailableException", "DisabledException", "KMSInvalidStateException", "KeyDeletionException", "KeyDeletedException":
		return apperror.NewPermanentWithCause("kms key not usable: "+code, err)
	case "AccessDeniedException", "NotFoundException":
		return apperror.NewPermanentWithCause("kms access denied or key not found: "+code, err)
	default:
		return err
	}
}
