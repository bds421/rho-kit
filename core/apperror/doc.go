// Package apperror defines the transport-agnostic application error model
// used across the kit. Domain code returns these errors; transport adapters
// (httpx, grpcx) map them to the appropriate transport status.
//
// # Error model
//
// [AppError] is the sealed interface implemented by every concrete error
// type. Each value carries:
//
//   - a typed [Code] identifying the failure category;
//   - a human-readable Message safe to surface to callers;
//   - a Retryable flag the resilience/retry helpers consult.
//
// # Codes
//
// The nine [Code] values cover the recurring failure shapes seen in
// service code:
//
//   - [CodeNotFound]:        requested entity does not exist.
//   - [CodeValidation]:      input failed structural or semantic checks.
//   - [CodeConflict]:        state mismatch (duplicate key, version skew).
//   - [CodePermanent]:       failure will not succeed on retry.
//   - [CodeAuthRequired]:    caller is unauthenticated.
//   - [CodeForbidden]:       caller is authenticated but unauthorized.
//   - [CodeRateLimit]:       quota or rate cap exceeded.
//   - [CodeOperationFailed]: server-side failure not expressible by the others.
//   - [CodeUnavailable]:     dependency unreachable or service not ready.
//
// # Constructors
//
// Constructors follow the convention `New<Type>` (or `New<Type>WithCause`
// / `New<Type>WithRetryAfter` for the variants that carry extra fields):
//
//   - [NewNotFound], [NewValidation], [NewFieldValidation]
//   - [NewConflict] (non-retryable), [NewConflictRetryable]
//   - [NewPermanent], [NewPermanentWithCause]
//   - [NewAuthRequired], [NewForbidden]
//   - [NewRateLimit], [NewRateLimitWithRetryAfter]
//   - [NewOperationFailed], [NewOperationFailedWithCause]
//   - [NewUnavailable], [NewUnavailableWithCause], [NewUnavailableWithRetryAfter]
//   - [NewDependencyUnavailable]
//
// # Predicates and extractors
//
// `Is<Type>(err)` predicates report whether the error chain contains a
// particular concrete type; `As<Type>(err)` extractors return the value
// when the caller needs structured fields (RetryAfter, Dependency, the
// validation FieldError slice, etc.). Extractors are provided only for
// types that carry information beyond Message.
//
// # Retry integration
//
// [ShouldRetry] is the helper resilience/retry uses to decide whether an
// error is worth retrying. It returns the error's [AppError.Retryable]
// flag, defaulting to false for non-apperror values.
//
// # Transport mapping
//
// apperror is intentionally transport-agnostic. HTTP status mapping
// lives in httpx (see httpx.HTTPStatus and the problemdetails package);
// gRPC code mapping lives in grpcx. Domain code should not reach across
// the kit boundary to construct transport-specific errors.
package apperror
