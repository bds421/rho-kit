package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSuppressionInventoryMetadata(t *testing.T) {
	got, ok := parseSuppression("main.go", 7, `// kit-doctor:allow rate-limit-omission owner="platform" reason="edge limit" review="2026-12-01" posture="security"`)
	assert.True(t, ok)
	assert.Equal(t, "rate-limit-omission", got.Rule)
	assert.Equal(t, "platform", got.Owner)
	assert.True(t, got.Complete)
}

func TestParseSuppressionLegacyIsVisibleButIncomplete(t *testing.T) {
	got, ok := parseSuppression("main.go", 7, `// kit-doctor:allow rate-limit-omission reason="edge limit"`)
	assert.True(t, ok)
	assert.False(t, got.Complete)
}
