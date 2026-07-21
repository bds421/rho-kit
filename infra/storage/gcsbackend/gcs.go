package gcsbackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	gcsstorage "cloud.google.com/go/storage"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

const tracerName = "kit/storage/gcs"

// Compile-time interface compliance check.
var _ storage.Storage = (*Backend)(nil)

// Backend implements [storage.Storage] using Google Cloud Storage.
// Safe for concurrent use — all mutable state is owned by the upstream
// *gcsstorage.Client which is itself goroutine-safe.
type Backend struct {
	client     *gcsstorage.Client
	bucket     *gcsstorage.BucketHandle
	cfg        Config
	instance   string
	validators []storage.Validator
	metrics    *Metrics
}

// Option configures a Backend.
type Option func(*Backend)

// WithInstance sets the metrics/tracing instance label.
func WithInstance(name string) Option {
	return func(b *Backend) {
		if err := storage.ValidateInstanceName(name); err != nil {
			panic("gcsbackend: WithInstance invalid instance name")
		}
		b.instance = name
	}
}

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	copied := storage.CloneValidators(validators...)
	return func(b *Backend) {
		b.validators = storage.AppendValidators(b.validators, copied...)
	}
}

// WithMetricsRegisterer sets the Prometheus registerer for GCS
// metrics. If not set, prometheus.DefaultRegisterer is used. Replaces
// the v1 WithRegisterer spelling so it no longer collides with the
// metrics-level option of the same name.
func WithMetricsRegisterer(reg prometheus.Registerer) Option {
	return func(b *Backend) {
		if reg == nil {
			b.metrics = NewMetrics()
			return
		}
		b.metrics = NewMetrics(WithRegisterer(reg))
	}
}

// New creates a new Backend from config.
func New(ctx context.Context, cfg Config, opts ...Option) (*Backend, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("gcsbackend: Config.Bucket is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	clientOpts := append([]option.ClientOption(nil), cfg.ClientOptions...)
	if cfg.CredentialsFile != "" {
		clientOpts = append(clientOpts, option.WithAuthCredentialsFile(option.ServiceAccount, cfg.CredentialsFile))
	}
	if cfg.Endpoint != "" {
		clientOpts = append(clientOpts, option.WithEndpoint(cfg.Endpoint))
	}

	client, err := gcsstorage.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, storage.WrapSafe("gcsbackend: create client failed", err)
	}

	b := &Backend{
		client:   client,
		bucket:   client.Bucket(cfg.Bucket),
		cfg:      cfg,
		instance: "default",
	}
	for _, o := range opts {
		if o == nil {
			panic("gcsbackend: New option must not be nil")
		}
		o(b)
	}
	if b.metrics == nil {
		b.metrics = defaultMetrics()
	}
	return b, nil
}

// NewWithClient creates a Backend with a custom GCS client for testing.
func NewWithClient(client *gcsstorage.Client, cfg Config, opts ...Option) *Backend {
	if client == nil {
		panic("gcsbackend: NewWithClient requires a non-nil *storage.Client")
	}
	if cfg.Bucket == "" {
		panic("gcsbackend: Config.Bucket is required")
	}
	b := &Backend{
		client:   client,
		bucket:   client.Bucket(cfg.Bucket),
		cfg:      cfg,
		instance: "default",
	}
	for _, o := range opts {
		if o == nil {
			panic("gcsbackend: NewWithClient option must not be nil")
		}
		o(b)
	}
	if b.metrics == nil {
		b.metrics = defaultMetrics()
	}
	return b
}

