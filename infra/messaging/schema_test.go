package messaging_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
)

func TestInMemorySchemaRegistry_Register(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type":"object"}`)

	err := reg.Register("order.created", 1, schema)
	require.NoError(t, err)

	got, err := reg.Lookup("order.created", 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object"}`, string(got))
}

func TestInMemorySchemaRegistry_Register_DuplicateError(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type":"object"}`)

	require.NoError(t, reg.Register("order.created", 1, schema))

	err := reg.Register("order.created", 1, schema)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

func TestInMemorySchemaRegistry_Register_EmptyTypeError(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	err := reg.Register("", 1, json.RawMessage(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type must not be empty")
}

func TestInMemorySchemaRegistry_Register_NilSchemaError(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	err := reg.Register("order.created", 1, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil or empty")
}

func TestInMemorySchemaRegistry_Register_EmptySchemaError(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	err := reg.Register("order.created", 1, json.RawMessage{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil or empty")
}

func TestInMemorySchemaRegistry_Lookup_NotFound(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()

	_, err := reg.Lookup("order.created", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no schema found")
}

func TestInMemorySchemaRegistry_Lookup_ImmutableResult(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type":"object"}`)
	require.NoError(t, reg.Register("test.event", 1, schema))

	got, err := reg.Lookup("test.event", 1)
	require.NoError(t, err)

	// Mutate the returned value.
	got[0] = 'X'

	// Original should be unaffected.
	got2, err := reg.Lookup("test.event", 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object"}`, string(got2))
}

func TestInMemorySchemaRegistry_Register_ImmutableInput(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type":"object"}`)
	require.NoError(t, reg.Register("test.event", 1, schema))

	// Mutate the input after registration.
	schema[0] = 'X'

	got, err := reg.Lookup("test.event", 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"object"}`, string(got))
}

func TestInMemorySchemaRegistry_Versions(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{}`)

	require.NoError(t, reg.Register("order.created", 3, schema))
	require.NoError(t, reg.Register("order.created", 1, schema))
	require.NoError(t, reg.Register("order.created", 2, schema))
	require.NoError(t, reg.Register("user.updated", 1, schema))

	versions := reg.Versions("order.created")
	assert.Equal(t, []uint{1, 2, 3}, versions)
}

func TestInMemorySchemaRegistry_Versions_UnknownType(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	assert.Nil(t, reg.Versions("unknown.type"))
}

func TestInMemorySchemaRegistry_MultipleTypes(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()

	require.NoError(t, reg.Register("order.created", 1, json.RawMessage(`{"v":1}`)))
	require.NoError(t, reg.Register("user.updated", 1, json.RawMessage(`{"v":2}`)))

	got1, err := reg.Lookup("order.created", 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1}`, string(got1))

	got2, err := reg.Lookup("user.updated", 1)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":2}`, string(got2))
}

func TestInMemorySchemaRegistry_ConcurrentAccess(t *testing.T) {
	reg := messaging.NewInMemorySchemaRegistry()
	schema := json.RawMessage(`{"type":"object"}`)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(version uint) {
			defer wg.Done()
			_ = reg.Register("test.event", version, schema)
		}(uint(i))
	}
	wg.Wait()

	versions := reg.Versions("test.event")
	assert.Len(t, versions, 50)
}
