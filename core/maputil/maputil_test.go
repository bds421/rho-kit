package maputil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetIfNotNil_WritesDereferencedValue(t *testing.T) {
	m := make(map[string]any)
	v := "hello"
	SetIfNotNil(m, "k", &v)
	assert.Equal(t, "hello", m["k"])
}

func TestSetIfNotNil_NilPointerIsNoop(t *testing.T) {
	m := make(map[string]any)
	var v *string
	SetIfNotNil(m, "k", v)
	_, exists := m["k"]
	assert.False(t, exists)
}

func TestSetIfNotNil_NilMapPanics(t *testing.T) {
	v := 42
	assert.Panics(t, func() {
		SetIfNotNil[int](nil, "k", &v)
	})
}

func TestSetIfNotNil_NilValueOnNilMapIsNoop(t *testing.T) {
	// The nil-value early-return means a nil map never gets touched.
	var v *int
	assert.NotPanics(t, func() {
		SetIfNotNil[int](nil, "k", v)
	})
}

func TestSetIfNotNil_OverwritesExisting(t *testing.T) {
	m := map[string]any{"k": "old"}
	v := "new"
	SetIfNotNil(m, "k", &v)
	assert.Equal(t, "new", m["k"])
}
