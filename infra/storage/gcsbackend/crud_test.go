package gcsbackend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

// These tests exercise the Put/Get/Delete/Exists CRUD contract against a
// fake GCS JSON API served by httptest, using the existing endpoint seam
// (newTestBackend). They cover the resumable/multipart upload finalize,
// generation-pinned downloads, the not-found sentinel mapping, the capacity
// translation on writes, and the operation_errors_total contract — none of
// which were previously covered.

const (
	// gcsUploadPrefix is the path the SDK POSTs multipart/resumable uploads to.
	gcsUploadPrefix = "/upload/"
	// gcsObjectMetaPrefix matches the JSON metadata API: GET/DELETE /b/<bucket>/o/<key>.
	gcsObjectMetaPrefix = "/b/"
)

func isObjectMetaRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, gcsObjectMetaPrefix) && strings.Contains(r.URL.Path, "/o/")
}

func isUploadRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, gcsUploadPrefix)
}

func TestPut_Success(t *testing.T) {
	var uploadHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUploadRequest(r) {
			uploadHit = true
			// Drain the multipart body so the writer's io.Copy completes.
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, attrsJSON)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	err := b.Put(context.Background(), "key", strings.NewReader("hello"), storage.ObjectMeta{
		ContentType: "text/plain",
		Custom:      map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatalf("Put error = %v, want nil", err)
	}
	if !uploadHit {
		t.Fatal("upload endpoint was never hit")
	}
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "put")); got != 0 {
		t.Fatalf("put operation_errors_total = %v, want 0", got)
	}
}

func TestPut_CapacityErrorTranslated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUploadRequest(r) {
			_, _ = io.Copy(io.Discard, r.Body)
			// 507 Insufficient Storage → ErrInsufficientCapacity.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInsufficientStorage)
			_, _ = io.WriteString(w, `{"error":{"code":507,"message":"Insufficient Storage"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	err := b.Put(context.Background(), "key", strings.NewReader("hello"), storage.ObjectMeta{ContentType: "text/plain"})
	if err == nil {
		t.Fatal("Put error = nil, want capacity error")
	}
	if !errors.Is(err, storage.ErrInsufficientCapacity) {
		t.Fatalf("Put error = %v, want errors.Is ErrInsufficientCapacity", err)
	}
	// A write failure is a real error and must inflate operation_errors_total.
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "put")); got != 1 {
		t.Fatalf("put operation_errors_total = %v, want 1", got)
	}
}

func TestPut_WriteFailureWrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUploadRequest(r) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"code":500,"message":"boom"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	err := b.Put(context.Background(), "key", strings.NewReader("hello"), storage.ObjectMeta{ContentType: "text/plain"})
	if err == nil {
		t.Fatal("Put error = nil, want wrapped error")
	}
	// Non-capacity failures must not be misclassified as capacity errors.
	if errors.Is(err, storage.ErrInsufficientCapacity) {
		t.Fatalf("Put error = %v, must not be ErrInsufficientCapacity", err)
	}
}

func TestPut_InvalidKeyRejectedBeforeUpload(t *testing.T) {
	var uploadHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isUploadRequest(r) {
			uploadHit = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, attrsJSON)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	// A traversal key must be rejected by ValidateKey before any network call.
	err := b.Put(context.Background(), "../escape", strings.NewReader("x"), storage.ObjectMeta{})
	if err == nil {
		t.Fatal("Put error = nil, want key validation error")
	}
	if uploadHit {
		t.Fatal("upload endpoint hit despite invalid key")
	}
}

func TestGet_Success(t *testing.T) {
	const wantBody = "hello world"
	var mediaGeneration string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isObjectMetaRequest(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, attrsJSON)
			return
		}
		// Media download path: capture the pinned generation query param.
		mediaGeneration = r.URL.Query().Get("generation")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, wantBody)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	rc, meta, err := b.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("Get error = %v, want nil", err)
	}
	defer func() { _ = rc.Close() }()

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != wantBody {
		t.Fatalf("body = %q, want %q", string(body), wantBody)
	}
	if meta.ContentType != "text/plain" {
		t.Fatalf("ContentType = %q, want text/plain", meta.ContentType)
	}
	if meta.Size != 5 {
		t.Fatalf("Size = %d, want 5", meta.Size)
	}
	if meta.ETag != "abc" {
		t.Fatalf("ETag = %q, want abc", meta.ETag)
	}
	// Generation pinning: the media GET must request the exact generation
	// returned by Attrs to avoid an Attrs->NewReader TOCTOU race.
	if mediaGeneration != "1700000000000001" {
		t.Fatalf("media generation = %q, want pinned 1700000000000001", mediaGeneration)
	}
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "get")); got != 0 {
		t.Fatalf("get operation_errors_total = %v, want 0", got)
	}
}

func TestGet_AttrsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Attrs returns 404 → ErrObjectNotExist → storage.ErrObjectNotFound.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	rc, _, err := b.Get(context.Background(), "missing")
	if rc != nil {
		_ = rc.Close()
	}
	if !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("Get error = %v, want errors.Is ErrObjectNotFound", err)
	}
	// A miss must not inflate operation_errors_total.
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "get")); got != 0 {
		t.Fatalf("get operation_errors_total = %v, want 0", got)
	}
}

func TestDelete_Success(t *testing.T) {
	var deleted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && isObjectMetaRequest(r) {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	if err := b.Delete(context.Background(), "key"); err != nil {
		t.Fatalf("Delete error = %v, want nil", err)
	}
	if !deleted {
		t.Fatal("delete endpoint was never hit")
	}
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "delete")); got != 0 {
		t.Fatalf("delete operation_errors_total = %v, want 0", got)
	}
}

func TestDelete_NotFoundIsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 404 → ErrObjectNotExist → Delete is idempotent and returns nil.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	if err := b.Delete(context.Background(), "missing"); err != nil {
		t.Fatalf("Delete error = %v, want nil for missing object (idempotent)", err)
	}
	// A not-found delete is expected and must not inflate errors.
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "delete")); got != 0 {
		t.Fatalf("delete operation_errors_total = %v, want 0", got)
	}
}

func TestDelete_ServerErrorWrapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"code":500,"message":"boom"}}`)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	err := b.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("Delete error = nil, want wrapped server error")
	}
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "delete")); got != 1 {
		t.Fatalf("delete operation_errors_total = %v, want 1", got)
	}
}

