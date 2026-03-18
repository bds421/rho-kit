package gcsbackend

import (
	"context"
	"errors"
	"fmt"
	"io"

	gcsstorage "cloud.google.com/go/storage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/api/option"

	"github.com/bds421/rho-kit/infra/storage"
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
}

// Option configures a GCSBackend.
type Option func(*GCSBackend)

// WithInstance sets the metrics/tracing instance label.
func WithInstance(name string) Option {
	return func(b *GCSBackend) {
		if name == "" {
			panic("gcsbackend: instance name must not be empty")
		}
		b.instance = name
	}
}

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	return func(b *GCSBackend) {
		b.validators = append(b.validators, validators...)
	}
}

// New creates a new GCSBackend from config.
func New(ctx context.Context, cfg GCSConfig, opts ...Option) (*GCSBackend, error) {
	if cfg.Bucket == "" {
		panic("gcsbackend: GCSConfig.Bucket is required")
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
		return nil, fmt.Errorf("gcsbackend: create client: %w", err)
	}

	b := &GCSBackend{
		client:   client,
		bucket:   client.Bucket(cfg.Bucket),
		cfg:      cfg,
		instance: "default",
	}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// NewWithClient creates a GCSBackend with a custom GCS client for testing.
func NewWithClient(client *gcsstorage.Client, cfg GCSConfig, opts ...Option) *GCSBackend {
	if cfg.Bucket == "" {
		panic("gcsbackend: GCSConfig.Bucket is required")
	}
	b := &GCSBackend{
		client:   client,
		bucket:   client.Bucket(cfg.Bucket),
		cfg:      cfg,
		instance: "default",
	}
	for _, o := range opts {
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
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	validated, err := storage.ApplyValidators(r, &meta, b.validators)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	obj := b.bucket.Object(key)
	w := obj.NewWriter(ctx)
	w.ContentType = contentType
	w.Metadata = meta.Custom

	if _, err := io.Copy(w, validated); err != nil {
		_ = w.Close()
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("gcsbackend: put %q: write: %w", key, err)
	}

	if err := w.Close(); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("gcsbackend: put %q: close: %w", key, err)
	}

	return nil
}

// Get downloads a GCS object. Caller must close the returned ReadCloser.
func (b *GCSBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gcs.Get")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.cfg.Bucket),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	obj := b.bucket.Object(key)

	// Fetch attrs first (single API call) for full metadata including
	// ETag, LastModified, and custom metadata.
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("gcsbackend: get %q: %w", key, storage.ErrObjectNotFound)
		}
		span.SetStatus(codes.Error, err.Error())
		return nil, storage.ObjectMeta{}, fmt.Errorf("gcsbackend: get %q: attrs: %w", key, err)
	}

	// Pin the generation from Attrs to ensure NewReader reads the same object
	// version, preventing a TOCTOU race if the object is overwritten between
	// the Attrs and NewReader calls.
	rc, err := obj.Generation(attrs.Generation).NewReader(ctx)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, storage.ObjectMeta{}, fmt.Errorf("gcsbackend: get %q: %w", key, err)
	}

	meta := storage.ObjectMeta{
		ContentType:  attrs.ContentType,
		Size:         attrs.Size,
		ETag:         attrs.Etag,
		LastModified: attrs.Updated,
		Custom:       attrs.Metadata,
	}

	return rc, meta, nil
}

// Delete removes a GCS object. Returns nil if the object does not exist.
func (b *GCSBackend) Delete(ctx context.Context, key string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gcs.Delete")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.cfg.Bucket),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	err := b.bucket.Object(key).Delete(ctx)
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return nil
		}
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("gcsbackend: delete %q: %w", key, err)
	}
	return nil
}

// Exists checks whether a GCS object exists using Attrs.
func (b *GCSBackend) Exists(ctx context.Context, key string) (bool, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gcs.Exists")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.cfg.Bucket),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	_, err := b.bucket.Object(key).Attrs(ctx)
	if err != nil {
		if errors.Is(err, gcsstorage.ErrObjectNotExist) {
			return false, nil
		}
		span.SetStatus(codes.Error, err.Error())
		return false, fmt.Errorf("gcsbackend: exists %q: %w", key, err)
	}
	return true, nil
}

// Close closes the underlying GCS client.
func (b *GCSBackend) Close() error {
	return b.client.Close()
}
