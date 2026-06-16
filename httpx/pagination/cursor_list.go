package pagination

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
)

// CursorListOpts configures a cursor-list handler.
type CursorListOpts[T any] struct {
	DefaultLimit int
	MaxLimit     int
	ListFn       func(ctx context.Context, cursor string, limit int) ([]T, error)
	IDFn         func(T) string
	Logger       *slog.Logger
	// Validator validates the cursor format. Defaults to ValidateCursorUUID
	// when Signer is nil; ignored when Signer is set (the signer's verify
	// step replaces format validation since the payload is recovered as
	// trusted plaintext only after the HMAC matches).
	Validator CursorValidator
	// Signer, when set, HMAC-signs cursors so clients cannot forge or
	// enumerate IDs. The cursor on the wire becomes
	// base64url(id).base64url(hmac-sha256(secret, id)); the kit decodes
	// and verifies before passing the raw id to ListFn, and re-signs the
	// next-page cursor before sending. Strongly recommended for any
	// endpoint that paginates user-scoped or otherwise-sensitive data.
	Signer *CursorSigner
}

// HandleCursorList is a generic handler for cursor-based paginated list endpoints.
// It parses cursor params, validates the cursor, calls ListFn, builds the
// CursorResult, and writes the JSON response.
//
// When opts.Signer is non-nil, the incoming cursor is decoded and HMAC-verified
// before reaching ListFn, and the outgoing next_cursor is signed before
// serialisation — protecting against forgery and enumeration. When Signer
// is nil, raw cursors flow (pre-Wave-7 behaviour) and the Validator is
// applied for format checking only.
func HandleCursorList[T any](w http.ResponseWriter, r *http.Request, opts CursorListOpts[T]) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	// Validate the limit envelope at request time. Wave 68 closed a
	// hostile-review finding that HandleCursorList accepted any
	// DefaultLimit/MaxLimit at the call site, including negatives,
	// which then flowed into ParseCursorParams and BuildResult.
	if opts.MaxLimit <= 0 {
		opts.Logger.Error("pagination MaxLimit must be positive")
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if opts.DefaultLimit <= 0 {
		opts.Logger.Error("pagination DefaultLimit must be positive")
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if opts.DefaultLimit > opts.MaxLimit {
		opts.Logger.Error("pagination DefaultLimit exceeds MaxLimit")
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// ListFn and IDFn are mandatory wiring. A nil ListFn would nil-deref at
	// the call site below and a nil IDFn panics inside BuildResult whenever
	// hasMore is true; both are the same class of misconfiguration as an
	// invalid limit envelope, so surface them as a deliberate logged 500
	// rather than a torn connection from an unhandled panic.
	if opts.ListFn == nil {
		opts.Logger.Error("pagination ListFn must not be nil")
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if opts.IDFn == nil {
		opts.Logger.Error("pagination IDFn must not be nil")
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if opts.Signer != nil && !opts.Signer.ready() {
		opts.Logger.Error("pagination cursor signer invalid")
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	cp, err := ParseCursorParams(r, opts.DefaultLimit, opts.MaxLimit)
	if err != nil {
		if errors.Is(err, ErrCursorTooLong) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		if errors.Is(err, ErrAmbiguousQueryParam) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid pagination query")
			return
		}
		opts.Logger.Error("pagination cursor parameters invalid", redact.Error(err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}

	rawCursor := cp.Cursor
	if opts.Signer != nil {
		decoded, err := opts.Signer.Decode(cp.Cursor)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		rawCursor = decoded
	} else {
		validator := opts.Validator
		if validator == nil {
			validator = ValidateCursorUUID
		}
		if err := validator(cp.Cursor); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
	}

	items, err := opts.ListFn(r.Context(), rawCursor, cp.Limit)
	if err != nil {
		httpx.WriteServiceError(w, r, opts.Logger, err)
		return
	}

	result := BuildResult(items, cp.Limit, opts.IDFn)
	if opts.Signer != nil && result.NextCursor != "" {
		result.NextCursor = opts.Signer.Encode(result.NextCursor)
	}
	_ = httpx.WriteJSON(w, r, http.StatusOK, result)
}
