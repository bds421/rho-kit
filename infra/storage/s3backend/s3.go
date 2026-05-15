package s3backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

const tracerName = "kit/storage/s3"

// Compile-time interface compliance checks.
var (
	_ storage.Storage        = (*Backend)(nil)
	_ storage.PresignedStore = (*Backend)(nil)
)

// Client abstracts the S3 API methods used by Backend.
// This enables unit testing with a mock client.
type Client interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadObject(ctx context.Context, params *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	CopyObject(ctx context.Context, params *s3.CopyObjectInput, optFns ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
}

// Presigner abstracts the S3 presign API methods.
type Presigner interface {
	PresignGetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// Backend implements [storage.Storage] using AWS S3 (or compatible endpoints).
// Safe for concurrent use — all mutable state is owned by the upstream
// AWS SDK client which is itself goroutine-safe.
type Backend struct {
	client     Client
	presigner  Presigner
	bucket     string
	cfg        Config
	instance   string
	validators []storage.Validator
	metrics    *Metrics
}

// Option configures an Backend.
type Option func(*Backend)

// WithInstance sets the Prometheus instance label. Defaults to "default".
// Use a small static name like "avatars" or "documents".
func WithInstance(name string) Option {
	return func(b *Backend) {
		if err := storage.ValidateInstanceName(name); err != nil {
			panic("s3backend: WithInstance: invalid instance name")
		}
		b.instance = name
	}
}

// WithConfig overrides the stored Config. This is primarily useful in tests
// via NewWithClient where no config is loaded from environment.
func WithConfig(cfg Config) Option {
	return func(b *Backend) {
		b.cfg = cfg
		if cfg.Bucket != "" {
			b.bucket = cfg.Bucket
		}
	}
}

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	copied := storage.CloneValidators(validators...)
	return func(b *Backend) {
		b.validators = storage.AppendValidators(b.validators, copied...)
	}
}

// WithMetricsRegisterer sets the Prometheus registerer for S3
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

// New creates a new Backend from the given config.
//
// New uses [context.Background] for the AWS SDK credential resolution
// chain. This is safe for static `AccessKey`/`SecretKey` configs (no
// I/O), but with `UseDefaultCredentials=true` or a remote
// `CredentialProvider` the SDK may perform EC2 metadata fetches, STS
// AssumeRole, SSO token exchange, or web-identity token reads — all
// unbounded without a caller deadline. Production services that
// resolve credentials remotely should call [NewContext] with a
// bounded ctx instead.
//
// Panics if cfg.Bucket is empty.
func New(cfg Config, opts ...Option) (*Backend, error) {
	return NewContext(context.Background(), cfg, opts...)
}

// NewContext is the ctx-aware variant of [New]. The supplied ctx
// bounds the AWS SDK credential resolution chain
// ([awsconfig.LoadDefaultConfig]). Prefer this constructor over [New]
// when the SDK may perform remote I/O during startup (EC2 metadata,
// STS AssumeRole, SSO token exchange, web-identity tokens).
//
// Panics if cfg.Bucket is empty.
func NewContext(ctx context.Context, cfg Config, opts ...Option) (*Backend, error) {
	if cfg.Bucket == "" {
		panic("s3backend: Config.Bucket is required")
	}
	if err := cfg.Validate(""); err != nil {
		return nil, err
	}

	awsCfg, err := buildAWSConfig(ctx, cfg)
	if err != nil {
		return nil, storage.WrapSafe("s3backend: build AWS config failed", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.ForcePathStyle {
			o.UsePathStyle = true
		}
	})

	b := &Backend{
		client:    client,
		presigner: s3.NewPresignClient(client),
		bucket:    cfg.Bucket,
		cfg:       cfg,
		instance:  "default",
		metrics:   defaultMetrics(),
	}
	for _, o := range opts {
		if o == nil {
			panic("s3backend: NewContext: option must not be nil")
		}
		o(b)
	}
	return b, nil
}

// NewWithClient creates an Backend with a custom Client and presigner.
// Intended for testing with mock clients.
func NewWithClient(client Client, presigner Presigner, bucket string, opts ...Option) *Backend {
	if client == nil {
		panic("s3backend: NewWithClient requires a non-nil Client")
	}
	if presigner == nil {
		panic("s3backend: NewWithClient requires a non-nil Presigner")
	}
	if bucket == "" {
		panic("s3backend: NewWithClient: bucket must not be empty")
	}
	b := &Backend{
		client:    client,
		presigner: presigner,
		bucket:    bucket,
		instance:  "default",
		metrics:   defaultMetrics(),
	}
	for _, o := range opts {
		if o == nil {
			panic("s3backend: NewWithClient: option must not be nil")
		}
		o(b)
	}
	return b
}

func buildAWSConfig(ctx context.Context, cfg Config) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	switch {
	case cfg.CredentialProvider != nil:
		opts = append(opts, awsconfig.WithCredentialsProvider(cfg.CredentialProvider))
	case cfg.UseDefaultCredentials:
		// Let the AWS SDK resolve and refresh credentials from the default
		// chain: environment, shared config, web identity, ECS/EKS/EC2 roles,
		// SSO, process providers, and any SDK-supported rotating source.
	default:
		// Static access-key path. The AWS SDK's StaticCredentialsProvider
		// returns the same credential on every request — no rotation,
		// no expiry — so a rotation event at the IAM side leaves the
		// kit signing with the old key until the process is restarted.
		// Warn at construction so operators wiring static keys cannot
		// silently miss the rotation gap (L119).
		slog.Warn("s3backend: using static AccessKeyID/SecretAccessKey — credentials will NOT rotate without a process restart; prefer UseDefaultCredentials or CredentialProvider for rotating credentials",
			redact.String("bucket", cfg.Bucket),
			redact.String("region", cfg.Region),
		)
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	if cfg.Endpoint != "" {
		opts = append(opts, awsconfig.WithBaseEndpoint(cfg.Endpoint))
	}
	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

// Bucket returns the configured bucket name.
func (b *Backend) Bucket() string {
	return b.bucket
}

// Close releases any resources held by the backend. The AWS SDK
// HTTP client is stateless from the kit's perspective, so this is a
// documented no-op present only for uniform interface implementation.
func (b *Backend) Close() error { return nil }

// Put uploads content from r to the given key. Validators run before the upload.
// The reader is piped directly to S3 without buffering into memory.
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Put")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
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

	input := &s3.PutObjectInput{
		Bucket:      aws.String(b.bucket),
		Key:         aws.String(key),
		Body:        validated,
		ContentType: aws.String(contentType),
		Metadata:    storage.CloneCustomMeta(meta.Custom),
	}
	if meta.Size > 0 {
		input.ContentLength = aws.Int64(meta.Size)
	}
	if err := applySSE(input, b.cfg); err != nil {
		span.SetStatus(codes.Error, storage.SpanErrorDescription(err))
		return err
	}

	start := now()
	_, putErr := b.client.PutObject(ctx, input)
	b.metrics.observeOp(b.instance, "put", start, putErr)

	if putErr != nil {
		if translated := translateS3Capacity(putErr, meta.Size); translated != nil {
			span.SetStatus(codes.Error, storage.SpanErrorDescription(translated))
			return translated
		}
		opErr := storage.WrapSafe("s3backend: put failed", putErr)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	return nil
}

// translateS3Capacity translates an S3 PutObject error into
// [storage.ErrInsufficientCapacity] when the underlying smithy.APIError
// indicates the upload exceeded the bucket / request size budget. Returns
// nil when the error is not a capacity failure so the caller can fall
// back to the generic translation.
//
// EntityTooLarge — the object exceeded the per-object cap (5 GiB single
// PUT, 5 TiB multipart). InvalidRequest with a non-zero declared size is
// the S3 response when a request-body length header overflows the
// streaming limit.
//
// Generic ServiceUnavailable (503) is deliberately NOT mapped here. AWS
// returns it for regional outage, throttling, partial maintenance, and
// other transient conditions that are not capacity failures; tagging
// them as STORAGE_FULL would send operators to the wrong runbook and
// confuse retry / admission decisions. Such errors fall through to the
// generic safe wrapper as transient backend failures.
func translateS3Capacity(err error, size int64) error {
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return nil
	}
	switch apiErr.ErrorCode() {
	case "EntityTooLarge":
		return fmt.Errorf("s3backend: object exceeds bucket size limit: %w (cause: %w)", storage.ErrInsufficientCapacity, err)
	case "InvalidRequest":
		if size > 0 {
			return fmt.Errorf("s3backend: request rejected for size: %w (cause: %w)", storage.ErrInsufficientCapacity, err)
		}
	}
	return nil
}

// Get downloads object content. Caller must close the returned ReadCloser.
func (b *Backend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Get")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	start := now()
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	b.metrics.observeOp(b.instance, "get", start, s3MetricErr(err))

	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			// NotFound is expected control flow, not an error — don't pollute traces.
			return nil, storage.ObjectMeta{}, fmt.Errorf("s3backend: get: %w", storage.ErrObjectNotFound)
		}
		opErr := storage.WrapSafe("s3backend: get failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}

	meta := storage.ObjectMeta{
		ContentType: aws.ToString(out.ContentType),
		ETag:        aws.ToString(out.ETag),
		Custom:      storage.CloneCustomMeta(out.Metadata),
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
func (b *Backend) Delete(ctx context.Context, key string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Delete")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	start := now()
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	b.metrics.observeOp(b.instance, "delete", start, s3MetricErr(err))

	if err != nil {
		if isS3NotFound(err) {
			return nil
		}
		opErr := storage.WrapSafe("s3backend: delete failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	return nil
}

// Exists checks presence using HeadObject.
func (b *Backend) Exists(ctx context.Context, key string) (bool, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "s3.Exists")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.bucket", b.bucket),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	start := now()
	_, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	b.metrics.observeOp(b.instance, "exists", start, s3MetricErr(err))

	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		opErr := storage.WrapSafe("s3backend: exists failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return false, opErr
	}
	return true, nil
}

func s3MetricErr(err error) error {
	if isS3NotFound(err) {
		return nil
	}
	return err
}

func isS3NotFound(err error) bool {
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchKey *types.NoSuchKey
	return errors.As(err, &noSuchKey)
}

// applySSE sets the ServerSideEncryption (and SSEKMSKeyId when applicable)
// fields on a PutObjectInput based on the configured SSE policy. The default
// is "AES256" so buckets without a default-encryption policy still receive
// encrypted objects; callers can opt out by setting cfg.SSE = "".
func validateSSEConfig(cfg Config) error {
	switch cfg.SSE {
	case "", "AES256":
	case "aws:kms":
		if cfg.SSEKMSKeyID == "" {
			return fmt.Errorf("STORAGE_S3_SSE_KMS_KEY_ID is required when STORAGE_S3_SSE=aws:kms")
		}
	default:
		return fmt.Errorf("STORAGE_S3_SSE must be one of empty, AES256, or aws:kms")
	}
	if cfg.SSEKMSKeyID != "" && cfg.SSE != "aws:kms" {
		return fmt.Errorf("STORAGE_S3_SSE_KMS_KEY_ID requires STORAGE_S3_SSE=aws:kms")
	}
	return nil
}

func applySSE(input *s3.PutObjectInput, cfg Config) error {
	if err := validateSSEConfig(cfg); err != nil {
		return err
	}
	switch cfg.SSE {
	case "":
		// Opt-out: don't set anything, rely on bucket policy.
	case "AES256":
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	case "aws:kms":
		input.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		input.SSEKMSKeyId = aws.String(cfg.SSEKMSKeyID)
	}
	return nil
}

func applyCopySSE(input *s3.CopyObjectInput, cfg Config) error {
	if err := validateSSEConfig(cfg); err != nil {
		return err
	}
	switch cfg.SSE {
	case "":
		// Opt-out: don't set anything, rely on bucket policy.
	case "AES256":
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	case "aws:kms":
		input.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		input.SSEKMSKeyId = aws.String(cfg.SSEKMSKeyID)
	}
	return nil
}
