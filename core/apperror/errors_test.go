package apperror

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNotFoundError(t *testing.T) {
	err := NewNotFound("user", "123")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "user")
	assert.Contains(t, err.Error(), "123")
	assert.True(t, IsNotFound(err))
	assert.False(t, IsValidation(err))
	assert.False(t, IsConflict(err))
	assert.False(t, IsPermanent(err))

	nf, ok := AsNotFound(err)
	assert.True(t, ok)
	assert.Equal(t, "user", nf.Entity)
	assert.Equal(t, "123", nf.EntityID)
}

func TestValidationError(t *testing.T) {
	err := NewValidation("email is required")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "email is required")
	assert.True(t, IsValidation(err))
	assert.False(t, IsNotFound(err))
	assert.False(t, IsConflict(err))
	assert.False(t, IsPermanent(err))
}

func TestFieldValidationError(t *testing.T) {
	err := NewFieldValidation(
		FieldError{Field: "name", Message: "is required"},
		FieldError{Field: "port", Message: "must be between 1 and 65535"},
	)
	assert.Error(t, err)
	assert.True(t, IsValidation(err))
	assert.Contains(t, err.Error(), "name: is required")
	assert.Contains(t, err.Error(), "port: must be between 1 and 65535")

	ve, ok := AsValidation(err)
	assert.True(t, ok)
	assert.Len(t, ve.Fields, 2)
	assert.Equal(t, "name", ve.Fields[0].Field)
	assert.Equal(t, "port", ve.Fields[1].Field)
}

func TestConflictError(t *testing.T) {
	err := NewConflict("name already exists")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "name already exists")
	assert.True(t, IsConflict(err))
	assert.False(t, IsNotFound(err))
	assert.False(t, IsValidation(err))
	assert.False(t, IsPermanent(err))
}

func TestPermanentError(t *testing.T) {
	err := NewPermanent("SMTP is not enabled")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "SMTP is not enabled")
	assert.True(t, IsPermanent(err))
	assert.False(t, IsNotFound(err))
	assert.False(t, IsValidation(err))
	assert.False(t, IsConflict(err))

	var pe *PermanentError
	assert.True(t, errors.As(err, &pe))
	assert.Nil(t, pe.Unwrap())
}

func TestPermanentErrorWithCause(t *testing.T) {
	cause := errors.New("connection refused")
	err := NewPermanentWithCause("delivery failed", cause)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "delivery failed")
	assert.True(t, IsPermanent(err))

	assert.True(t, errors.Is(err, cause))

	var pe *PermanentError
	assert.True(t, errors.As(err, &pe))
	assert.Equal(t, cause, pe.Unwrap())
}

func TestAsValidation_NotValidation(t *testing.T) {
	err := NewNotFound("user", "123")
	_, ok := AsValidation(err)
	assert.False(t, ok)
}

func TestAuthRequiredError(t *testing.T) {
	err := NewAuthRequired("session expired")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session expired")
	assert.True(t, IsAuthRequired(err))
	assert.False(t, IsNotFound(err))
	assert.False(t, IsValidation(err))
	assert.False(t, IsConflict(err))
	assert.False(t, IsPermanent(err))
	assert.False(t, IsRateLimit(err))
	assert.False(t, IsOperationFailed(err))
}

func TestRateLimitError(t *testing.T) {
	err := NewRateLimit("quota exceeded", 30*time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
	assert.True(t, IsRateLimit(err))
	assert.False(t, IsNotFound(err))
	assert.False(t, IsAuthRequired(err))
	assert.False(t, IsPermanent(err))

	rl, ok := AsRateLimit(err)
	assert.True(t, ok)
	assert.Equal(t, 30*time.Second, rl.RetryAfter)
}

func TestRateLimitError_ZeroRetryAfter(t *testing.T) {
	err := NewRateLimit("too many requests", 0)
	assert.True(t, IsRateLimit(err))

	rl, ok := AsRateLimit(err)
	assert.True(t, ok)
	assert.Zero(t, rl.RetryAfter)
}

func TestAsRateLimit_NotRateLimit(t *testing.T) {
	err := NewNotFound("user", "123")
	_, ok := AsRateLimit(err)
	assert.False(t, ok)
}

func TestOperationFailedError(t *testing.T) {
	err := NewOperationFailed("payment declined")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "payment declined")
	assert.True(t, IsOperationFailed(err))
	assert.False(t, IsNotFound(err))
	assert.False(t, IsValidation(err))
	assert.False(t, IsPermanent(err))

	var oe *OperationFailedError
	assert.True(t, errors.As(err, &oe))
	assert.Nil(t, oe.Unwrap())
}

