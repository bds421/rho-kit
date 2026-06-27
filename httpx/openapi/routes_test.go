package openapi_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/v2/openapi"
)

func TestEmitPathsJSON_BuildsOpenAPI31(t *testing.T) {
	out, err := openapi.EmitPathsJSON("example-api", []openapi.RouteMeta{
		{Method: "GET", Path: "/v1/contacts", Summary: "List contacts", Public: true},
		{Method: "POST", Path: "/v1/contacts", Summary: "Create contact"},
	})
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	assert.Equal(t, "3.1.0", doc["openapi"])
	paths, ok := doc["paths"].(map[string]any)
	require.True(t, ok)
	get, ok := paths["/v1/contacts"].(map[string]any)["get"].(map[string]any)
	require.True(t, ok)
	tags, ok := get["tags"].([]any)
	require.True(t, ok)
	assert.Contains(t, tags, "public")
}

func TestEmitPathsJSON_RejectsDuplicateRoute(t *testing.T) {
	_, err := openapi.EmitPathsJSON("example-api", []openapi.RouteMeta{
		{Method: "GET", Path: "/v1/contacts"},
		{Method: "GET", Path: "/v1/contacts"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate route")
}
