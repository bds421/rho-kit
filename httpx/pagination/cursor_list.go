package pagination

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/httpx"
)

// CursorListOpts configures a cursor-list handler.
type CursorListOpts[T any] struct {
	DefaultLimit int
	MaxLimit     int
	ListFn       func(ctx context.Context, cursor string, limit int) ([]T, error)
	IDFn         func(T) string
	Logger       *slog.Logger
	// Validator validates the cursor format. Defaults to ValidateCursorUUID.
	// Set a custom validator for non-UUID cursors (e.g., ULID, integer PKs).
	Validator CursorValidator
}

// HandleCursorList is a generic handler for cursor-based paginated list endpoints.
// It parses cursor params, validates the cursor, calls ListFn, builds the
// CursorResult, and writes the JSON response.
func HandleCursorList[T any](w http.ResponseWriter, r *http.Request, opts CursorListOpts[T]) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	validator := opts.Validator
	if validator == nil {
		validator = ValidateCursorUUID
	}
	cp := ParseCursorParams(r, opts.DefaultLimit, opts.MaxLimit)
	if err := validator(cp.Cursor); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	items, err := opts.ListFn(r.Context(), cp.Cursor, cp.Limit)
	if err != nil {
		httpx.WriteServiceError(w, r, opts.Logger, err)
		return
	}

	result := BuildResult(items, cp.Limit, opts.IDFn)
	httpx.WriteJSON(w, http.StatusOK, result)
}
