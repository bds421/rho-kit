package grpcx

import (
	"errors"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// kitErrorInfoDomain is stamped on every [GRPCStatus] conversion so
// clients (e.g. the retry interceptor) can classify permanent application
// errors without parsing free-form messages.
const kitErrorInfoDomain = "rho-kit"

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
	// ResourceExhausted is the canonical gRPC code for "the backend
	// can't accept this write because it is out of capacity" —
	// distinct from rate-limit semantics on the client path (also
	// ResourceExhausted) only in the cause string, but the kit chose
	// the same gRPC code so clients can apply uniform backoff.
	apperror.CodeStorageFull: codes.ResourceExhausted,
	// CodeTimeout maps to DeadlineExceeded — gRPC's canonical
	// "deadline expired" code; matches HTTP 408 on the other transport.
	apperror.CodeTimeout: codes.DeadlineExceeded,
	// CodePayloadTooLarge maps to ResourceExhausted (no per-payload
	// gRPC code exists). Same code as CodeStorageFull but the cause
	// message distinguishes "your payload is too big" from "the
	// backend is full". Matches HTTP 413 on the other transport.
	// Clients must NOT retry: [GRPCStatus] attaches ErrorInfo with
	// Reason=PAYLOAD_TOO_LARGE for machine-readable permanence.
	apperror.CodePayloadTooLarge: codes.ResourceExhausted,
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
//
// App errors receive an [errdetails.ErrorInfo] detail with Domain "rho-kit"
// and Reason set to the [apperror.Code] string so clients can classify
// permanent ResourceExhausted (e.g. PAYLOAD_TOO_LARGE) without parsing
// free-form messages.
func GRPCStatus(err error) *status.Status {
	var appErr apperror.AppError
	if !errors.As(err, &appErr) {
		return status.New(codes.Internal, "internal error")
	}
	st := status.New(GRPCCode(err), appErr.Error())
	detail := &errdetails.ErrorInfo{
		Reason: string(appErr.ErrorCode()),
		Domain: kitErrorInfoDomain,
	}
	if with, derr := st.WithDetails(detail); derr == nil {
		return with
	}
	return st
}