func TestExists_True(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isObjectMetaRequest(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, attrsJSON)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	ok, err := b.Exists(context.Background(), "key")
	if err != nil {
		t.Fatalf("Exists error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("Exists = false, want true")
	}
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "exists")); got != 0 {
		t.Fatalf("exists operation_errors_total = %v, want 0", got)
	}
}

func TestExists_False(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 404 → ErrObjectNotExist → Exists returns (false, nil).
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	ok, err := b.Exists(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Exists error = %v, want nil for missing object", err)
	}
	if ok {
		t.Fatal("Exists = true, want false")
	}
	// A negative existence probe must not inflate errors.
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "exists")); got != 0 {
		t.Fatalf("exists operation_errors_total = %v, want 0", got)
	}
}

func TestExists_NonNotFoundErrorPropagates(t *testing.T) {
	// Attrs is idempotent, so the SDK retries 5xx with backoff; a bounded
	// context keeps the test fast while still exercising the contract: any
	// error that is not ErrObjectNotExist propagates as (false, err) and is
	// counted in operation_errors_total (unlike a 404 miss).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"code":500,"message":"boom"}}`)
	}))
	defer srv.Close()

	b, _ := newTestBackend(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ok, err := b.Exists(ctx, "key")
	if err == nil {
		t.Fatal("Exists error = nil, want propagated error")
	}
	if errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("Exists error = %v, must not be ErrObjectNotFound for a server error", err)
	}
	if ok {
		t.Fatal("Exists = true on error, want false")
	}
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues(b.instance, "exists")); got != 1 {
		t.Fatalf("exists operation_errors_total = %v, want 1", got)
	}
}
