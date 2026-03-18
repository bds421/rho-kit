package s3backend

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/infra/storage"
)

const tracerName = "kit/storage/s3"

// Compile-time interface compliance checks.
var (
	_ storage.Storage        = (*S3Backend)(nil)
	_ storage.PresignedStore = (*S3Backend)(nil)
)

// S3Client abstracts the S3 API methods used by S3Backend.
// This enables unit testing with a mock client.
type S3Client interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	CopyObject(ctx context.Context, params *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
}

// S3Presigner abstracts the S3 presign API methods.
type S3Presigner interface {
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// S3Backend implements [storage.Storage] using AWS S3 (or compatible endpoints).
type S3Backend struct {
	client     S3Client
	presigner  S3Presigner
	bucket     string
	cfg        S3Config
	instance   string
	validators []storage.Validator
	metrics    *S3Metrics
}

// Option configures an S3Backend.
type Option func(*S3Backend)

// WithInstance sets the Prometheus instance label. Defaults to "default".
// Use a small static name like "avatars" or "documents".
func WithInstance(name string) Option {
	return func(b *S3Backend) {
		if name == "" {
			panic("s3backend: instance name must not be empty")
		}
		b.instance = name
	}
}

// WithConfig overrides the stored S3Config. This is primarily useful in tests
// via NewWithClient where no config is loaded from environment.
func WithConfig(cfg S3Config) Option {
	return func(b *S3Backend) {
		b.cfg = cfg
		if cfg.Bucket != "" {
			b.bucket = cfg.Bucket
		}
	}
}

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	return func(b *S3Backend) {
		b.validators = append(b.validators, validators...)
	}
}

// WithRegisterer sets the Prometheus registerer for S3 metrics.
// If not set, prometheus.DefaultRegisterer is used.
func WithRegisterer(reg prometheus.Registerer) Option {
	return func(b *S3Backend) {
		b.metrics = NewS3Metrics(reg)
	}
}

// New creates a new S3Backend from the given config.
// Panics if cfg.Bucket is empty.
func New(cfg S3Config, opts ...Option) (*S3Backend, error) {
	if cfg.Bucket == "" {
		panic("s3backend: S3Config.Bucket is required")
	}

	awsCfg, err := buildAWSConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("s3backend: build AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.ForcePathStyle {
			o.UsePathStyle = true
		}
	})

	b := &S3Backend{
		client:    client,
		presigner: s3.NewPresignClient(client),
		bucket:    cfg.Bucket,
		cfg:       cfg,
		instance:  "default",
		metrics:   defaultS3Metrics,
	}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// NewWithClient creates an S3Backend with a custom S3Client and presigner.
// Intended for testing with mock clients.
func NewWithClient(client S3Client, presigner S3Presigner, bucket string, opts ...Option) *S3Backend {
	if bucket == "" {
		panic("s3backend: bucket must not be empty")
	}
	b := &S3Backend{
		client:    client,
		presigner: presigner,
		bucket:    bucket,
		instance:  "default",
		metrics:   defaultS3Metrics,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

func buildAWSConfig(ctx context.Context, cfg S3Config) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		),
	}
	if cfg.Endpoint != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(cfg.Endpoint))
	}
	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

// Bucket returns the configured bucket name.
func (b *S3Backend) Bucket() string {
	return b.bucket
}

// Put uploads content from r to the given key. Validators run before the upload.
// The reader is piped directly to S3 without buffering into memory.
func (b *S3Backend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Put")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
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

	input := &s3.PutObjectInput{
		Bucket:      aws.String(b.bucket),
		Key:         aws.String(key),
		Body:        validated,
		ContentType: aws.String(contentType),
		Metadata:    meta.Custom,
	}
	if meta.Size > 0 {
		input.ContentLength = aws.Int64(meta.Size)
	}

	start := now()
	_, putErr := b.client.PutObject(ctx, input)
	b.metrics.observeOp(b.instance, "put", start, putErr)

	if putErr != nil {
		span.SetStatus(codes.Error, putErr.Error())
		return fmt.Errorf("s3backend: put %q: %w", key, putErr)
	}
	return nil
}

// Get downloads object content. Caller must close the returned ReadCloser.
func (b *S3Backend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Get")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	start := now()
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	b.metrics.observeOp(b.instance, "get", start, err)

	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			// NotFound is expected control flow, not an error — don't pollute traces.
			return nil, storage.ObjectMeta{}, fmt.Errorf("s3backend: get %q: %w", key, storage.ErrObjectNotFound)
		}
		span.SetStatus(codes.Error, err.Error())
		return nil, storage.ObjectMeta{}, fmt.Errorf("s3backend: get %q: %w", key, err)
	}

	meta := storage.ObjectMeta{
		ContentType: aws.ToString(out.ContentType),
		ETag:        aws.ToString(out.ETag),
		Custom:      out.Metadata,
	}
	if out.ContentLength != nil {
		meta.Size = *out.ContentLength
	}
	if out.LastModified != nil {
		meta.LastModified = *out.LastModified
	}
	return out.Body, meta, nil
}

// Delete removes an object by key. Returns nil if the key does not exist.
func (b *S3Backend) Delete(ctx context.Context, key string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Delete")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	start := now()
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	b.metrics.observeOp(b.instance, "delete", start, err)

	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("s3backend: delete %q: %w", key, err)
	}
	return nil
}

// Exists checks presence using HeadObject.
func (b *S3Backend) Exists(ctx context.Context, key string) (bool, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Exists")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	start := now()
	_, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	b.metrics.observeOp(b.instance, "exists", start, err)

	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return false, nil
		}
		span.SetStatus(codes.Error, err.Error())
		return false, fmt.Errorf("s3backend: exists %q: %w", key, err)
	}
	return true, nil
}
