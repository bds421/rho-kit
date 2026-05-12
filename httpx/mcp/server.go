package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/validate"
)

// JSON-RPC 2.0 error codes. The MCP spec piggybacks on the standard
// codes with no additions, so callers reading the wire format see
// the same numbers a generic JSON-RPC client expects.
const (
	rpcErrParseError     = -32700
	rpcErrInvalidRequest = -32600
	rpcErrMethodNotFound = -32601
	rpcErrInvalidParams  = -32602
	rpcErrInternalError  = -32603
)

// jsonRPCRequest is the inbound request envelope.
//
// ID is read as json.RawMessage so we can distinguish "absent"
// (notification — len(ID) == 0) from "explicit null" (a valid
// JSON-RPC id whose value happens to be null). Per the JSON-RPC 2.0
// spec, notifications MUST NOT receive a response.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
}

// isNotification reports whether the request's `id` member was absent
// from the wire. An absent id makes the request a notification and
// the server MUST NOT write a response (JSON-RPC 2.0 §4.1).
func isNotification(id json.RawMessage) bool {
	return len(id) == 0
}

// jsonRPCResponse is the outbound response envelope. ID is preserved
// from the request (or null for notifications).
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError is the standard JSON-RPC 2.0 error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// HTTP returns the http.Handler for the JSON-RPC endpoint. Mount it
// from the same mux as the REST API — both share the kit's
// middleware stack, so auth/tenant/rate-limit decisions agree.
//
// The handler accepts only POST. Other methods produce 405. The
// response body is always JSON-RPC-shaped, even on transport errors,
// so callers parsing the response don't need a special branch for
// "did the kit respond at all?"
func (s *Server) HTTP() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeJSONRPCError(w, http.StatusMethodNotAllowed, json.RawMessage("null"),
				rpcErrInvalidRequest, "method not allowed; use POST")
			return
		}

		body, err := readBody(r, s.cfg.maxRequestBytes)
		if err != nil {
			writeJSONRPCError(w, http.StatusOK, json.RawMessage("null"),
				rpcErrParseError, safeReadBodyMessage(err))
			return
		}

		// Reject obvious batch requests (a JSON array). Single-call
		// semantics keep the action-log entry per-call rather than
		// per-batch — explicitly deferred per the package doc.
		if isJSONArray(body) {
			writeJSONRPCError(w, http.StatusOK, json.RawMessage("null"),
				rpcErrInvalidRequest, "batch requests are not supported")
			return
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			// Spec exception: parse errors permit id:null even when
			// the caller's id was unparseable.
			writeJSONRPCError(w, http.StatusOK, json.RawMessage("null"),
				rpcErrParseError, "invalid JSON")
			return
		}
		// JSON-RPC 2.0 notifications: a request without an `id` member
		// MUST NOT receive a response. Track presence (vs. null) by
		// reading id as a json.RawMessage and checking len.
		notification := isNotification(req.ID)
		if req.JSONRPC != "2.0" {
			if notification {
				return
			}
			writeJSONRPCError(w, http.StatusOK, req.ID,
				rpcErrInvalidRequest, "jsonrpc must be \"2.0\"")
			return
		}
		if req.Method == "" {
			if notification {
				return
			}
			writeJSONRPCError(w, http.StatusOK, req.ID,
				rpcErrInvalidRequest, "method is required")
			return
		}

		// For notifications, swap in a writer that drops the response
		// body so dispatch and invoke can keep their existing
		// "always emit a JSON-RPC response" shape — the audit and
		// strict-mode checks still need to run, but the bytes never
		// leave the server.
		if notification {
			w = &nullResponseWriter{header: http.Header{}}
		}
		s.dispatch(w, r, req)
	})
}

// nullResponseWriter discards all writes. Used to suppress JSON-RPC
// responses for notifications (requests without an `id` member) while
// preserving the rest of the dispatch path — audit, strict-mode checks,
// and tool execution still run.
type nullResponseWriter struct {
	header http.Header
}