func TestOperationFailedErrorWithCause(t *testing.T) {
	cause := errors.New("gateway timeout")
	err := NewOperationFailedWithCause("payment failed", cause)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "payment failed")
	assert.True(t, IsOperationFailed(err))

	assert.True(t, errors.Is(err, cause))

	var oe *OperationFailedError
	assert.True(t, errors.As(err, &oe))
	assert.Equal(t, cause, oe.Unwrap())
}

func TestAsNotFound(t *testing.T) {
	err := NewNotFound("user", uint(42))
	nf, ok := AsNotFound(err)
	assert.True(t, ok)
	assert.Equal(t, "user", nf.Entity)
	assert.Equal(t, uint(42), nf.EntityID)
}

func TestAsNotFound_NotNotFound(t *testing.T) {
	err := NewConflict("dup")
	_, ok := AsNotFound(err)
	assert.False(t, ok)
}

func TestAsConflict(t *testing.T) {
	err := NewConflict("name already exists")
	c, ok := AsConflict(err)
	assert.True(t, ok)
	assert.Equal(t, "name already exists", c.Message)
}

func TestAsConflict_NotConflict(t *testing.T) {
	err := NewNotFound("user", "1")
	_, ok := AsConflict(err)
	assert.False(t, ok)
}

func TestAsPermanent(t *testing.T) {
	cause := errors.New("upstream failed")
	err := NewPermanentWithCause("cannot retry", cause)
	p, ok := AsPermanent(err)
	assert.True(t, ok)
	assert.Equal(t, "cannot retry", p.Message)
	assert.Equal(t, cause, p.Unwrap())
}

func TestAsPermanent_NotPermanent(t *testing.T) {
	err := NewValidation("bad input")
	_, ok := AsPermanent(err)
	assert.False(t, ok)
}

func TestAsAuthRequired(t *testing.T) {
	err := NewAuthRequired("token expired")
	a, ok := AsAuthRequired(err)
	assert.True(t, ok)
	assert.Equal(t, "token expired", a.Message)
}

func TestAsAuthRequired_NotAuthRequired(t *testing.T) {
	err := NewConflict("conflict")
	_, ok := AsAuthRequired(err)
	assert.False(t, ok)
}

func TestAsOperationFailed(t *testing.T) {
	cause := errors.New("db timeout")
	err := NewOperationFailedWithCause("operation failed", cause)
	o, ok := AsOperationFailed(err)
	assert.True(t, ok)
	assert.Equal(t, "operation failed", o.Message)
	assert.Equal(t, cause, o.Unwrap())
}

func TestAsOperationFailed_NotOperationFailed(t *testing.T) {
	err := NewNotFound("user", "1")
	_, ok := AsOperationFailed(err)
	assert.False(t, ok)
}

func TestNewFieldValidation_ZeroArgs_Panics(t *testing.T) {
	assert.Panics(t, func() {
		_ = NewFieldValidation()
	})
}

func TestHTTPStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"NotFound", NewNotFound("x", 1), 404},
		{"Validation", NewValidation("bad"), 400},
		{"Conflict", NewConflict("dup"), 409},
		{"Permanent", NewPermanent("no"), 422},
		{"AuthRequired", NewAuthRequired("login"), 401},
		{"RateLimit", NewRateLimit("slow", time.Second), 429},
		{"OperationFailed", NewOperationFailed("fail"), 500},
		{"Forbidden", NewForbidden("denied"), 403},
		{"Generic", errors.New("generic"), 500},
		{"Unavailable_NoDep_503", NewUnavailable("not ready"), 503},
		{"Unavailable_WithCause_NoDep_503", NewUnavailableWithCause("not ready", errors.New("cause")), 503},
		{"DependencyUnavailable_502", NewDependencyUnavailable("redis", "redis down", nil), 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HTTPStatus(tt.err))
		})
	}
}

