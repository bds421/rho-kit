package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAPIKeyDemo_AuthenticatesIssuedKey verifies the end-to-end opaque-key
// flow: the route issues a key at construction, and a request bearing that
// key (which carries the orders.read scope) reaches the handler.
func TestAPIKeyDemo_AuthenticatesIssuedKey(t *testing.T) {
	handler, token, err := newAPIKeyDemoHandler(context.Background(), slog.Default())
	require.NoError(t, err)
	require.NotEmpty(t, token)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/keys-demo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestAPIKeyDemo_RejectsMissingAndWrongKey verifies unauthenticated and
// bad-key requests are rejected with 401.
func TestAPIKeyDemo_RejectsMissingAndWrongKey(t *testing.T) {
	handler, token, err := newAPIKeyDemoHandler(context.Background(), slog.Default())
	require.NoError(t, err)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	cases := map[string]string{
		"missing":      "",
		"wrong secret": "Bearer " + token + "tampered",
	}
	for name, auth := range cases {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/keys-demo", nil)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}