func (n *nullResponseWriter) Header() http.Header         { return n.header }
func (n *nullResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (n *nullResponseWriter) WriteHeader(int)             {}

// dispatch routes a JSON-RPC method to the appropriate handler.
// Recognised methods:
//   - "initialize" → server capabilities.
//   - "tools/list" → tool catalog.
//   - "tools/call" → invoke a registered tool by name carried in
//     params.
//   - "<tool-name>" → shorthand: invoke the named tool directly,
//     params are the tool input.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, req jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(w, req)
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(w, r, req)
	default:
		entry, ok := s.lookup(req.Method)
		if !ok {
			writeJSONRPCError(w, http.StatusOK, req.ID,
				rpcErrMethodNotFound, "method not found")
			return
		}
		s.invoke(w, r, req, req.Method, entry, req.Params, false)
	}
}

// handleInitialize returns server capabilities. Minimal MCP
// implementation: tools, no prompts/resources.
func (s *Server) handleInitialize(w http.ResponseWriter, req jsonRPCRequest) {
	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "rho-kit/mcp",
			"version": "v0.1.0",
		},
	}
	writeJSONRPCResult(w, req.ID, result)
}

// handleToolsList returns the tool catalog.
func (s *Server) handleToolsList(w http.ResponseWriter, req jsonRPCRequest) {
	tools := s.Tools()
	result := map[string]any{"tools": tools}
	writeJSONRPCResult(w, req.ID, result)
}

// toolsCallParams is the params shape for the "tools/call" method.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// handleToolsCall handles `tools/call`.
func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req jsonRPCRequest) {
	var params toolsCallParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			writeJSONRPCError(w, http.StatusOK, req.ID,
				rpcErrInvalidParams, "params must be an object with `name` and `arguments`")
			return
		}
	}
	if params.Name == "" {
		writeJSONRPCError(w, http.StatusOK, req.ID,
			rpcErrInvalidParams, "tool name is required")
		return
	}
	entry, ok := s.lookup(params.Name)
	if !ok {
		writeJSONRPCError(w, http.StatusOK, req.ID,
			rpcErrMethodNotFound, "tool not found")
		return
	}
	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	s.invoke(w, r, req, params.Name, entry, args, true)
}

// invoke dispatches a single tool call, runs the action log, and
// emits the response.
//
// Audit invariant: when an action logger is configured the Server
// MUST NOT execute the tool unless the audit append can be made
// attributable. In strict mode (default) this means tenant
// resolution must succeed before dispatch; the call returns a
// -32603 internal error to the caller and no tool side effects
// occur otherwise. See [WithStrictAudit] for the loose alternative.
//
// Audit ordering: in strict + sync mode, recordActionLog runs BEFORE
// the response is written and a non-nil append error fails the
// JSON-RPC response with -32603. The tool may have run, but the
// caller never observes a "success" without a durable audit entry.
// In async mode appends are enqueued and the response is written
// immediately — async mode is best-effort by contract.
//
// Response shape: tools/call results are wrapped in the MCP-standard
// `{content: [{type: "json", data: ...}]}` envelope so generic MCP
// clients see a spec-compliant body. Direct shorthand calls (method
// = tool name, not "tools/call") still receive the raw tool output
// for kit consumers using the typed Out struct directly. NOTE:
// tools/call shape is breaking against pre-fix releases; clients
// reading `result.content[0].data` instead of `result` directly.
func (s *Server) invoke(w http.ResponseWriter, r *http.Request, req jsonRPCRequest, name string, entry *toolEntry, args json.RawMessage, mcpShape bool) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	ctx := r.Context()

	// Pre-dispatch audit gate: in strict mode, refuse to run the
	// tool when no tenant is on context. Without this check the
	// tool would execute and the audit entry would silently be
	// skipped — fail-open audit gap (security review H-2).
	if !s.auditPrecheck(ctx, r, name) {
		writeJSONRPCError(w, http.StatusOK, req.ID,
			rpcErrInternalError, "internal error")
		return
	}

	result, dispatchErr := entry.dispatch(ctx, args)

	// Strict + sync audit: the append must succeed before we admit
	// the tool's result back to the caller. An audit-store outage
	// fails the response with -32603 even though the tool ran — the
	// audit invariant ("every executed tool call returned to the
	// caller produced a signed entry") trumps the side effect.
	if auditErr := s.recordActionLog(ctx, r, name, dispatchErr); auditErr != nil {
		if s.cfg.strictAudit && !s.cfg.asyncAudit {
			writeJSONRPCError(w, http.StatusOK, req.ID,
				rpcErrInternalError, "internal error")
			return
		}
	}

	if dispatchErr != nil {
		code, msg := s.mapErrorToRPC(ctx, dispatchErr)
		writeJSONRPCError(w, http.StatusOK, req.ID, code, msg)
		return
	}

	if mcpShape {
		writeMCPToolResult(w, req.ID, result)
		return
	}
	writeJSONRPCRaw(w, req.ID, result)
}

