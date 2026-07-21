package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/core/v2/validate"
)

// wrapToolHandler builds an [sdkmcp.ToolHandler] (the SDK's low-level
// callback shape) that owns argument decode, validation, the
// destructive gate, the audit precheck/append, handler dispatch, and
// caller-safe error mapping.
//
// We deliberately bypass the SDK's [sdkmcp.AddTool] generic helper
// because that helper:
//
//  1. Unmarshals arguments using the SDK's internal json package, then
//     surfaces the raw error message back to the caller — leaking
//     decoder text (e.g. `"json: cannot unmarshal \"secret\" into ..."`)
//     to anyone who can call a tool. The kit's transport contract is
//     "never reflect caller-controlled bytes back in error messages".
//  2. Skips the kit's [validate.Struct] enforcement, which is a
//     superset of the JSON-Schema-level checks the SDK performs.
//
// wrapToolHandler is a free function rather than a method because Go's
// 1.x generics do not allow type parameters on methods.
func wrapToolHandler[In any, Out any](s *Server, name string, h Handler[In, Out], destructive bool) sdkmcp.ToolHandler {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		header := http.Header{}
		if req != nil && req.Extra != nil && req.Extra.Header != nil {
			header = req.Extra.Header
		}
		httpReq := (&http.Request{Header: header}).WithContext(ctx)

		var ok bool
		ctx, ok = s.auditPrecheck(ctx, httpReq, name)
		if !ok {
			return errorResult("internal error"), nil
		}
		httpReq = httpReq.WithContext(ctx)

		// Decode arguments. The SDK has already validated the JSON
		// payload against the inputSchema before reaching us; we still
		// decode strictly (DisallowUnknownFields) so an extra field
		// surfaces as the kit's stable "invalid request" message
		// rather than leaking the field name.
		var rawArgs json.RawMessage
		if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
			rawArgs = req.Params.Arguments
		}
		var in In
		if len(rawArgs) > 0 {
			dec := json.NewDecoder(bytes.NewReader(rawArgs))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				s.logInternalError(ctx, "mcp: argument decode failed", err)
				_ = s.recordActionLog(ctx, httpReq, name, errInvalidArguments)
				return errorResult("invalid arguments"), nil
			}
			if tok, err := dec.Token(); err != io.EOF {
				if err != nil {
					s.logInternalError(ctx, "mcp: trailing token after arguments", err)
				} else {
					// dec.Token returns a nil interface for a JSON null token;
					// reflect.TypeOf(nil) is nil and .String() would panic.
					kind := "null"
					if tok != nil {
						kind = reflect.TypeOf(tok).String()
					}
					s.cfg.logger.Warn("mcp: rejected request with trailing JSON tokens",
						redact.String("tool", name),
						redact.String("token_kind", kind),
					)
				}
				_ = s.recordActionLog(ctx, httpReq, name, errInvalidArguments)
				return errorResult("invalid arguments"), nil
			}
		}

		// Destructive-gate enforcement.
		if destructive {
			gatePayload := rawArgs
			if len(gatePayload) == 0 {
				gatePayload = json.RawMessage("{}")
			}
			if s.cfg.destructiveGate != nil {
				if gateErr := s.cfg.destructiveGate(ctx, name, gatePayload); gateErr != nil {
					_ = s.recordActionLog(ctx, httpReq, name, gateErr)
					return errorResult("destructive call refused"), nil
				}
			} else if !s.cfg.destructiveGateAcknowledged {
				_ = s.recordActionLog(ctx, httpReq, name, ErrDestructiveGateRequired)
				return errorResult("destructive tool not configured"), nil
			}
		}

		if err := validate.Struct(in); err != nil {
			_ = s.recordActionLog(ctx, httpReq, name, err)
			return errorResult(mapErrorForCaller(s, ctx, err)), nil
		}

		out, callErr, panicked := callHandlerSafely(s, ctx, name, h, in)

		// Marshal the response payload BEFORE auditing so a
		// marshal-failure on a "successful" handler return surfaces
		// to the audit as a failure rather than a phantom success.
		// The audit invariant is "every executed tool call produces
		// an entry whose outcome matches what the caller saw"; we
		// can only honour it if the audit reason reflects the
		// post-marshal outcome.
		var outBytes []byte
		marshalFailed := false
		if callErr == nil {
			b, marshalErr := json.Marshal(out)
			if marshalErr != nil {
				s.logInternalError(ctx, "mcp: marshal tool output", marshalErr)
				callErr = marshalErr
				marshalFailed = true
			} else {
				outBytes = b
			}
		}

		if auditErr := s.recordActionLog(ctx, httpReq, name, callErr); auditErr != nil {
			if s.cfg.strictAudit && !s.cfg.asyncAudit {
				return errorResult("internal error"), nil
			}
		}
		if callErr != nil {
			if marshalFailed || panicked {
				// Already logged above (marshal) or inside
				// callHandlerSafely (panic); skip the default-branch
				// logInternalError inside mapErrorForCaller to avoid a
				// duplicate server-side entry.
				return errorResult("internal error"), nil
			}
			return errorResult(mapErrorForCaller(s, ctx, callErr)), nil
		}

		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: string(outBytes)},
			},
			StructuredContent: json.RawMessage(outBytes),
		}, nil
	}
}

