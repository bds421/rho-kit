package azurekeyvault

import (
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

func azureResponseError(status int) error {
	return &azcore.ResponseError{StatusCode: status, ErrorCode: "test"}
}

func TestClassifyAzureError(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		if err := classifyAzureError("wrap", azureResponseError(status)); !apperror.IsUnavailable(err) {
			t.Fatalf("status %d = %v, want unavailable", status, err)
		}
	}
	for _, status := range []int{401, 403, 404, 409} {
		if err := classifyAzureError("unwrap", azureResponseError(status)); !apperror.IsPermanent(err) {
			t.Fatalf("status %d = %v, want permanent", status, err)
		}
	}
	raw := azureResponseError(400)
	if got := classifyAzureError("wrap", raw); got != raw {
		t.Fatalf("unclassified status must pass through, got %v", got)
	}
	plain := errors.New("dial failed")
	if got := classifyAzureError("wrap", plain); got != plain {
		t.Fatalf("non-response error must pass through, got %v", got)
	}
	if got := classifyAzureError("wrap", nil); got != nil {
		t.Fatalf("nil = %v, want nil", got)
	}
}
