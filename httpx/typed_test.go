package httpx_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/httpx"
)

type createUserReq struct {
	Name string `json:"name" validate:"required"`
	Age  int    `json:"age" validate:"gte=0"`
}

type userResp struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- Mux-bound tests (backward compat) ---

func TestHandle_Success(t *testing.T) {
	mux := http.NewServeMux()
	httpx.Handle(mux, "POST /users", testLogger(), func(_ context.Context, _ *http.Request, req createUserReq) (userResp, error) {
		return userResp{ID: 1, Name: req.Name, Age: req.Age}, nil
	})

	body := `{"name":"Alice","age":30}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp userResp
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "Alice", resp.Name)
	assert.Equal(t, 30, resp.Age)
}

func TestHandle_ValidationError(t *testing.T) {
	mux := http.NewServeMux()
	httpx.Handle(mux, "POST /users", testLogger(), func(_ context.Context, _ *http.Request, _ createUserReq) (userResp, error) {
		t.Fatal("handler should not be called")
		return userResp{}, nil
	})

	body := `{"age":30}` // missing required "name"
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandle_InvalidJSON(t *testing.T) {
	mux := http.NewServeMux()
	httpx.Handle(mux, "POST /users", testLogger(), func(_ context.Context, _ *http.Request, _ createUserReq) (userResp, error) {
		t.Fatal("handler should not be called")
		return userResp{}, nil
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(`not json`))
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandle_HandlerError(t *testing.T) {
	mux := http.NewServeMux()
	httpx.Handle(mux, "POST /users", testLogger(), func(_ context.Context, _ *http.Request, _ createUserReq) (userResp, error) {
		return userResp{}, apperror.NewNotFound("user", "123")
	})

	body := `{"name":"Alice","age":30}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleNoBody_Success(t *testing.T) {
	mux := http.NewServeMux()
	httpx.HandleNoBody(mux, "GET /users/{id}", testLogger(), func(_ context.Context, _ *http.Request) (userResp, error) {
		return userResp{ID: 1, Name: "Alice"}, nil
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users/1", nil)
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp userResp
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "Alice", resp.Name)
}

func TestHandleNoBody_Error(t *testing.T) {
	mux := http.NewServeMux()
	httpx.HandleNoBody(mux, "GET /users/{id}", testLogger(), func(_ context.Context, _ *http.Request) (userResp, error) {
		return userResp{}, apperror.NewNotFound("user", "1")
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users/1", nil)
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleStatus_Created(t *testing.T) {
	mux := http.NewServeMux()
	httpx.HandleStatus(mux, "POST /users", testLogger(), func(_ context.Context, _ *http.Request, req createUserReq) (int, userResp, error) {
		return http.StatusCreated, userResp{ID: 1, Name: req.Name}, nil
	})

	body := `{"name":"Alice","age":25}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp userResp
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "Alice", resp.Name)
}

func TestHandleStatus_Error(t *testing.T) {
	mux := http.NewServeMux()
	httpx.HandleStatus(mux, "POST /users", testLogger(), func(_ context.Context, _ *http.Request, _ createUserReq) (int, userResp, error) {
		return 0, userResp{}, apperror.NewValidation("invalid input")
	})

	body := `{"name":"Alice","age":25}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// --- Decoupled handler tests ---

func TestJSON_Success(t *testing.T) {
	h := httpx.JSON(testLogger(), func(_ context.Context, _ *http.Request, req createUserReq) (userResp, error) {
		return userResp{ID: 1, Name: req.Name, Age: req.Age}, nil
	})

	body := `{"name":"Bob","age":25}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp userResp
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "Bob", resp.Name)
}

func TestJSON_ValidationError(t *testing.T) {
	h := httpx.JSON(testLogger(), func(_ context.Context, _ *http.Request, _ createUserReq) (userResp, error) {
		t.Fatal("handler should not be called")
		return userResp{}, nil
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(`{"age":30}`))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestJSONNoBody_Success(t *testing.T) {
	h := httpx.JSONNoBody(testLogger(), func(_ context.Context, _ *http.Request) (userResp, error) {
		return userResp{ID: 1, Name: "Charlie"}, nil
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/users/1", nil)
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp userResp
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "Charlie", resp.Name)
}

func TestJSONStatus_Created(t *testing.T) {
	h := httpx.JSONStatus(testLogger(), func(_ context.Context, _ *http.Request, req createUserReq) (int, userResp, error) {
		return http.StatusCreated, userResp{ID: 1, Name: req.Name}, nil
	})

	body := `{"name":"Diana","age":28}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/users", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestJSONNoBodyStatus_Accepted(t *testing.T) {
	h := httpx.JSONNoBodyStatus(testLogger(), func(_ context.Context, _ *http.Request) (int, userResp, error) {
		return http.StatusAccepted, userResp{ID: 1, Name: "Eve"}, nil
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tasks/1/start", nil)
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	var resp userResp
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "Eve", resp.Name)
}

func TestJSONNoBodyStatus_Error(t *testing.T) {
	h := httpx.JSONNoBodyStatus(testLogger(), func(_ context.Context, _ *http.Request) (int, userResp, error) {
		return 0, userResp{}, apperror.NewNotFound("task", "1")
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/tasks/1/start", nil)
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestNoContent_Success(t *testing.T) {
	h := httpx.NoContent(testLogger(), func(_ context.Context, _ *http.Request) error {
		return nil
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/users/1", nil)
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestNoContent_Error(t *testing.T) {
	h := httpx.NoContent(testLogger(), func(_ context.Context, _ *http.Request) error {
		return apperror.NewNotFound("user", "1")
	})

	rec := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/users/1", nil)
	h.ServeHTTP(rec, r)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