// errUnknownField is the sentinel returned when a JSON-RPC params
// payload contains a field not present on the tool's input struct.
// We surface it as -32602 with a generic message rather than the
// decoder's "json: unknown field \"foo\"" text — that text leaks
// the input-struct shape to untrusted callers and caller-controlled
// log streams (security review L-4).
var errUnknownField = errors.New("mcp: request contained an unknown field")

var errInvalidArguments = errors.New("mcp: invalid arguments")

// buildDispatch constructs a type-erased dispatch function for a
// typed [Handler]. Validation runs against the freshly-decoded In
// value before the handler is called, so a `validate:"required"`
// violation surfaces as -32602 Invalid params rather than as a
// 500 from the handler.
func buildDispatch[In any, Out any](h Handler[In, Out]) dispatchFunc {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var in In
		if len(raw) > 0 {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				if strings.Contains(err.Error(), "unknown field") {
					return nil, errUnknownField
				}
				return nil, errInvalidArguments
			}
			// Trailing JSON guard: `{"x":1} {"y":2}` is not valid
			// JSON-RPC params even though dec.Decode happily reads
			// the first object. Probe for a second token; only EOF
			// is acceptable.
			if _, err := dec.Token(); err != io.EOF {
				return nil, errInvalidArguments
			}
		}
		if err := validate.Struct(in); err != nil {
			return nil, err
		}
		out, err := h(ctx, in)
		if err != nil {
			return nil, err
		}
		buf, err := json.Marshal(out)
		if err != nil {
			return nil, fmt.Errorf("mcp: marshal response: %w", err)
		}
		return buf, nil
	}
}

// writeMCPToolResult emits a tools/call response in the MCP-standard
// shape: `{content: [{type: "json", data: ...}]}`. The tool's raw
// JSON output becomes the `data` field. Generic MCP clients expect
// this envelope so they can render mixed content (text/json/image)
// uniformly.
func writeMCPToolResult(w http.ResponseWriter, id json.RawMessage, raw json.RawMessage) {
	wrapped := map[string]any{
		"content": []map[string]any{
			{
				"type": "json",
				"data": json.RawMessage(raw),
			},
		},
	}
	writeJSONRPCResult(w, id, wrapped)
}

