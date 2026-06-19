package pagination

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHandleCursorList_SignedCursor_RoundTrip(t *testing.T) {
	signer := MustNewCursorSigner([]byte("test-secret-32-bytes-aaaaaaaaaaaa"))

	type item struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	pageOne := []item{{ID: "id-1", Name: "a"}, {ID: "id-2", Name: "b"}}
	pageTwo := []item{{ID: "id-3", Name: "c"}}

	var gotCursor string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[item]{
			DefaultLimit: 2,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, _ int) ([]item, error) {
				gotCursor = cursor
				if cursor == "" {
					return append(pageOne, item{ID: "extra", Name: "x"}), nil
				}
				return pageTwo, nil
			},
			IDFn:   func(i item) string { return i.ID },
			Signer: signer,
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("first page status = %d", w.Code)
	}

	var result CursorResult[item]
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// next_cursor must NOT be the raw id.
	if result.NextCursor == "" {
		t.Fatal("expected non-empty next_cursor")
	}
	if result.NextCursor == "id-2" {
		t.Fatal("next_cursor leaked the raw id; signing did not take effect")
	}

	// Round-trip: pass it back to handler, ListFn must see the raw id.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/items?cursor="+result.NextCursor, nil)
	handler.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second page status = %d", w2.Code)
	}
	if gotCursor != "id-2" {
		t.Errorf("ListFn cursor = %q, want raw 'id-2' after signer.Decode", gotCursor)
	}
}

func TestHandleCursorList_SignedCursor_TamperedReturns400(t *testing.T) {
	signer := MustNewCursorSigner([]byte("test-secret-32-bytes-aaaaaaaaaaaa"))

	listFnCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, _ string, _ int) ([]testItem, error) {
				listFnCalled = true
				return nil, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Signer: signer,
			Logger: slog.Default(),
		})
	})

	// Build a valid cursor for "id-x" with the SAME secret, then mutate the
	// signature aggressively. Single-char mutations could randomly hit the
	// same base64 char; flipping multiple chars guarantees a mismatch.
	good := signer.Encode("id-x")
	dot := strings.IndexByte(good, '.')
	if dot < 0 {
		t.Fatalf("malformed signer output: %q", good)
	}
	// Replace the entire signature with zeros (encoded as 'A's).
	tampered := good[:dot+1] + strings.Repeat("A", len(good)-dot-1)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items?cursor="+tampered, nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("tampered cursor status = %d, want 400", w.Code)
	}
	if listFnCalled {
		t.Error("ListFn must not run on tampered cursor")
	}
}

func TestHandleCursorList_SignedCursor_DifferentSecretRejected(t *testing.T) {
	signerA := MustNewCursorSigner([]byte("aaaa-aaaa-aaaa-aaaa-aaaa-aaaa-aaaa"))
	signerB := MustNewCursorSigner([]byte("bbbb-bbbb-bbbb-bbbb-bbbb-bbbb-bbbb"))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, _ string, _ int) ([]testItem, error) {
				return nil, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Signer: signerA, // server signs with A
			Logger: slog.Default(),
		})
	})

	cursor := signerB.Encode("id-x") // attacker forges with B
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items?cursor="+cursor, nil)
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("foreign-secret cursor status = %d, want 400", w.Code)
	}
}

func TestNewCursorSigner_RejectsShortSecret(t *testing.T) {
	_, err := NewCursorSigner([]byte("short"))
	if err == nil {
		t.Fatal("expected error for <32-byte secret")
	}
	if strings.Contains(err.Error(), "5") {
		t.Fatalf("cursor signer error leaked supplied secret length: %v", err)
	}
}

func TestCursorSigner_ZeroValueFailsClosed(t *testing.T) {
	var zero CursorSigner
	if got := zero.Encode("id-1"); got != "" {
		t.Fatalf("zero-value Encode returned %q, want empty", got)
	}
	if _, err := zero.Decode("payload.signature"); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("zero-value Decode error = %v, want ErrCursorInvalid", err)
	}

	var nilSigner *CursorSigner
	if got := nilSigner.Encode("id-1"); got != "" {
		t.Fatalf("nil Encode returned %q, want empty", got)
	}
	if _, err := nilSigner.Decode("payload.signature"); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("nil Decode error = %v, want ErrCursorInvalid", err)
	}
}

func TestCursorSigner_EncodeEmptyReturnsEmpty(t *testing.T) {
	s := MustNewCursorSigner([]byte("test-secret-32-bytes-aaaaaaaaaaaa"))
	if got := s.Encode(""); got != "" {
		t.Errorf("Encode('') = %q, want empty", got)
	}
}

func TestCursorSigner_DecodeEmptyReturnsEmpty(t *testing.T) {
	s := MustNewCursorSigner([]byte("test-secret-32-bytes-aaaaaaaaaaaa"))
	got, err := s.Decode("")
	if err != nil {
		t.Fatalf("Decode('') err = %v", err)
	}
	if got != "" {
		t.Errorf("Decode('') = %q, want empty", got)
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
	if body := w.Body.String(); !strings.Contains(body, "invalid cursor") || strings.Contains(body, "not-a-uuid") {
		t.Fatalf("body = %q, want generic invalid cursor without raw cursor", body)
	}
}

func TestHandleCursorList_oversizedCursorReturns400WithoutCallingListFn(t *testing.T) {
	listFnCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				listFnCalled = true
				return nil, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items?cursor="+strings.Repeat("a", MaxCursorLen+1), nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if listFnCalled {
		t.Fatal("ListFn should not be called for oversized cursor")
	}
	if body := w.Body.String(); !strings.Contains(body, "invalid cursor") {
		t.Fatalf("body = %q, want invalid cursor", body)
	}
}

