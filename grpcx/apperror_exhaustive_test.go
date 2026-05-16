package grpcx

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/core/v2/apperror"
)

// Wave 144: exhaustiveness gate — every kit-defined apperror.Code must
// have a gRPC status mapping. Adding a new Code in core/apperror
// without updating defaultGRPCCode now fails this test rather than
// silently downgrading the response to codes.Internal.
func TestGRPCCode_AllCodesMapped(t *testing.T) {
	for _, code := range apperror.AllCodes() {
		_, found := defaultGRPCCode[code]
		assert.True(t, found, "apperror.Code %q has no entry in defaultGRPCCode — add a mapping", code)
	}
}
