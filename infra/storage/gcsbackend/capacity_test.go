package gcsbackend

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/api/googleapi"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestTranslateGCSCapacity(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantCap bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("plain"), false},
		{"507", &googleapi.Error{Code: 507, Message: "Insufficient Storage"}, true},
		{"413 quota", &googleapi.Error{Code: 413, Message: "object exceeds bucket quota"}, true},
		{"413 storage", &googleapi.Error{Code: 413, Message: "bucket storage limit reached"}, true},
		{"413 other", &googleapi.Error{Code: 413, Message: "header too large"}, false},
		{"500", &googleapi.Error{Code: 500, Message: "server error"}, false},
		{"wrapped 507", fmt.Errorf("write: %w", &googleapi.Error{Code: 507, Message: "Insufficient Storage"}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateGCSCapacity(tc.err)
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