// Put uploads content from r to the given GCS object key.
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gcs.Put")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.cfg.Bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(ctx, r, &meta, b.validators)
	if err != nil {
		span.SetStatus(codes.Error, storage.SpanErrorDescription(err))
		return err
	}
	if len(b.validators) > 0 {
		defer func() { _ = storage.CloseValidatedReader(validated) }()
	}
	if err := storage.ValidateObjectMeta(meta); err != nil {
		span.SetStatus(codes.Error, storage.SpanErrorDescription(err))
		return err
	}

	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// A separate cancellable ctx for the writer so we can abort the
	// resumable upload session cleanly on copy failure. Cancelling the
	// writer's ctx is the GCS SDK's documented way to abort an in-flight
	// resumable upload — Close on a half-written session would otherwise
	// leave the session dangling on the server for hours.
	writerCtx, cancelWriter := context.WithCancel(ctx)

	obj := b.bucket.Object(key)
	w := obj.NewWriter(writerCtx)
	w.ContentType = contentType
	w.Metadata = storage.CloneCustomMeta(meta.Custom)

	start := now()
	if _, err := io.Copy(w, validated); err != nil {
		cancelWriter()
		// Close after cancel reaps any goroutine the SDK started; we
		// ignore its error because the upload is intentionally aborted.
		_ = w.Close()
		b.metrics.observeOp(b.instance, "put", start, err)
		if capacity := translateGCSCapacity(err); capacity != nil {
			span.SetStatus(codes.Error, storage.SpanErrorDescription(capacity))
			return capacity
		}
		opErr := storage.WrapSafe("gcsbackend: put write failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	if err := w.Close(); err != nil {
		cancelWriter()
		b.metrics.observeOp(b.instance, "put", start, err)
		if capacity := translateGCSCapacity(err); capacity != nil {
			span.SetStatus(codes.Error, storage.SpanErrorDescription(capacity))
			return capacity
		}
		opErr := storage.WrapSafe("gcsbackend: put close failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	cancelWriter()
	b.metrics.observeOp(b.instance, "put", start, nil)

	return nil
}

// Get downloads a GCS object. Caller must close the returned ReadCloser.
func (b *Backend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gcs.Get")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.cfg.Bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	obj := b.bucket.Object(key)

	// Fetch attrs first (single API call) for full metadata including
	// ETag, LastModified, and custom metadata.
	start := now()
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		// Not-found is expected (cache miss / CAS probe / sweep) and
		// must not inflate operation_errors_total. Route through
		// gcsMetricErr so the contract matches S3 / Azure / SFTP.
		b.metrics.observeOp(b.instance, "get", start, gcsMetricErr(err))
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("gcsbackend: get: %w", storage.ErrObjectNotFound)
		}
		opErr := storage.WrapSafe("gcsbackend: get attrs failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}

	// Pin the generation from Attrs to ensure NewReader reads the same object
	// version, preventing a TOCTOU race if the object is overwritten between
	// the Attrs and NewReader calls.
	rc, err := obj.Generation(attrs.Generation).NewReader(ctx)
	if err != nil {
		// The object (or its pinned generation) can vanish in the
		// Attrs->NewReader window. Treat that as not-found: route the
		// metric through gcsMetricErr so it does not inflate
		// operation_errors_total, and return ErrObjectNotFound so callers
		// matching errors.Is keep working — same contract as the Attrs path.
		b.metrics.observeOp(b.instance, "get", start, gcsMetricErr(err))
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("gcsbackend: get: %w", storage.ErrObjectNotFound)
		}
		opErr := storage.WrapSafe("gcsbackend: get failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}
	b.metrics.observeOp(b.instance, "get", start, nil)

	meta := storage.ObjectMeta{
		ContentType:  attrs.ContentType,
		Size:         attrs.Size,
		ETag:         attrs.Etag,
		LastModified: attrs.Updated,
		Custom:       storage.CloneCustomMeta(attrs.Metadata),
	}

	return rc, meta, nil
}

// Delete removes a GCS object. Returns nil if the object does not exist.
func (b *Backend) Delete(ctx context.Context, key string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gcs.Delete")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.cfg.Bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	start := now()
	err := b.bucket.Object(key).Delete(ctx)
	b.metrics.observeOp(b.instance, "delete", start, gcsMetricErr(err))
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return nil
		}
		opErr := storage.WrapSafe("gcsbackend: delete failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	return nil
}

// Exists checks whether a GCS object exists using Attrs.
func (b *Backend) Exists(ctx context.Context, key string) (bool, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gcs.Exists")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.cfg.Bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	start := now()
	_, err := b.bucket.Object(key).Attrs(ctx)
	b.metrics.observeOp(b.instance, "exists", start, gcsMetricErr(err))
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return false, nil
		}
		opErr := storage.WrapSafe("gcsbackend: exists failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return false, opErr
	}
	return true, nil
}

func gcsMetricErr(err error) error {
	if errors.Is(err, gcsstorage.ErrObjectNotExist) {
		return nil
	}
	return err
}

// translateGCSCapacity inspects a GCS write error and returns
// [storage.ErrInsufficientCapacity] when the response indicates the
// bucket has hit a quota or storage-class limit. Returns nil for
// non-capacity errors so the caller can fall back to its generic
// translation.
//
// GCS surfaces capacity-related failures via *googleapi.Error with
// status 507 Insufficient Storage (bucket-level storage quota) or
// 413 Request Entity Too Large with a body referencing "quota" or
// "storage". The "quota" substring guard avoids classifying generic
// 413 / 507 responses unrelated to capacity (e.g. metadata cap).
func translateGCSCapacity(err error) error {
	if err == nil {
		return nil
	}
	var gerr *googleapi.Error
	if !errors.As(err, &gerr) {
		return nil
	}
	switch gerr.Code {
	case 507:
		return fmt.Errorf("gcsbackend: bucket insufficient storage: %w (cause: %w)", storage.ErrInsufficientCapacity, err) // kit:ok-fmt-errorf-wrap
	case 413:
		msg := strings.ToLower(gerr.Message)
		if strings.Contains(msg, "quota") || strings.Contains(msg, "storage") {
			return fmt.Errorf("gcsbackend: quota exhausted: %w (cause: %w)", storage.ErrInsufficientCapacity, err) // kit:ok-fmt-errorf-wrap
		}
	}
	return nil
}

// Healthy reports whether the configured bucket is reachable. It performs a
// lightweight single-object list probe; an empty bucket is still considered
// healthy, mirroring the azurebackend / sftpbackend Healthy contract so
// multi-backend wiring can treat the providers uniformly.
func (b *Backend) Healthy() bool {
	if b == nil || b.bucket == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	it := b.bucket.Objects(ctx, nil)
	// Cap the API page at one item: we only need to know the bucket responds,
	// not enumerate its contents.
	it.PageInfo().MaxSize = 1
	_, err := it.Next()
	if err == nil || errors.Is(err, iterator.Done) {
		return true
	}
	return false
}

// Close closes the underlying GCS client.
func (b *Backend) Close() error {
	if b == nil || b.client == nil {
		return nil
	}
	return b.client.Close()
}
