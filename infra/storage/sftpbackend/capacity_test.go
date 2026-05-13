package sftpbackend

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestTranslateSFTPCapacity(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantCap bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("plain"), false},
		{"ENOSPC wrapped", fmt.Errorf("remote write: %w", syscall.ENOSPC), true},
		{"no space text", errors.New("ssh: no space left on device"), true},
		{"random error", errors.New("permission denied"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateSFTPCapacity(tc.err)
			if !tc.wantCap {
				assert.Nil(t, got)
				return
			}
			assert.True(t, errors.Is(got, storage.ErrInsufficientCapacity), "got %v", got)
			assert.True(t, apperror.IsStorageFull(got))
		})
	}
}