func TestHandleCursorList_ambiguousQueryReturns400WithoutCallingListFn(t *testing.T) {
	listFnCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				listFnCalled = true
				return nil, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items?cursor=a&cursor=b", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if listFnCalled {
		t.Fatal("ListFn should not be called for ambiguous query")
	}
	if body := w.Body.String(); !strings.Contains(body, "invalid pagination query") {
		t.Fatalf("body = %q, want invalid pagination query", body)
	}
}

func TestHandleCursorList_invalidLimitConfigReturns500WithoutCallingListFn(t *testing.T) {
	listFnCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 0,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				listFnCalled = true
				return nil, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if listFnCalled {
		t.Fatal("ListFn should not be called for invalid limit config")
	}
	if body := w.Body.String(); !strings.Contains(body, "internal error") {
		t.Fatalf("body = %q, want internal error", body)
	}
}

func TestHandleCursorList_InvalidSignerReturns500WithoutCallingListFn(t *testing.T) {
	var signer CursorSigner
	listFnCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, _ string, _ int) ([]testItem, error) {
				listFnCalled = true
				return []testItem{{ID: "1", Name: "a"}}, nil
			},
			IDFn:   func(i testItem) string { return i.ID },
			Signer: &signer,
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if listFnCalled {
		t.Fatal("ListFn should not be called for invalid signer")
	}
	if body := w.Body.String(); !strings.Contains(body, "internal error") {
		t.Fatalf("body = %q, want internal error", body)
	}
}

func TestHandleCursorList_ValidatorErrorDoesNotLeak(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, cursor string, limit int) ([]testItem, error) {
				t.Error("ListFn should not be called for invalid cursor")
				return nil, nil
			},
			IDFn: func(i testItem) string { return i.ID },
			Validator: func(string) error {
				return errors.New("cursor lookup failed: redis://user:secret@10.0.0.5/0")
			},
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items?cursor=opaque-token", nil)
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "invalid cursor") {
		t.Fatalf("body = %q, want invalid cursor", body)
	}
	for _, leak := range []string{"redis://", "secret", "10.0.0.5", "opaque-token"} {
		if strings.Contains(body, leak) {
			t.Fatalf("body leaked %q: %q", leak, body)
		}
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

func TestBuildResult_nilSliceSerialisesAsEmptyArray(t *testing.T) {
	// A nil items slice (the idiomatic empty result) must produce "data":[]
	// not "data":null so strictly-typed JSON clients see a stable shape.
	result := BuildResult[testItem](nil, 10, func(i testItem) string { return i.ID })
	if result.Data == nil {
		t.Fatal("BuildResult Data is nil; want non-nil empty slice")
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); !strings.Contains(got, `"data":[]`) {
		t.Fatalf("marshalled result = %s, want \"data\":[]", got)
	}
	if strings.Contains(string(b), `"data":null`) {
		t.Fatalf("marshalled result contains data:null: %s", b)
	}
}

func TestHandleCursorList_nilListResultSerialisesAsEmptyArray(t *testing.T) {
	// ListFn returns a nil slice (common for empty DB results); the response
	// envelope must still emit "data":[] rather than "data":null.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn: func(_ context.Context, _ string, _ int) ([]testItem, error) {
				return nil, nil // nil, not []testItem{}
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
	if body := w.Body.String(); !strings.Contains(body, `"data":[]`) || strings.Contains(body, `"data":null`) {
		t.Fatalf("body = %q, want data:[] not data:null", body)
	}
}

func TestHandleCursorList_nilListFnReturns500WithoutPanic(t *testing.T) {
	// A nil ListFn is a wiring bug; it must surface as a deliberate logged
	// 500, the same failure mode as an invalid limit config, not a panic.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 10,
			MaxLimit:     100,
			ListFn:       nil,
			IDFn:         func(i testItem) string { return i.ID },
			Logger:       slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	handler.ServeHTTP(w, r) // must not panic

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "internal error") {
		t.Fatalf("body = %q, want internal error", body)
	}
}

func TestHandleCursorList_nilIDFnReturns500WithoutPanic(t *testing.T) {
	// A nil IDFn panics inside BuildResult once hasMore is true; it must
	// instead be rejected up front with a logged 500. ListFn must not run.
	listFnCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleCursorList(w, r, CursorListOpts[testItem]{
			DefaultLimit: 2,
			MaxLimit:     100,
			ListFn: func(_ context.Context, _ string, _ int) ([]testItem, error) {
				listFnCalled = true
				// Return limit+1 items so hasMore is true, which is the path
				// that dereferences IDFn in BuildResult.
				return []testItem{{ID: "1"}, {ID: "2"}, {ID: "3"}}, nil
			},
			IDFn:   nil,
			Logger: slog.Default(),
		})
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/items", nil)
	handler.ServeHTTP(w, r) // must not panic

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if listFnCalled {
		t.Fatal("ListFn should not be called when IDFn is nil")
	}
	if body := w.Body.String(); !strings.Contains(body, "internal error") {
		t.Fatalf("body = %q, want internal error", body)
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
