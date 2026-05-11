package app

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/slo"
)

func TestNewSLOModule_ClonesDefinitions(t *testing.T) {
	defs := []slo.SLO{slo.ErrorRateSLO("api-errors", 0.01, time.Hour)}

	m := newSLOModule(defs...)
	defs[0].Name = "mutated"

	require.Len(t, m.slos, 1)
	assert.Equal(t, "api-errors", m.slos[0].Name)
}
