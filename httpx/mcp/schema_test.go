package mcp_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/v2/mcp"
)

type addressInput struct {
	Street string `json:"street" validate:"required"`
	City   string `json:"city"`
}

type representativeInput struct {
	Name      string         `json:"name" validate:"required"`
	Age       int            `json:"age"`
	Active    bool           `json:"active"`
	Tags      []string       `json:"tags"`
	CreatedAt time.Time      `json:"created_at" validate:"required"`
	Address   addressInput   `json:"address"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

func TestGenerateSchema_Representative(t *testing.T) {
	raw, err := mcp.GenerateSchema(reflect.TypeOf(representativeInput{}))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, "object", got["type"])

	props, ok := got["properties"].(map[string]any)
	require.True(t, ok, "schema must have properties map")

	// Field-level type checks.
	assert.Equal(t, map[string]any{"type": "string"}, props["name"])
	assert.Equal(t, map[string]any{"type": "integer"}, props["age"])
	assert.Equal(t, map[string]any{"type": "boolean"}, props["active"])
	assert.Equal(t, map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}, props["tags"])
	assert.Equal(t, map[string]any{
		"type":   "string",
		"format": "date-time",
	}, props["created_at"])

	// Required list comes from validate:"required" tags only.
	required, ok := got["required"].([]any)
	require.True(t, ok, "schema must declare required list")
	requiredSet := make(map[string]struct{}, len(required))
	for _, r := range required {
		requiredSet[r.(string)] = struct{}{}
	}
	assert.Contains(t, requiredSet, "name")
	assert.Contains(t, requiredSet, "created_at")
	assert.NotContains(t, requiredSet, "age", "age has no validate tag and must not be required")
}

func TestGenerateSchema_NestedStruct(t *testing.T) {
	raw, err := mcp.GenerateSchema(reflect.TypeOf(representativeInput{}))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	props := got["properties"].(map[string]any)
	address := props["address"].(map[string]any)
	assert.Equal(t, "object", address["type"])
	addrRequired, _ := address["required"].([]any)
	require.Len(t, addrRequired, 1)
	assert.Equal(t, "street", addrRequired[0])
}

func TestGenerateSchema_RejectsCycles(t *testing.T) {
	type secretTokenCyclic struct {
		Self *secretTokenCyclic `json:"self"`
	}
	_, err := mcp.GenerateSchema(reflect.TypeOf(secretTokenCyclic{}))
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrCyclicSchema), "expected ErrCyclicSchema, got %v", err)
	assert.NotContains(t, err.Error(), "secretTokenCyclic")
}

func TestGenerateSchema_RejectsCycles_NestedSlice(t *testing.T) {
	type tree struct {
		Children []tree `json:"children"`
	}
	_, err := mcp.GenerateSchema(reflect.TypeOf(tree{}))
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrCyclicSchema))
}

func TestGenerateSchema_RejectsCycles_RecursiveSliceType(t *testing.T) {
	type secretTokenRecursiveSlice []secretTokenRecursiveSlice
	_, err := mcp.GenerateSchema(reflect.TypeOf(secretTokenRecursiveSlice{}))
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrCyclicSchema))
	assert.NotContains(t, err.Error(), "secretTokenRecursiveSlice")
}

func TestGenerateSchema_RejectsCycles_RecursiveMapType(t *testing.T) {
	type secretTokenRecursiveMap map[string]secretTokenRecursiveMap
	_, err := mcp.GenerateSchema(reflect.TypeOf(secretTokenRecursiveMap{}))
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrCyclicSchema))
	assert.NotContains(t, err.Error(), "secretTokenRecursiveMap")
}

func TestGenerateSchema_RejectsUnsupportedTypesWithoutReflectingType(t *testing.T) {
	type secretTokenUnsupported struct {
		C chan int `json:"c"`
	}
	_, err := mcp.GenerateSchema(reflect.TypeOf(secretTokenUnsupported{}))
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrUnsupportedType))
	assert.NotContains(t, err.Error(), "secretTokenUnsupported")
	assert.NotContains(t, err.Error(), "chan")
}

func TestGenerateSchema_RejectsNonStringMapKeyWithoutReflectingType(t *testing.T) {
	type input struct {
		Lookup map[int]string `json:"lookup"`
	}
	_, err := mcp.GenerateSchema(reflect.TypeOf(input{}))
	require.Error(t, err)
	assert.True(t, errors.Is(err, mcp.ErrUnsupportedType))
	assert.NotContains(t, err.Error(), "int")
}

func TestGenerateSchema_OptionalPointer(t *testing.T) {
	type input struct {
		Note *string `json:"note,omitempty"`
	}
	raw, err := mcp.GenerateSchema(reflect.TypeOf(input{}))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	props := got["properties"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "string"}, props["note"])
	_, hasRequired := got["required"]
	assert.False(t, hasRequired, "optional fields must not appear in required")
}

func TestGenerateSchema_MapWithStringKey(t *testing.T) {
	type input struct {
		Tags map[string]int `json:"tags"`
	}
	raw, err := mcp.GenerateSchema(reflect.TypeOf(input{}))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	tags := got["properties"].(map[string]any)["tags"].(map[string]any)
	assert.Equal(t, "object", tags["type"])
	addl := tags["additionalProperties"].(map[string]any)
	assert.Equal(t, "integer", addl["type"])
}

func TestGenerateSchema_DescTagPropagates(t *testing.T) {
	type input struct {
		ID string `json:"id" desc:"The unique identifier."`
	}
	raw, err := mcp.GenerateSchema(reflect.TypeOf(input{}))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	id := got["properties"].(map[string]any)["id"].(map[string]any)
	assert.Equal(t, "The unique identifier.", id["description"])
}

type Embedded struct {
	Hello string `json:"hello" validate:"required"`
}

type pointerEmbedWrapper struct {
	*Embedded
	Extra string `json:"extra"`
}

func TestGenerateSchema_AnonymousPointerEmbed(t *testing.T) {
	raw, err := mcp.GenerateSchema(reflect.TypeOf(pointerEmbedWrapper{}))
	require.NoError(t, err, "anonymous pointer embed must not panic schema generation")

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	props := got["properties"].(map[string]any)
	assert.Contains(t, props, "hello", "embedded field must be flattened into parent")
	assert.Contains(t, props, "extra")
	required, _ := got["required"].([]any)
	hasHello := false
	for _, r := range required {
		if r == "hello" {
			hasHello = true
			break
		}
	}
	assert.True(t, hasHello, "embedded required field must propagate to parent's required list")
}

func TestGenerateSchema_HonorsJSONTagSkip(t *testing.T) {
	type input struct {
		Visible string `json:"visible"`
		Hidden  string `json:"-"`
	}
	raw, err := mcp.GenerateSchema(reflect.TypeOf(input{}))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	props := got["properties"].(map[string]any)
	_, hasHidden := props["Hidden"]
	assert.False(t, hasHidden)
	_, hasHiddenTagged := props["-"]
	assert.False(t, hasHiddenTagged)
	_, hasVisible := props["visible"]
	assert.True(t, hasVisible)
}
