package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFailOn_AcceptsAllSupportedMetrics(t *testing.T) {
	got, err := parseFailOn("ns/op,B/op,allocs/op")
	require.NoError(t, err)
	assert.Contains(t, got, MetricNs)
	assert.Contains(t, got, MetricBytes)
	assert.Contains(t, got, MetricAllocs)
}

func TestParseFailOn_TrimsWhitespaceAndAllowsEmptyTokens(t *testing.T) {
	got, err := parseFailOn(" ns/op , , B/op ")
	require.NoError(t, err)
	assert.Len(t, got, 2)
	assert.Contains(t, got, MetricNs)
	assert.Contains(t, got, MetricBytes)
}

func TestParseFailOn_RejectsUnknownMetric(t *testing.T) {
	_, err := parseFailOn("alloc/op")
	require.Error(t, err, "typo must be rejected, not silently dropped")
	assert.Contains(t, err.Error(), "alloc/op")
}

func TestParseFailOn_RejectsEvenWithValidMixed(t *testing.T) {
	_, err := parseFailOn("ns/op,bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestIsValidMetric(t *testing.T) {
	assert.True(t, IsValidMetric(MetricNs))
	assert.True(t, IsValidMetric(MetricBytes))
	assert.True(t, IsValidMetric(MetricAllocs))
	assert.False(t, IsValidMetric(Metric("alloc/op")))
	assert.False(t, IsValidMetric(Metric("")))
}