func TestAppErrorInterface(t *testing.T) {
	// All concrete types implement AppError.
	var _ AppError = &NotFoundError{}
	var _ AppError = &ValidationError{}
	var _ AppError = &ConflictError{}
	var _ AppError = &PermanentError{}
	var _ AppError = &AuthRequiredError{}
	var _ AppError = &RateLimitError{}
	var _ AppError = &OperationFailedError{}
	var _ AppError = &UnavailableError{}
}

func TestUnavailableError(t *testing.T) {
	err := NewUnavailable("service not ready")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "service not ready")
	assert.True(t, IsUnavailable(err))
	assert.False(t, IsNotFound(err))
	assert.False(t, IsOperationFailed(err))

	var ue *UnavailableError
	assert.True(t, errors.As(err, &ue))
	assert.Nil(t, ue.Unwrap())
	assert.Empty(t, ue.Dependency)
}

func TestUnavailableErrorWithCause(t *testing.T) {
	cause := errors.New("connection refused")
	err := NewUnavailableWithCause("redis down", cause)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis down")
	assert.True(t, IsUnavailable(err))
	assert.True(t, errors.Is(err, cause))

	var ue *UnavailableError
	assert.True(t, errors.As(err, &ue))
	assert.Equal(t, cause, ue.Unwrap())
}

func TestDependencyUnavailable(t *testing.T) {
	cause := errors.New("tcp dial timeout")
	err := NewDependencyUnavailable("payment-service", "payment service unreachable", cause)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "payment service unreachable")
	assert.True(t, IsUnavailable(err))
	assert.True(t, errors.Is(err, cause))

	ue, ok := AsUnavailable(err)
	assert.True(t, ok)
	assert.Equal(t, "payment-service", ue.Dependency)
	assert.Equal(t, cause, ue.Unwrap())
}

func TestAsUnavailable_NotUnavailable(t *testing.T) {
	err := NewNotFound("user", "1")
	_, ok := AsUnavailable(err)
	assert.False(t, ok)
}

func TestRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"NotFound", NewNotFound("x", 1), false},
		{"Validation", NewValidation("bad"), false},
		{"Conflict", NewConflict("dup"), true},
		{"Permanent", NewPermanent("no"), false},
		{"AuthRequired", NewAuthRequired("login"), false},
		{"Forbidden", NewForbidden("denied"), false},
		{"RateLimit", NewRateLimit("slow", time.Second), true},
		{"OperationFailed", NewOperationFailed("fail"), false},
		{"Unavailable", NewUnavailable("down"), true},
		{"DependencyUnavailable", NewDependencyUnavailable("redis", "down", nil), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var appErr AppError
			assert.True(t, errors.As(tt.err, &appErr))
			assert.Equal(t, tt.retryable, appErr.Retryable())
		})
	}
}

func TestShouldRetry(t *testing.T) {
	// Retryable app errors return true.
	assert.True(t, ShouldRetry(NewConflict("dup")))
	assert.True(t, ShouldRetry(NewRateLimit("slow", time.Second)))
	assert.True(t, ShouldRetry(NewUnavailable("down")))

	// Non-retryable app errors return false.
	assert.False(t, ShouldRetry(NewNotFound("x", 1)))
	assert.False(t, ShouldRetry(NewValidation("bad")))
	assert.False(t, ShouldRetry(NewPermanent("no")))
	assert.False(t, ShouldRetry(NewOperationFailed("fail")))

	// Non-apperror errors return false (fail-safe).
	assert.False(t, ShouldRetry(errors.New("generic")))

	// Nil errors return false.
	assert.False(t, ShouldRetry(nil))
}
