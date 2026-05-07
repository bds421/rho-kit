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

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/core/validate"
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
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
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
				rpcErrParseError, fmt.Sprintf("failed to read request body: %v", err))
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
			writeJSONRPCError(w, http.StatusOK, json.RawMessage("null"),
				rpcErrParseError, "invalid JSON: "+err.Error())
			return
		}
		if req.JSONRPC != "" && req.JSONRPC != "2.0" {
			writeJSONRPCError(w, http.StatusOK, req.ID,
				rpcErrInvalidRequest, "jsonrpc must be \"2.0\"")
			return
		}
		if req.Method == "" {
			writeJSONRPCError(w, http.StatusOK, req.ID,
				rpcErrInvalidRequest, "method is required")
			return
		}

		s.dispatch(w, r, req)
	})
}

// dispatch routes a JSON-RPC method to the appropriate handler.
// Recognised methods:
//   - "initialize" → server capabilities.
//   - "tools/list" → tool catalog.
//   - "tools/call" → invoke a registered tool by name carried in
//     params.
//   - "<tool-name>" → shorthand: invoke the named tool directly,
//     params are the tool input.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, req jsonRPCRequest) {
	switch {
	case req.Method == "initialize":
		s.handleInitialize(w, req)

	case req.Method == "tools/list":
		s.handleToolsList(w, req)

	case req.Method == "tools/call":
		s.handleToolsCall(w, r, req)

	default:
		// Treat any other method name as a direct tool call.
		entry, ok := s.lookup(req.Method)
		if !ok {
			writeJSONRPCError(w, http.StatusOK, req.ID,
				rpcErrMethodNotFound, fmt.Sprintf("method %q not found", req.Method))
			return
		}
		s.invoke(w, r, req, req.Method, entry, req.Params)
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
			rpcErrMethodNotFound, fmt.Sprintf("tool %q not found", params.Name))
		return
	}
	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	s.invoke(w, r, req, params.Name, entry, args)
}

// invoke dispatches a single tool call, runs the action log, and
// emits the response.
func (s *Server) invoke(w http.ResponseWriter, r *http.Request, req jsonRPCRequest, name string, entry *toolEntry, args json.RawMessage) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	ctx := r.Context()
	result, dispatchErr := entry.dispatch(ctx, args)

	// Action-log integration. We log every call with the resolved
	// outcome; failures carry a truncated reason.
	s.recordActionLog(ctx, r, name, dispatchErr)

	if dispatchErr != nil {
		code, msg := mapErrorToRPC(dispatchErr)
		writeJSONRPCError(w, http.StatusOK, req.ID, code, msg)
		return
	}

	// MCP `tools/call` wraps the tool's result in `{content: [...]}`
	// for compatibility with prompt clients. We emit the result
	// directly under `result` for both shorthand and `tools/call`
	// so SDK consumers who already typed-out the tool's Out struct
	// get it untouched. Clients that want the MCP-flavoured wrapper
	// can opt in via WithMCPContentWrapping (deferred).
	writeJSONRPCRaw(w, req.ID, result)
}

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
				return nil, apperror.NewValidation("invalid arguments: " + err.Error())
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

// mapErrorToRPC converts an error from a tool's handler into a
// JSON-RPC error code + client-safe message.
func mapErrorToRPC(err error) (int, string) {
	if err == nil {
		return rpcErrInternalError, "internal error"
	}
	switch {
	case apperror.IsValidation(err):
		// Surface field-level details when present so the agent
		// learns which argument was wrong without a fresh round
		// trip.
		if ve, ok := apperror.AsValidation(err); ok && len(ve.Fields) > 0 {
			return rpcErrInvalidParams, ve.Error()
		}
		return rpcErrInvalidParams, err.Error()
	case apperror.IsNotFound(err):
		return rpcErrInvalidParams, err.Error()
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
		return rpcErrInternalError, err.Error()
	default:
		return rpcErrInternalError, err.Error()
	}
}

// readBody enforces the configured body cap.
func readBody(r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("missing request body")
	}
	defer r.Body.Close()
	if max <= 0 {
		max = 1 << 20
	}
	limited := io.LimitReader(r.Body, max+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("request body exceeds %d bytes", max)
	}
	return body, nil
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
		writeJSONRPCError(w, http.StatusOK, id, rpcErrInternalError, "marshal result: "+err.Error())
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
