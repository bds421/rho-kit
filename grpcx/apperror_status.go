package grpcx

import (
	"errors"

	"github.com/bds421/rho-kit/core/apperror"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// defaultGRPCCode maps application error codes to gRPC status codes.
var defaultGRPCCode = map[apperror.Code]codes.Code{
	apperror.CodeNotFound:        codes.NotFound,
	apperror.CodeValidation:      codes.InvalidArgument,
	apperror.CodeConflict:        codes.AlreadyExists,
	apperror.CodePermanent:       codes.FailedPrecondition,
	apperror.CodeAuthRequired:    codes.Unauthenticated,
	apperror.CodeRateLimit:       codes.ResourceExhausted,
	apperror.CodeOperationFailed: codes.Internal,
	apperror.CodeForbidden:       codes.PermissionDenied,
	apperror.CodeUnavailable:     codes.Unavailable,
}

// GRPCCode returns the gRPC status code for the given error.
// Returns codes.Internal for non-apperror errors or unknown codes.
//
// For [apperror.UnavailableError], the code is always codes.Unavailable
// regardless of whether a dependency is identified (unlike HTTP which
// distinguishes 502 vs 503).
func GRPCCode(err error) codes.Code {
	var appErr apperror.AppError
	if !errors.As(err, &appErr) {
		return codes.Internal
	}
	if code, found := defaultGRPCCode[appErr.ErrorCode()]; found {
		return code
	}
	return codes.Internal
}

// GRPCStatus converts an error to a *status.Status with the appropriate gRPC
// status code. The error message is preserved for client consumption.
// Non-apperror errors return codes.Internal with a generic message to avoid
// leaking internal details.
func GRPCStatus(err error) *status.Status {
	var appErr apperror.AppError
	if !errors.As(err, &appErr) {
		return status.New(codes.Internal, "internal error")
	}
	return status.New(GRPCCode(err), appErr.Error())
}
