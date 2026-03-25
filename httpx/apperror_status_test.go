package httpx

import (
	"errors"
	"testing"
	"time"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/stretchr/testify/assert"
)

func TestHTTPStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"NotFound", apperror.NewNotFound("x", 1), 404},
		{"Validation", apperror.NewValidation("bad"), 400},
		{"Conflict", apperror.NewConflict("dup"), 409},
		{"Permanent", apperror.NewPermanent("no"), 422},
		{"AuthRequired", apperror.NewAuthRequired("login"), 401},
		{"RateLimit", apperror.NewRateLimit("slow", time.Second), 429},
		{"OperationFailed", apperror.NewOperationFailed("fail"), 500},
		{"Forbidden", apperror.NewForbidden("denied"), 403},
		{"Generic", errors.New("generic"), 500},
		{"Unavailable_NoDep_503", apperror.NewUnavailable("not ready"), 503},
		{"Unavailable_WithCause_NoDep_503", apperror.NewUnavailableWithCause("not ready", errors.New("cause")), 503},
		{"DependencyUnavailable_502", apperror.NewDependencyUnavailable("redis", "redis down", nil), 502},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HTTPStatus(tt.err))
		})
	}
}
