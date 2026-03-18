package pagination

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type testItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestHandleCursorList_success(t *testing.T) {
	items := []testItem{
		{ID: "3", Name: "c"},
		{ID: "2", Name: "b"},
		{ID: "1", Name: "a"}, // extra item for hasMore
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 2,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				if limit != 2 {
					t.Errorf("ListFn limit = %d, want 2", limit)
				}
				return items, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result CursorResult[testItem]
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !result.HasMore {
		t.Error("expected HasMore=true")
	}
	if len(result.Data) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Data))
	}
	if result.NextCursor != "2" {
		t.Errorf("NextCursor = %q, want '2'", result.NextCursor)
	}
}

func TestHandleCursorList_withCursor(t *testing.T) {
	var gotCursor string

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				gotCursor = cursor
				return []testItem{{ID: "1", Name: "a"}}, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items?cursor=550e8400-e29b-41d4-a716-446655440000", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if gotCursor != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("cursor = %q, want UUID", gotCursor)
	}
}

func TestHandleCursorList_invalidCursor(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				t.Error("ListFn should not be called for invalid cursor")
				return nil, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items?cursor=not-a-uuid", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleCursorList_serviceError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				return nil, errors.New("db connection failed")
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandleCursorList_emptyResult(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				return []testItem{}, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var result CursorResult[testItem]
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if result.HasMore {
		t.Error("expected HasMore=false")
	}
	if result.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty", result.NextCursor)
	}
}

func TestHandleCursorList_limitParam(t *testing.T) {
	var gotLimit int

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     50,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				gotLimit = limit
				return []testItem{}, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items?limit=25", nil)
	handler.ServeHTTP(w, r)

	if gotLimit != 25 {
		t.Errorf("limit = %d, want 25", gotLimit)
	}
}
