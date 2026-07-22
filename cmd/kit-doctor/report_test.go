package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

func TestNewReport_IsVersionedAndCountsFindings(t *testing.T) {
	r := newReport("./service", "high", []rules.Finding{
		{Severity: rules.Warning}, {Severity: rules.High}, {Severity: rules.High}, {Severity: rules.Critical},
	}, nil)
	assert.Equal(t, "rho-kit-doctor/v1", r.SchemaVersion)
	assert.Equal(t, "./service", r.Path)
	assert.Equal(t, "high", r.Strict)
	assert.Equal(t, 1, r.Summary.Warning)
	assert.Equal(t, 2, r.Summary.High)
	assert.Equal(t, 1, r.Summary.Critical)
}
