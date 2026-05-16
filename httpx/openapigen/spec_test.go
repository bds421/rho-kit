package openapigen_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/validate"
	"github.com/bds421/rho-kit/httpx/v2/openapigen"
)

type createWidgetReq struct {
	Name  string `json:"name" validate:"required,min=2,max=64"`
	Price int    `json:"price" validate:"required,min=0"`
}

type widgetResp struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Price int    `json:"price"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewSpec_PopulatesInfo(t *testing.T) {
	spec := openapigen.NewSpec("test-api", "v1.2.3")
	doc := spec.Document()
	assert.Equal(t, "3.1.0", doc.OpenAPI)
	assert.Equal(t, "test-api", doc.Info.Title)
	assert.Equal(t, "v1.2.3", doc.Info.Version)
}

func TestNewSpec_RejectsEmptyTitle(t *testing.T) {
	assert.Panics(t, func() {
		_ = openapigen.NewSpec("", "v1.0.0")
	})
}

func TestNewSpec_RejectsEmptyVersion(t *testing.T) {
	assert.Panics(t, func() {
		_ = openapigen.NewSpec("api", "")
	})
}

func TestRegister_BasicRoute(t *testing.T) {
	spec := openapigen.NewSpec("widgets", "v1")
	err := spec.Register(http.MethodPost, "/widgets",
		openapigen.WithRequestType[createWidgetReq](),
		openapigen.WithResponseType[widgetResp](http.StatusCreated),
		openapigen.WithSummary("Create a widget"),
		openapigen.WithOperationID("createWidget"),
		openapigen.WithTags("widgets"),
	)
	require.NoError(t, err)

	doc := spec.Document()
	item, ok := doc.Paths["/widgets"]
	require.True(t, ok, "path /widgets must exist")
	require.NotNil(t, item.Post)

	op := item.Post
	assert.Equal(t, "Create a widget", op.Summary)
	assert.Equal(t, "createWidget", op.OperationID)
	assert.Equal(t, []string{"widgets"}, op.Tags)

	require.NotNil(t, op.RequestBody)
	require.NotNil(t, op.RequestBody.Content)
	mt, ok := op.RequestBody.Content["application/json"]
	require.True(t, ok)
	require.NotNil(t, mt.Schema)
	assert.Equal(t, "object", mt.Schema.Type)
	assert.Contains(t, mt.Schema.Properties, "name")
	assert.Contains(t, mt.Schema.Properties, "price")

	require.NotNil(t, op.Responses)
	resp201, ok := op.Responses["201"]
	require.True(t, ok)
	assert.NotEmpty(t, resp201.Description)
	mt = resp201.Content["application/json"]
	require.NotNil(t, mt.Schema)
	assert.Equal(t, "object", mt.Schema.Type)
}

func TestRegister_RejectsDuplicate(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, spec.Register(http.MethodGet, "/x"))
	err := spec.Register(http.MethodGet, "/x")
	assert.ErrorIs(t, err, openapigen.ErrRouteAlreadyRegistered)
}

func TestRegister_RejectsEmptyPath(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	err := spec.Register(http.MethodGet, "")
	assert.ErrorIs(t, err, openapigen.ErrEmptyPath)
}

func TestRegister_RejectsInvalidMethod(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	err := spec.Register("FOO", "/x")
	assert.ErrorIs(t, err, openapigen.ErrInvalidMethod)
}

func TestRegister_NormalisesMethodCase(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, spec.Register("post", "/x",
		openapigen.WithSummary("create"),
	))
	doc := spec.Document()
	require.NotNil(t, doc.Paths["/x"].Post)
}

func TestRegister_AllVerbs(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	verbs := []string{
		http.MethodGet, http.MethodPut, http.MethodPost,
		http.MethodDelete, http.MethodOptions, http.MethodHead,
		http.MethodPatch, http.MethodTrace,
	}
	for _, v := range verbs {
		require.NoError(t, spec.Register(v, "/x/"+strings.ToLower(v)))
	}
	doc := spec.Document()
	assert.Len(t, doc.Paths, len(verbs))
}

func TestRegister_MultipleMethodsSamePath(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, spec.Register(http.MethodGet, "/widgets",
		openapigen.WithResponseType[widgetResp](http.StatusOK),
	))
	require.NoError(t, spec.Register(http.MethodPost, "/widgets",
		openapigen.WithRequestType[createWidgetReq](),
		openapigen.WithResponseType[widgetResp](http.StatusCreated),
	))
	doc := spec.Document()
	item := doc.Paths["/widgets"]
	assert.NotNil(t, item.Get)
	assert.NotNil(t, item.Post)
}

func TestRegister_PathParameter(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	param := openapigen.Parameter{
		Name:        "id",
		In:          "path",
		Description: "Widget identifier",
	}
	require.NoError(t, spec.Register(http.MethodGet, "/widgets/{id}",
		openapigen.WithParameter(param),
		openapigen.WithResponseType[widgetResp](http.StatusOK),
	))
	doc := spec.Document()
	item := doc.Paths["/widgets/{id}"]
	require.NotNil(t, item.Get)
	require.Len(t, item.Get.Parameters, 1)
	assert.Equal(t, "id", item.Get.Parameters[0].Name)
	assert.True(t, item.Get.Parameters[0].Required, "path parameter must be auto-required")
}

func TestRegister_RejectsBadParameter(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	err := spec.Register(http.MethodGet, "/x",
		openapigen.WithParameter(openapigen.Parameter{Name: "id", In: "INVALID"}),
	)
	assert.Error(t, err)
}

func TestRegister_RejectsEmptyTag(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	err := spec.Register(http.MethodGet, "/x",
		openapigen.WithTags(""),
	)
	assert.Error(t, err)
}

func TestRegister_ResponseStatusWithoutSchema(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, spec.Register(http.MethodDelete, "/widgets/{id}",
		openapigen.WithResponseStatus(http.StatusNoContent, "Widget deleted"),
	))
	doc := spec.Document()
	resp := doc.Paths["/widgets/{id}"].Delete.Responses["204"]
	assert.Equal(t, "Widget deleted", resp.Description)
	assert.Nil(t, resp.Content)
}

func TestRegister_DefaultDescription(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, spec.Register(http.MethodGet, "/x",
		openapigen.WithResponseType[widgetResp](http.StatusOK),
	))
	doc := spec.Document()
	resp := doc.Paths["/x"].Get.Responses["200"]
	assert.NotEmpty(t, resp.Description, "must fall back to non-empty default")
}

func TestMarshal_RendersDeterministicJSON(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1.0.0")
	require.NoError(t, spec.Register(http.MethodPost, "/widgets",
		openapigen.WithRequestType[createWidgetReq](),
		openapigen.WithResponseType[widgetResp](http.StatusCreated),
		openapigen.WithResponseType[errorBody](http.StatusBadRequest),
	))

	a, err := spec.Marshal()
	require.NoError(t, err)
	b, err := spec.Marshal()
	require.NoError(t, err)
	assert.Equal(t, string(a), string(b), "two Marshal calls must produce identical bytes")

	// Confirm cache mutation doesn't leak to the caller.
	a[0] = 'X'
	c, err := spec.Marshal()
	require.NoError(t, err)
	assert.Equal(t, string(b), string(c), "Marshal must defensively copy")
}

func TestMarshal_CacheInvalidatedOnMutation(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, spec.Register(http.MethodGet, "/x"))
	a, err := spec.Marshal()
	require.NoError(t, err)
	require.NoError(t, spec.Register(http.MethodGet, "/y"))
	b, err := spec.Marshal()
	require.NoError(t, err)
	assert.NotEqual(t, string(a), string(b), "mutation must invalidate cache")
}

func TestHandler_ServesJSON(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1.0.0")
	require.NoError(t, spec.Register(http.MethodGet, "/x"))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	spec.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", res.Header.Get("Content-Type"))

	var doc openapigen.Document
	require.NoError(t, json.NewDecoder(res.Body).Decode(&doc))
	assert.Equal(t, "3.1.0", doc.OpenAPI)
	assert.Equal(t, "api", doc.Info.Title)
}

func TestHandler_HEADReturnsNoBody(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1.0.0")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/openapi.json", nil)
	spec.Handler().ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	body, _ := io.ReadAll(res.Body)
	assert.Empty(t, body, "HEAD must not emit a body")
}

func TestHandler_RejectsNonGetMethods(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1.0.0")
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(m, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(m, "/openapi.json", nil)
			spec.Handler().ServeHTTP(rec, req)
			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
			assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
		})
	}
}

func TestSpec_Components(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	spec.AddSecurityScheme("bearerAuth", openapigen.SecurityScheme{
		Type:         "http",
		Scheme:       "bearer",
		BearerFormat: "JWT",
	})
	require.NoError(t, spec.Register(http.MethodGet, "/x",
		openapigen.WithSecurity(map[string][]string{"bearerAuth": {}}),
	))
	doc := spec.Document()
	require.NotNil(t, doc.Components)
	scheme, ok := doc.Components.SecuritySchemes["bearerAuth"]
	require.True(t, ok)
	assert.Equal(t, "http", scheme.Type)
	assert.Equal(t, "bearer", scheme.Scheme)
	assert.Equal(t, "JWT", scheme.BearerFormat)

	op := doc.Paths["/x"].Get
	require.NotNil(t, op.Security)
	require.Len(t, *op.Security, 1)
	assert.Equal(t, []string{}, (*op.Security)[0]["bearerAuth"])
}

func TestSpec_GlobalSecurity(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	spec.SetGlobalSecurity([]map[string][]string{{"bearerAuth": {}}})
	doc := spec.Document()
	require.Len(t, doc.Security, 1)
}

func TestSpec_AnonymousOperationOverride(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	spec.SetGlobalSecurity([]map[string][]string{{"bearerAuth": {}}})
	require.NoError(t, spec.Register(http.MethodGet, "/public",
		openapigen.WithSecurity(),
	))
	doc := spec.Document()
	op := doc.Paths["/public"].Get
	require.NotNil(t, op.Security, "anonymous override must surface as a non-nil pointer")
	assert.Len(t, *op.Security, 0, "anonymous override must emit empty (not nil) slice")

	// Critically: the Marshal output must include `security: []` so OAS
	// readers do not fall back to the document-level requirement.
	raw, err := spec.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"security":[]`,
		"anonymous override must round-trip through JSON as an empty array, not be omitted")
}

