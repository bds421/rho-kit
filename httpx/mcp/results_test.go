package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/v2/mcp"
)

func TestToolError_CallerReadsTheChosenText(t *testing.T) {
	s := mcp.NewServer(mcp.WithBestEffortAuditOnMissingTenant())
	cause := errors.New("db: connection to 10.0.0.5 refused")
	require.NoError(t, mcp.Register(s, "fail", func(context.Context, echoIn) (echoOut, error) {
		return echoOut{}, mcp.NewToolError("no channel #ops; find_channel lists them", cause)
	}))
	res, rpcErr := callTool(t, s.HTTP(), "fail", map[string]any{"message": "x"}, nil)
	require.Nil(t, rpcErr.Error)
	assert.True(t, res.IsError)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "no channel #ops; find_channel lists them", res.Content[0].Text, "the cause stays on the server")
	assert.ErrorIs(t, mcp.NewToolError("x", cause), cause)
}

// contentItem is a content item of any type.
type contentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     []byte `json:"data"`
	MIMEType string `json:"mimeType"`
	URI      string `json:"uri"`
	Size     int64  `json:"size"`
	Resource struct {
		URI  string `json:"uri"`
		Blob []byte `json:"blob"`
	} `json:"resource"`
}

func TestContents_AnswerWithItemsInsteadOfJSON(t *testing.T) {
	s := mcp.NewServer(mcp.WithBestEffortAuditOnMissingTenant())
	require.NoError(t, mcp.Register(s, "file", func(context.Context, echoIn) (mcp.Contents, error) {
		return mcp.Contents{
			mcp.Text("chart.png"),
			mcp.Image{Data: []byte("\x89PNG"), MIMEType: "image/png"},
			mcp.Resource{URI: "urn:example:file:1", MIMEType: "application/pdf", Blob: []byte("%PDF")},
			mcp.ResourceLink{URI: "https://example.com/files/2", Name: "big.zip", Size: 1 << 30},
		}, nil
	}))
	for _, tool := range s.Tools() {
		assert.Empty(t, tool.OutputSchema, "content results have no output schema")
	}
	resp := doRPC(t, s.HTTP(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"file","arguments":{"message":"x"}}}`)
	var res struct {
		Content           []contentItem   `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &res))
	require.False(t, res.IsError)
	assert.Empty(t, res.StructuredContent)
	require.Len(t, res.Content, 4)
	assert.Equal(t, "text", res.Content[0].Type)
	assert.Equal(t, []byte("\x89PNG"), res.Content[1].Data)
	assert.Equal(t, "image/png", res.Content[1].MIMEType)
	assert.Equal(t, "resource", res.Content[2].Type)
	assert.Equal(t, []byte("%PDF"), res.Content[2].Resource.Blob)
	assert.Equal(t, "resource_link", res.Content[3].Type)
	assert.EqualValues(t, 1<<30, res.Content[3].Size)
}

func listTools(t *testing.T, h http.Handler, setup func(*http.Request)) []map[string]any {
	t.Helper()
	resp := doRPCRequest(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, setup)
	var res struct {
		Tools []map[string]any `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &res))
	return res.Tools
}

type roleKey struct{}

func TestWithToolFilter_ListsPerCaller(t *testing.T) {
	s := mcp.NewServer(mcp.WithBestEffortAuditOnMissingTenant(), mcp.WithToolFilter(func(ctx context.Context, name string) bool {
		return name == "echo" || ctx.Value(roleKey{}) == "admin"
	}))
	require.NoError(t, mcp.Register(s, "echo", echoHandler))
	require.NoError(t, mcp.Register(s, "admin_only", echoHandler))
	names := func(setup func(*http.Request)) []string {
		var out []string
		for _, tool := range listTools(t, s.HTTP(), setup) {
			out = append(out, tool["name"].(string))
		}
		return out
	}
	assert.Equal(t, []string{"echo"}, names(nil))
	admin := func(r *http.Request) { *r = *r.WithContext(context.WithValue(r.Context(), roleKey{}, "admin")) }
	assert.ElementsMatch(t, []string{"echo", "admin_only"}, names(admin))
	assert.Panics(t, func() { mcp.WithToolFilter(nil) })
}

func TestWithAnnotations_SendsEveryHint(t *testing.T) {
	s := mcp.NewServer(mcp.WithBestEffortAuditOnMissingTenant(), mcp.WithoutDestructiveGate())
	require.NoError(t, mcp.Register(s, "read", echoHandler, mcp.WithAnnotations(mcp.Annotations{Title: "Read", ReadOnly: true, Idempotent: true})))
	require.NoError(t, mcp.Register(s, "remove", echoHandler, mcp.WithDestructive(), mcp.WithAnnotations(mcp.Annotations{Title: "Remove", OpenWorld: true})))
	require.NoError(t, mcp.Register(s, "plain", echoHandler))
	got := map[string]map[string]any{}
	for _, tool := range listTools(t, s.HTTP(), nil) {
		got[tool["name"].(string)] = tool
	}
	assert.Equal(t, "Read", got["read"]["title"])
	assert.Equal(t, map[string]any{"title": "Read", "readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		got["read"]["annotations"])
	assert.Equal(t, map[string]any{"title": "Remove", "readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true},
		got["remove"]["annotations"])
	assert.NotContains(t, got["plain"], "annotations")
	for _, tool := range s.Tools() {
		if tool.Name == "read" {
			assert.Equal(t, "Read", tool.Title)
		}
	}
}

func TestWithMaxRequestBytes_BoundsARequest(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"` + strings.Repeat("x", 2048) + `"}}}`
	post := func(h http.Handler) int {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	assert.Equal(t, http.StatusRequestEntityTooLarge, post(newTestServer(t, mcp.WithBestEffortAuditOnMissingTenant(), mcp.WithMaxRequestBytes(1024)).HTTP()))
	assert.Equal(t, http.StatusOK, post(newTestServer(t, mcp.WithBestEffortAuditOnMissingTenant(), mcp.WithMaxRequestBytes(4096)).HTTP()))
	assert.Panics(t, func() { mcp.WithMaxRequestBytes(-1) })
}

type panickingContent struct{}

func (panickingContent) ToolContent() []mcp.Content { panic("content broke") }

func TestContents_APanicFailsTheCallAndNilItemsAreSkipped(t *testing.T) {
	s := mcp.NewServer(mcp.WithBestEffortAuditOnMissingTenant())
	require.NoError(t, mcp.Register(s, "broken", func(context.Context, echoIn) (panickingContent, error) {
		return panickingContent{}, nil
	}))
	var nilImage *mcp.Image
	require.NoError(t, mcp.Register(s, "holes", func(context.Context, echoIn) (mcp.Contents, error) {
		return mcp.Contents{nil, nilImage, mcp.Text("kept")}, nil
	}))
	res, rpc := callTool(t, s.HTTP(), "broken", map[string]any{"message": "x"}, nil)
	require.Nil(t, rpc.Error)
	assert.True(t, res.IsError)
	assert.Equal(t, "internal error", res.Content[0].Text)
	res, rpc = callTool(t, s.HTTP(), "holes", map[string]any{"message": "x"}, nil)
	require.Nil(t, rpc.Error)
	require.Len(t, res.Content, 1)
	assert.Equal(t, "kept", res.Content[0].Text)
}