// mapErrorToRPC converts an error from a tool's handler into a
// JSON-RPC error code + client-safe message.
//
// The validation / not-found / auth / forbidden / rate-limit
// branches are intentional — callers benefit from field-level
// detail to correct their next request, and the kit's apperror
// types are constructed by handlers with the wire surface in mind.
//
// The default and conflict branches log the full error server-side
// (so operators retain forensic detail) and return a generic
// "internal error" message to the JSON-RPC caller. Wrapped
// infrastructure errors ("pq: relation \"x\" does not exist",
// "context deadline exceeded: ... 10.0.0.1:5432") would otherwise
// leak topology to whoever can call a tool — security review M-1.
func (s *Server) mapErrorToRPC(ctx context.Context, err error) (int, string) {
	if err == nil {
		return rpcErrInternalError, "internal error"
	}
	switch {
	case errors.Is(err, errUnknownField):
		// L-4: do not echo the field name back to the caller.
		// The decoder's "json: unknown field \"foo\"" string
		// reveals the input-struct shape and is caller-controlled
		// log content.
		s.logInternalError(ctx, "mcp: rejected request with unknown field", err)
		return rpcErrInvalidParams, "invalid request"
	case errors.Is(err, errInvalidArguments):
		return rpcErrInvalidParams, "invalid arguments"
	case apperror.IsValidation(err):
		// Surface field-level details when present so the agent
		// learns which argument was wrong without a fresh round
		// trip. Message-only validation errors are free-form handler
		// text, so keep the JSON-RPC response stable.
		if ve, ok := apperror.AsValidation(err); ok && len(ve.Fields) > 0 {
			return rpcErrInvalidParams, ve.Error()
		}
		return rpcErrInvalidParams, "invalid request"
	case apperror.IsNotFound(err):
		return rpcErrInvalidParams, "resource not found"
	case apperror.IsAuthRequired(err):
		// JSON-RPC has no dedicated auth code; -32000 reserved
		// for server-defined errors. We use -32601 (method not
		// found) intentionally — an unauthenticated call should
		// not reveal which tools exist.
		return rpcErrMethodNotFound, "authentication required"
	case apperror.IsForbidden(err):
		return rpcErrMethodNotFound, "forbidden"
	case apperror.IsRateLimit(err):
		return rpcErrInternalError, "rate limit exceeded"
	case apperror.IsConflict(err):
		s.logInternalError(ctx, "mcp: tool returned conflict error", err)
		return rpcErrInternalError, "internal error"
	default:
		s.logInternalError(ctx, "mcp: tool returned internal error", err)
		return rpcErrInternalError, "internal error"
	}
}

// logInternalError records a server-side log entry for an error
// whose details must not be returned to the JSON-RPC caller.
// Includes request_id from context when present so operators can
// correlate the log line with the response the caller received.
func (s *Server) logInternalError(ctx context.Context, msg string, err error) {
	s.cfg.logger.ErrorContext(ctx, msg,
		redact.Error(err),
	)
}

// readBody enforces the configured body cap.
func readBody(r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil {
		return nil, errMissingRequestBody
	}
	defer func() { _ = r.Body.Close() }()
	if max <= 0 {
		max = 1 << 20
	}
	limited := io.LimitReader(r.Body, max+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, requestBodyTooLargeError{max: max}
	}
	return body, nil
}

var errMissingRequestBody = errors.New("missing request body")

type requestBodyTooLargeError struct {
	max int64
}

func (e requestBodyTooLargeError) Error() string {
	return "request body exceeds maximum size"
}

func safeReadBodyMessage(err error) string {
	if err == nil {
		return "failed to read request body"
	}
	if errors.Is(err, errMissingRequestBody) {
		return errMissingRequestBody.Error()
	}
	var tooLarge requestBodyTooLargeError
	if errors.As(err, &tooLarge) {
		return tooLarge.Error()
	}
	return "failed to read request body"
}

// isJSONArray returns true when the body's first non-whitespace byte
// is '[' — the JSON-RPC batch-request marker.
func isJSONArray(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

// writeJSONRPCResult serialises a successful JSON-RPC response.
func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	buf, err := json.Marshal(result)
	if err != nil {
		writeJSONRPCError(w, http.StatusOK, id, rpcErrInternalError, "internal error")
		return
	}
	writeJSONRPCRaw(w, id, buf)
}

// writeJSONRPCRaw emits a response with a pre-marshalled result.
func writeJSONRPCRaw(w http.ResponseWriter, id json.RawMessage, result json.RawMessage) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      normaliseID(id),
		Result:  result,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// writeJSONRPCError emits an error response. Status controls only
// the HTTP status; the JSON body always carries the JSON-RPC error
// shape.
func writeJSONRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      normaliseID(id),
		Error: &jsonRPCError{
			Code:    code,
			Message: message,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// normaliseID returns a JSON null when the request ID was absent.
// Per the JSON-RPC 2.0 spec, error responses to unparseable
// requests use null as the ID.
func normaliseID(id json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(id))
	if trimmed == "" {
		return json.RawMessage("null")
	}
	return id
}