func TestSpec_ServersAndTags(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	spec.AddServer(openapigen.Server{URL: "https://api.example.com", Description: "prod"})
	spec.AddTag(openapigen.Tag{Name: "widgets", Description: "Widget CRUD"})
	doc := spec.Document()
	require.Len(t, doc.Servers, 1)
	require.Len(t, doc.Tags, 1)
	assert.Equal(t, "widgets", doc.Tags[0].Name)
}

func TestHandle_RegistersOnMuxAndSpec(t *testing.T) {
	mux := http.NewServeMux()
	spec := openapigen.NewSpec("api", "v1")
	logger := newLogger()

	err := openapigen.Handle[createWidgetReq, widgetResp](mux, spec,
		http.MethodPost, "/widgets", logger,
		func(_ context.Context, _ *http.Request, in createWidgetReq) (widgetResp, error) {
			return widgetResp{ID: "w-1", Name: in.Name, Price: in.Price}, nil
		},
		openapigen.WithSummary("Create a widget"),
	)
	require.NoError(t, err)

	// Spec recorded.
	doc := spec.Document()
	require.NotNil(t, doc.Paths["/widgets"].Post)

	// Mux serves the handler.
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"name":"axe","price":42}`)
	req := httptest.NewRequest(http.MethodPost, "/widgets", body)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var got widgetResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "w-1", got.ID)
	assert.Equal(t, "axe", got.Name)
}

func TestHandle_RejectsNilMux(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	err := openapigen.Handle[createWidgetReq, widgetResp](nil, spec,
		http.MethodPost, "/x", newLogger(),
		func(_ context.Context, _ *http.Request, _ createWidgetReq) (widgetResp, error) {
			return widgetResp{}, nil
		},
	)
	assert.Error(t, err)
}

func TestHandle_RejectsNilSpec(t *testing.T) {
	mux := http.NewServeMux()
	err := openapigen.Handle[createWidgetReq, widgetResp](mux, nil,
		http.MethodPost, "/x", newLogger(),
		func(_ context.Context, _ *http.Request, _ createWidgetReq) (widgetResp, error) {
			return widgetResp{}, nil
		},
	)
	assert.Error(t, err)
}

func TestHandleStatus_DefaultsTo200(t *testing.T) {
	mux := http.NewServeMux()
	spec := openapigen.NewSpec("api", "v1")

	require.NoError(t, openapigen.HandleStatus[createWidgetReq, widgetResp](mux, spec,
		http.MethodPost, "/widgets", newLogger(),
		func(_ context.Context, _ *http.Request, in createWidgetReq) (int, widgetResp, error) {
			return http.StatusCreated, widgetResp{Name: in.Name}, nil
		},
	))

	doc := spec.Document()
	resp := doc.Paths["/widgets"].Post.Responses
	_, has200 := resp["200"]
	assert.True(t, has200, "default response status is 200 when caller does not specify")
}

func TestHandleStatus_RespectsCallerStatus(t *testing.T) {
	mux := http.NewServeMux()
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, openapigen.HandleStatus[createWidgetReq, widgetResp](mux, spec,
		http.MethodPost, "/widgets", newLogger(),
		func(_ context.Context, _ *http.Request, _ createWidgetReq) (int, widgetResp, error) {
			return http.StatusCreated, widgetResp{}, nil
		},
		openapigen.WithResponseType[widgetResp](http.StatusCreated),
	))

	doc := spec.Document()
	resp := doc.Paths["/widgets"].Post.Responses
	_, has201 := resp["201"]
	_, has200 := resp["200"]
	assert.True(t, has201)
	assert.False(t, has200, "default 200 must not be added when caller supplied a status")
}

func TestHandleNoBody_RegistersOK(t *testing.T) {
	mux := http.NewServeMux()
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, openapigen.HandleNoBody[widgetResp](mux, spec,
		http.MethodGet, "/widgets/{id}", newLogger(),
		func(_ context.Context, _ *http.Request) (widgetResp, error) {
			return widgetResp{ID: "w-1"}, nil
		},
		openapigen.WithParameter(openapigen.Parameter{Name: "id", In: "path"}),
	))
	doc := spec.Document()
	op := doc.Paths["/widgets/{id}"].Get
	require.NotNil(t, op)
	assert.Nil(t, op.RequestBody, "no-body handler must not register a requestBody")
	require.Len(t, op.Parameters, 1)
}

func TestHandleNoContent_RegistersOK(t *testing.T) {
	mux := http.NewServeMux()
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, openapigen.HandleNoContent(mux, spec,
		http.MethodDelete, "/widgets/{id}", newLogger(),
		func(_ context.Context, _ *http.Request) error { return nil },
	))
	doc := spec.Document()
	op := doc.Paths["/widgets/{id}"].Delete
	require.NotNil(t, op)
	require.Contains(t, op.Responses, "204")
}

func TestHandle_FailsFastOnSchemaError(t *testing.T) {
	type cyclic struct {
		Self *cyclic `json:"self"`
	}
	mux := http.NewServeMux()
	spec := openapigen.NewSpec("api", "v1")
	err := openapigen.Handle[cyclic, widgetResp](mux, spec,
		http.MethodPost, "/x", newLogger(),
		func(_ context.Context, _ *http.Request, _ cyclic) (widgetResp, error) {
			return widgetResp{}, nil
		},
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, openapigen.ErrSchemaGeneration),
		"schema build failure must wrap ErrSchemaGeneration; got: %v", err)
	assert.True(t, errors.Is(err, validate.ErrCyclicSchema),
		"schema build failure must preserve the underlying validate sentinel; got: %v", err)
}

func TestWithRequestOptional_FlipsRequiredFalse(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	// WithRequestType defaults Required=true; WithRequestOptional must
	// flip it back to false. Order matters — the doc comment promises
	// WithRequestOptional pairs with WithRequestType, applied later.
	require.NoError(t, spec.Register(http.MethodPost, "/x",
		openapigen.WithRequestType[createWidgetReq](),
		openapigen.WithRequestOptional(),
	))
	doc := spec.Document()
	rb := doc.Paths["/x"].Post.RequestBody
	require.NotNil(t, rb)
	assert.False(t, rb.Required, "WithRequestOptional must clear the required flag")
}

func TestWithRequestMediaType_OverridesDefault(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, spec.Register(http.MethodPost, "/x",
		openapigen.WithRequestType[createWidgetReq](),
		openapigen.WithRequestMediaType("application/x-www-form-urlencoded"),
	))
	doc := spec.Document()
	rb := doc.Paths["/x"].Post.RequestBody
	require.NotNil(t, rb)
	_, ok := rb.Content["application/x-www-form-urlencoded"]
	assert.True(t, ok, "WithRequestMediaType must override the default application/json key")
	_, jsonKey := rb.Content["application/json"]
	assert.False(t, jsonKey, "default media type must not also appear")
}

func TestWithRequestSchema_RejectsNil(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	err := spec.Register(http.MethodPost, "/x", openapigen.WithRequestSchema(nil))
	require.Error(t, err)
}

func TestWithResponseMediaType_OverridesDefault(t *testing.T) {
	spec := openapigen.NewSpec("api", "v1")
	require.NoError(t, spec.Register(http.MethodGet, "/x",
		openapigen.WithResponseType[widgetResp](http.StatusOK),
		openapigen.WithResponseMediaType(http.StatusOK, "application/yaml"),
	))
	doc := spec.Document()
	resp := doc.Paths["/x"].Get.Responses["200"]
	_, ok := resp.Content["application/yaml"]
	assert.True(t, ok)
}

func TestRegister_RoundTripJSONShape(t *testing.T) {
	spec := openapigen.NewSpec("widgets", "v1.0.0")
	require.NoError(t, spec.Register(http.MethodPost, "/widgets",
		openapigen.WithRequestType[createWidgetReq](),
		openapigen.WithResponseType[widgetResp](http.StatusCreated),
		openapigen.WithSummary("Create"),
	))

	buf, err := spec.Marshal()
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(buf, &raw))
	assert.Equal(t, "3.1.0", raw["openapi"])

	paths, _ := raw["paths"].(map[string]any)
	widgets, _ := paths["/widgets"].(map[string]any)
	post, _ := widgets["post"].(map[string]any)
	require.NotNil(t, post)

	rb, _ := post["requestBody"].(map[string]any)
	require.NotNil(t, rb)
	assert.Equal(t, true, rb["required"])
	content, _ := rb["content"].(map[string]any)
	json, _ := content["application/json"].(map[string]any)
	require.NotNil(t, json)
	schema, _ := json["schema"].(map[string]any)
	assert.Equal(t, "object", schema["type"])
}
