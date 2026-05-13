package vaulttransit

import (
	"errors"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// classifyVaultError translates a raw Vault Transit error into an
// apperror that expresses the operational intent. The mapping mirrors
// awskms's classifyAWSError so callers using apperror.IsUnavailable /
// IsPermanent get consistent semantics across KMS adapters.
//
// Vault surfaces transport-class errors as *vaultapi.ResponseError with
// the HTTP status code; this is the only reliable classification surface
// (the body messages vary by Vault version and plugin).
func classifyVaultError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var respErr *vaultapi.ResponseError
	if !errors.As(err, &respErr) {
		return err
	}
	switch respErr.StatusCode {
	case 429:
		return apperror.NewDependencyUnavailable("kms", "vault throttled (429)", err)
	case 408, 500, 502, 503, 504:
		return apperror.NewDependencyUnavailable("kms", "vault transient (status "+statusString(respErr.StatusCode)+")", err)
	case 401, 403:
		return apperror.NewPermanentWithCause("vault access denied (status "+statusString(respErr.StatusCode)+")", err)
	case 404:
		return apperror.NewPermanentWithCause("vault key not found (status 404)", err)
	default:
		return err
	}
}

func statusString(code int) string {
	// Tiny helper so the package doesn't pull strconv just for this.
	if code < 100 || code >= 1000 {
		return "unknown"
	}
	return string([]byte{
		byte('0' + (code/100)%10),
		byte('0' + (code/10)%10),
		byte('0' + code%10),
	})
}
