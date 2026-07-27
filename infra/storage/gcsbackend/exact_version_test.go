package gcsbackend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExactVersionPinsGenerationAndVerifiesDeletion(t *testing.T) {
	t.Parallel()

	const generation = "1700000000000001"
	deleted := false
	var deleteGeneration string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete && isObjectMetaRequest(request) {
			deleteGeneration = request.URL.Query().Get("generation")
			deleted = true
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if isObjectMetaRequest(request) {
			if deleted && request.URL.Query().Get("generation") == generation {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, attrsJSON)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "hello")
	}))
	defer server.Close()

	backend, _ := newTestBackend(t, server)
	current, _, err := backend.CurrentVersion(context.Background(), "key")
	require.NoError(t, err)
	assert.Equal(t, generation, current.Version)

	require.NoError(t, backend.DeleteVersion(context.Background(), current))
	assert.Equal(t, generation, deleteGeneration)
}

func TestExactVersionDeleteDoesNotDeleteAnotherGeneration(t *testing.T) {
	t.Parallel()

	var deletedGeneration string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			deletedGeneration = request.URL.Query().Get("generation")
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		if request.URL.Query().Get("generation") == "1" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, attrsJSON)
	}))
	defer server.Close()

	backend, _ := newTestBackend(t, server)
	require.NoError(t, backend.DeleteVersion(context.Background(), storage.ObjectVersion{
		Key: "key", Version: "1",
	}))
	assert.Equal(t, "1", deletedGeneration)

	current, _, err := backend.CurrentVersion(context.Background(), "key")
	require.NoError(t, err)
	assert.Equal(t, "1700000000000001", current.Version)
}
