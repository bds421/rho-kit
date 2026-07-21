package azurekeyvault

import (
	"errors"
	"strconv"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// classifyAzureError translates a raw Azure Key Vault error into an
// apperror that expresses the operational intent. The mapping mirrors
// awskms's classifyAWSError so callers using apperror.IsUnavailable /
// IsPermanent get consistent semantics across KMS adapters.
//
// Azure surfaces errors as *azcore.ResponseError carrying an HTTP status
// code plus an "ErrorCode" string. Status codes give us the throttle /
// outage classification; ErrorCodes such as "KeyNotFound" / "Forbidden"
// give us the permanent-key signal.
func (k *KEK) classifyAzureError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) {
		if k != nil {
			k.metrics.recordError(operation, "unknown")
		}
		return err
	}
	if k != nil {
		k.metrics.recordError(operation, strconv.Itoa(respErr.StatusCode))
	}
	switch respErr.StatusCode {
	case 429:
		return apperror.NewDependencyUnavailable("kms", "azurekeyvault throttled (429)", err)
	case 408, 500, 502, 503, 504:
		return apperror.NewDependencyUnavailable("kms", "azurekeyvault transient ("+respErr.ErrorCode+")", err)
	case 401, 403:
		return apperror.NewPermanentWithCause("azurekeyvault access denied ("+respErr.ErrorCode+")", err)
	case 404:
		return apperror.NewPermanentWithCause("azurekeyvault key not found ("+respErr.ErrorCode+")", err)
	case 409:
		// Common when key is disabled / version mismatch / state conflict.
		return apperror.NewPermanentWithCause("azurekeyvault key not usable ("+respErr.ErrorCode+")", err)
	default:
		return err
	}
}

// classifyAzureError is retained for focused classification tests. Production
// calls the KEK method so every provider error is counted.
func classifyAzureError(operation string, err error) error {
	return (*KEK)(nil).classifyAzureError(operation, err)
}
