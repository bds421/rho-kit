package apperror

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
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
	err := NewRateLimitWithRetryAfter("quota exceeded", 30*time.Second)
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
	err := NewRateLimit("too many requests")
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

func TestNewFieldValidation_ZeroArgs_Panics(t *testing.T) {
	assert.Panics(t, func() {
		_ = NewFieldValidation()
	})
}

// HTTPStatus mapping moved to httpx.HTTPStatus — see httpx/apperror_status_test.go
// for the equivalent table-driven test.

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
	var _ AppError = &ForbiddenError{}
	var _ AppError = &StorageFullError{}
	var _ AppError = &TimeoutError{}
	var _ AppError = &PayloadTooLargeError{}
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

func TestStorageFullError(t *testing.T) {
	err := NewStorageFull("bucket quota exceeded")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bucket quota exceeded")
	assert.True(t, IsStorageFull(err))
	assert.False(t, IsUnavailable(err))
	assert.False(t, IsNotFound(err))

	var sfe *StorageFullError
	assert.True(t, errors.As(err, &sfe))
	assert.Equal(t, CodeStorageFull, sfe.ErrorCode())
	assert.True(t, sfe.Retryable())
	assert.Nil(t, sfe.Unwrap())
}

func TestStorageFullErrorWithCause(t *testing.T) {
	cause := errors.New("no space left on device")
	err := NewStorageFullWithCause("write failed", cause)
	assert.True(t, IsStorageFull(err))
	assert.True(t, errors.Is(err, cause))

	var sfe *StorageFullError
	assert.True(t, errors.As(err, &sfe))
	assert.Equal(t, cause, sfe.Unwrap())
}

func TestRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"NotFound", NewNotFound("x", 1), false},
		{"Validation", NewValidation("bad"), false},
		{"Conflict", NewConflict("dup"), false},
		{"ConflictRetryable", NewConflictRetryable("optimistic"), true},
		{"Permanent", NewPermanent("no"), false},
		{"AuthRequired", NewAuthRequired("login"), false},
		{"Forbidden", NewForbidden("denied"), false},
		{"RateLimit", NewRateLimit("slow"), true},
		{"RateLimitWithRetryAfter", NewRateLimitWithRetryAfter("slow", time.Second), true},
		{"OperationFailed", NewOperationFailed("fail"), false},
		{"Unavailable", NewUnavailable("down"), true},
		{"DependencyUnavailable", NewDependencyUnavailable("redis", "down", nil), true},
		{"StorageFull", NewStorageFull("disk full"), true},
		{"Timeout", NewTimeout("deadline exceeded"), true},
		{"PayloadTooLarge", NewPayloadTooLarge("too big", 1024), false},
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
	assert.True(t, ShouldRetry(NewConflictRetryable("optimistic")))
	assert.True(t, ShouldRetry(NewRateLimit("slow")))
	assert.True(t, ShouldRetry(NewRateLimitWithRetryAfter("slow", time.Second)))
	assert.True(t, ShouldRetry(NewUnavailable("down")))
	assert.True(t, ShouldRetry(NewStorageFull("disk full")))
	assert.True(t, ShouldRetry(NewTimeout("deadline exceeded")))

	// Non-retryable app errors return false.
	assert.False(t, ShouldRetry(NewNotFound("x", 1)))
	assert.False(t, ShouldRetry(NewValidation("bad")))
	assert.False(t, ShouldRetry(NewConflict("dup")))
	assert.False(t, ShouldRetry(NewPermanent("no")))
	assert.False(t, ShouldRetry(NewOperationFailed("fail")))
	assert.False(t, ShouldRetry(NewPayloadTooLarge("too big", 1024)))

	// Non-apperror errors return false (fail-safe).
	assert.False(t, ShouldRetry(errors.New("generic")))

	// Nil errors return false.
	assert.False(t, ShouldRetry(nil))
}

// TestAllCodes_Complete enforces that every package-level `Code*`
// constant is present in [AllCodes]. Adding a new Code without
// adding it to AllCodes would otherwise drift silently — the
// transport-adapter exhaustiveness tests (grpcx, httpx) consume
// AllCodes, so an omission here cascades into "the new code is
// real but the maps don't know it".
//
// Reflection over package-level constants isn't available in Go, so
// we parse the source file with go/ast. The test runs alongside the
// errors.go file in the same package, which keeps the AST scan
// trivially scoped.
func TestAllCodes_Complete(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "errors.go", nil, 0)
	if err != nil {
		t.Fatalf("parse errors.go: %v", err)
	}

	var declared []Code
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			// Each constant in this file follows `CodeX Code = "..."`;
			// the const block declares the same Code type explicitly so
			// we look for one Name per ValueSpec.
			for _, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Code") {
					continue
				}
				// The constant's runtime value is reachable by looking
				// the name up via the declared package — but a test in
				// the same package can just type-switch on the literal.
				if len(vs.Values) != 1 {
					continue
				}
				lit, ok := vs.Values[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				// Strip the surrounding quotes from the string literal.
				raw, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				declared = append(declared, Code(raw))
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("AST scan found zero Code constants — parser broken or file moved")
	}

	got := AllCodes()
	gotSet := make(map[Code]struct{}, len(got))
	for _, c := range got {
		gotSet[c] = struct{}{}
	}
	var missing []Code
	for _, c := range declared {
		if _, ok := gotSet[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("AllCodes() missing entries: %v (declared constants: %v)", missing, declared)
	}
	if len(got) != len(declared) {
		t.Fatalf("AllCodes() has %d entries but %d Code constants are declared (extras or duplicates)",
			len(got), len(declared))
	}
}
