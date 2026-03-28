package grpcx_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/grpcx"
)

func TestGRPCCode_MapsAppErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected codes.Code
	}{
		{"not found", apperror.NewNotFound("user", 1), codes.NotFound},
		{"validation", apperror.NewValidation("invalid input"), codes.InvalidArgument},
		{"conflict", apperror.NewConflict("duplicate entry"), codes.AlreadyExists},
		{"permanent", apperror.NewPermanent("permanent error"), codes.FailedPrecondition},
		{"auth required", apperror.NewAuthRequired("login required"), codes.Unauthenticated},
		{"rate limit", apperror.NewRateLimit("too many requests", 0), codes.ResourceExhausted},
		{"operation failed", apperror.NewOperationFailed("failed"), codes.Internal},
		{"forbidden", apperror.NewForbidden("not allowed"), codes.PermissionDenied},
		{"unavailable", apperror.NewUnavailable("service down"), codes.Unavailable},
		{"non-apperror", errors.New("random error"), codes.Internal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, grpcx.GRPCCode(tt.err))
		})
	}
}

func TestGRPCStatus_NonAppError(t *testing.T) {
	st := grpcx.GRPCStatus(errors.New("some error"))
	assert.Equal(t, codes.Internal, st.Code())
	assert.Equal(t, "internal error", st.Message())
}

func TestGRPCStatus_AppError(t *testing.T) {
	err := apperror.NewNotFound("user", 42)
	st := grpcx.GRPCStatus(err)
	assert.Equal(t, codes.NotFound, st.Code())
	assert.Contains(t, st.Message(), "not found")
}