// callHandlerSafely invokes the typed tool handler, converting a panic
// into a returned error so the dispatch path can audit and mask it like
// any other failure.
//
// The MCP SDK's dispatch path (v1.6.1) contains no recover(); a
// panicking handler would otherwise unwind past wrapToolHandler into
// the SDK's jsonrpc2 goroutine and crash the process — and, because the
// panic happens before recordActionLog runs, no failure entry would be
// written, breaking the strict-audit invariant that every executed tool
// call produces a signed entry. Recovering here turns the panic into a
// generic, caller-safe error ([errHandlerPanicked]) and logs the
// recovered value server-side (redacted). The bool return signals the
// caller to skip the duplicate default-branch log in mapErrorForCaller.
func callHandlerSafely[In any, Out any](s *Server, ctx context.Context, name string, h Handler[In, Out], in In) (out Out, err error, panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			panicked = true
			err = errHandlerPanicked
			var zero Out
			out = zero
			s.cfg.logger.ErrorContext(ctx, "mcp: tool handler panicked",
				redact.String("tool", name),
				redact.Panic(rec),
			)
		}
	}()
	out, err = h(ctx, in)
	return out, err, false
}

// errorResult builds a CallToolResult with IsError=true and the
// supplied caller-safe message as the sole text-content item. The
// message MUST NOT contain caller-controlled bytes.
func errorResult(msg string) *sdkmcp.CallToolResult {
	return &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: msg},
		},
	}
}

// mapErrorForCaller converts a handler/validation error into a
// caller-safe message string. Sensitive infrastructure errors are
// logged server-side and the caller sees "internal error" only.
//
// Validation errors with structured Fields slices (constructed by
// handler code with the wire surface in mind) are passed through —
// they carry field names, not free-form text — so the caller learns
// which argument to correct on the next attempt.
func mapErrorForCaller(s *Server, ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	switch {
	case apperror.IsValidation(err):
		if ve, ok := apperror.AsValidation(err); ok && len(ve.Fields) > 0 {
			// Reflect only field names — free-form Messages may embed
			// caller-supplied bytes or internal detail.
			return validationFieldNames(ve)
		}
		return "invalid request"
	case apperror.IsNotFound(err):
		return "resource not found"
	case apperror.IsAuthRequired(err):
		return "authentication required"
	case apperror.IsForbidden(err):
		return "forbidden"
	case apperror.IsRateLimit(err):
		return "rate limit exceeded"
	case apperror.IsConflict(err):
		return "conflict"
	default:
		s.logInternalError(ctx, "mcp: tool returned internal error", err)
		return "internal error"
	}
}

// validationFieldNames builds a caller-safe validation message that
// lists only field names (never free-form Field.Message text).
func validationFieldNames(ve *apperror.ValidationError) string {
	if ve == nil || len(ve.Fields) == 0 {
		return "invalid request"
	}
	names := make([]string, 0, len(ve.Fields))
	seen := make(map[string]struct{}, len(ve.Fields))
	for _, f := range ve.Fields {
		n := strings.TrimSpace(f.Field)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	if len(names) == 0 {
		return "invalid request"
	}
	return "invalid request: " + strings.Join(names, ", ")
}

// logInternalError records a server-side log entry for an error
// whose details must not be returned to the caller.
func (s *Server) logInternalError(ctx context.Context, msg string, err error) {
	s.cfg.logger.ErrorContext(ctx, msg,
		redact.Error(err),
	)
}
