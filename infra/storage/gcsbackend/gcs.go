package gcsbackend

import (
	"context"
	"errors"
	"fmt"
	"io"

	gcsstorage "cloud.google.com/go/storage"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/api/option"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

const tracerName = "kit/storage/gcs"

// Compile-time interface compliance check.
var _ storage.Storage = (*GCSBackend)(nil)

// GCSBackend implements [storage.Storage] using Google Cloud Storage.
type GCSBackend struct {
	client     *gcsstorage.Client
	bucket     *gcsstorage.BucketHandle
	cfg        GCSConfig
	instance   string
	validators []storage.Validator
	metrics    *GCSMetrics
}

// Option configures a GCSBackend.
type Option func(*GCSBackend)

// WithInstance sets the metrics/tracing instance label.
func WithInstance(name string) Option {
	return func(b *GCSBackend) {
		if err := storage.ValidateInstanceName(name); err != nil {
			panic("gcsbackend: invalid instance name")
		}
		b.instance = name
	}
}

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	copied := storage.CloneValidators(validators...)
	return func(b *GCSBackend) {
		b.validators = storage.AppendValidators(b.validators, copied...)
	}
}

// WithRegisterer sets the Prometheus registerer for GCS metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(b *GCSBackend) {
		b.metrics = NewGCSMetrics(reg)
	}
}

// New creates a new GCSBackend from config.
func New(ctx context.Context, cfg GCSConfig, opts ...Option) (*GCSBackend, error) {
	if cfg.Bucket == "" {
		panic("gcsbackend: GCSConfig.Bucket is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	var clientOpts []option.ClientOption
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

	b := &GCSBackend{
		client:   client,
		bucket:   client.Bucket(cfg.Bucket),
		cfg:      cfg,
		instance: "default",
		metrics:  defaultGCSMetrics,
	}
	for _, o := range opts {
		if o == nil {
			panic("gcsbackend: option must not be nil")
		}
		o(b)
	}
	return b, nil
}

// NewWithClient creates a GCSBackend with a custom GCS client for testing.
func NewWithClient(client *gcsstorage.Client, cfg GCSConfig, opts ...Option) *GCSBackend {
	if client == nil {
		panic("gcsbackend: NewWithClient requires a non-nil *storage.Client")
	}
	if cfg.Bucket == "" {
		panic("gcsbackend: GCSConfig.Bucket is required")
	}
	b := &GCSBackend{
		client:   client,
		bucket:   client.Bucket(cfg.Bucket),
		cfg:      cfg,
		instance: "default",
		metrics:  defaultGCSMetrics,
	}
	for _, o := range opts {
		if o == nil {
			panic("gcsbackend: option must not be nil")
		}
		o(b)
	}
	return b
}

// Put uploads content from r to the given GCS object key.
func (b *GCSBackend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
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
		opErr := storage.WrapSafe("gcsbackend: put write failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}

	if err := w.Close(); err != nil {
		cancelWriter()
		b.metrics.observeOp(b.instance, "put", start, err)
		opErr := storage.WrapSafe("gcsbackend: put close failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	cancelWriter()
	b.metrics.observeOp(b.instance, "put", start, nil)

	return nil
}

// Get downloads a GCS object. Caller must close the returned ReadCloser.
func (b *GCSBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
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
		b.metrics.observeOp(b.instance, "get", start, err)
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
		b.metrics.observeOp(b.instance, "get", start, err)
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
func (b *GCSBackend) Delete(ctx context.Context, key string) error {
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
	b.metrics.observeOp(b.instance, "delete", start, err)
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
func (b *GCSBackend) Exists(ctx context.Context, key string) (bool, error) {
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
	b.metrics.observeOp(b.instance, "exists", start, err)
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

// Close closes the underlying GCS client.
func (b *GCSBackend) Close() error {
	if b == nil || b.client == nil {
		return nil
	}
	return b.client.Close()
}
