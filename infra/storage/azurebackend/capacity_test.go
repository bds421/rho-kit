package azurebackend

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestTranslateAzureCapacity(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantCap bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("plain"), false},
		{"InsufficientStorage code", &azcore.ResponseError{ErrorCode: "InsufficientStorage", StatusCode: 507}, true},
		{"RequestBodyTooLarge code", &azcore.ResponseError{ErrorCode: "RequestBodyTooLarge", StatusCode: 413}, true},
		{"only status 507", &azcore.ResponseError{ErrorCode: "Other", StatusCode: 507}, true},
		{"only status 413", &azcore.ResponseError{ErrorCode: "Other", StatusCode: 413}, true},
		{"unrelated 500", &azcore.ResponseError{ErrorCode: "InternalError", StatusCode: 500}, false},
		{"wrapped", fmt.Errorf("upload: %w", &azcore.ResponseError{ErrorCode: "RequestBodyTooLarge", StatusCode: 413}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateAzureCapacity(tc.err)
			if !tc.wantCap {
				assert.Nil(t, got)
				return
			}
			assert.True(t, errors.Is(got, storage.ErrInsufficientCapacity), "got %v", got)
			assert.True(t, apperror.IsStorageFull(got))
			assert.True(t, errors.Is(got, tc.err), "must preserve cause")
		})
	}
}
