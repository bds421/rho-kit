package azurebackend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/storage"
)

const tracerName = "kit/storage/azure"

// Compile-time interface compliance check.
var _ storage.Storage = (*Backend)(nil)

// BlobClient abstracts the Azure Blob container methods used by Backend.
type BlobClient interface {
	UploadStream(ctx context.Context, blobName string, body io.Reader, opts *azblob.UploadStreamOptions) (azblob.UploadStreamResponse, error)
	DownloadStream(ctx context.Context, blobName string, opts *azblob.DownloadStreamOptions) (azblob.DownloadStreamResponse, error)
	DeleteBlob(ctx context.Context, blobName string, opts *azblob.DeleteBlobOptions) (azblob.DeleteBlobResponse, error)
	NewListBlobsFlatPager(opts *container.ListBlobsFlatOptions) BlobPager
}

// BlobPager abstracts the Azure pager for listing blobs.
type BlobPager interface {
	More() bool
	NextPage(ctx context.Context) (container.ListBlobsFlatResponse, error)
}

// Backend implements [storage.Storage] using Azure Blob Storage.
// Safe for concurrent use — all mutable state is owned by the upstream
// Azure SDK BlobClient which is itself goroutine-safe.
type Backend struct {
	client     BlobClient
	container  string
	cfg        Config
	instance   string
	validators []storage.Validator
	metrics    *Metrics
}

// Option configures an Backend.
type Option func(*Backend)

// WithInstance sets the metrics/tracing instance label.
func WithInstance(name string) Option {
	return func(b *Backend) {
		if err := storage.ValidateInstanceName(name); err != nil {
			panic("azurebackend: WithInstance invalid instance name")
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

// WithMetricsRegisterer sets the Prometheus registerer for Azure Blob
// Storage metrics. If not set, prometheus.DefaultRegisterer is used.
// Replaces the v1 WithRegisterer spelling so it no longer collides
// with the metrics-level option of the same name.
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
func New(cfg Config, opts ...Option) (*Backend, error) {
	if cfg.ContainerName == "" {
		return nil, fmt.Errorf("azurebackend: Config.ContainerName is required")
	}
	if err := cfg.Validate(""); err != nil {
		return nil, err
	}

	// Static AccountKey path. SharedKeyCredential never refreshes its
	// key — a rotation event in Azure leaves the kit signing with the
	// old key until the process is restarted. Warn at construction so
	// operators wiring static keys cannot silently miss the rotation
	// gap; production deployments should use [NewWithTokenCredential]
	// (managed identity / workload identity / chained Azure credentials)
	// instead (L120).
	slog.Warn("azurebackend: using static AccountKey — credentials will NOT rotate without a process restart; prefer NewWithTokenCredential for rotating credentials",
		redact.String("account", cfg.AccountName),
		redact.String("container", cfg.ContainerName),
	)

	cred, err := azblob.NewSharedKeyCredential(cfg.AccountName, cfg.AccountKey)
	if err != nil {
		return nil, storage.WrapSafe("azurebackend: create credential failed", err)
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.blob.core.windows.net", cfg.AccountName)
	}

	client, err := azblob.NewClientWithSharedKeyCredential(endpoint, cred, nil)
	if err != nil {
		return nil, storage.WrapSafe("azurebackend: create client failed", err)
	}

	b := &Backend{
		client:    &azureBlobClient{client: client, container: cfg.ContainerName},
		container: cfg.ContainerName,
		cfg:       cfg,
		instance:  "default",
		metrics:   defaultMetrics(),
	}
	for _, o := range opts {
		if o == nil {
			panic("azurebackend: New option must not be nil")
		}
		o(b)
	}
	return b, nil
}

// NewWithTokenCredential creates an Backend using Azure AD / workload
// identity credentials instead of a storage account key. The Azure SDK refreshes
// tokens through cred, so managed identity, workload identity, and chained
// credential rotations are handled without rebuilding the backend.
func NewWithTokenCredential(cfg Config, cred azcore.TokenCredential, opts ...Option) (*Backend, error) {
	if cfg.ContainerName == "" {
		return nil, fmt.Errorf("azurebackend: Config.ContainerName is required")
	}
	if cred == nil {
		return nil, fmt.Errorf("azurebackend: token credential is required")
	}
	if err := cfg.validateTokenCredential(""); err != nil {
		return nil, err
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.blob.core.windows.net", cfg.AccountName)
	}

	client, err := azblob.NewClient(endpoint, cred, nil)
	if err != nil {
		return nil, storage.WrapSafe("azurebackend: create client failed", err)
	}

	b := &Backend{
		client:    &azureBlobClient{client: client, container: cfg.ContainerName},
		container: cfg.ContainerName,
		cfg:       cfg,
		instance:  "default",
		metrics:   defaultMetrics(),
	}
	for _, o := range opts {
		if o == nil {
			panic("azurebackend: NewWithTokenCredential option must not be nil")
		}
		o(b)
	}
	return b, nil
}

// NewWithClient creates an Backend with a custom BlobClient for testing.
func NewWithClient(client BlobClient, containerName string, opts ...Option) *Backend {
	if client == nil {
		panic("azurebackend: NewWithClient requires a non-nil BlobClient")
	}
	if containerName == "" {
		panic("azurebackend: NewWithClient containerName must not be empty")
	}
	b := &Backend{
		client:    client,
		container: containerName,
		instance:  "default",
		metrics:   defaultMetrics(),
	}
	for _, o := range opts {
		if o == nil {
			panic("azurebackend: NewWithClient option must not be nil")
		}
		o(b)
	}
	return b
}

// Close releases any resources held by the backend. The Azure SDK
// HTTP client is stateless from the kit's perspective (idle-connection
// pool is owned by the global http.DefaultTransport tier), so this is
// a documented no-op present only for uniform interface implementation.
func (b *Backend) Close() error { return nil }

// Put uploads content from r to the given blob key.
func (b *Backend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "azure.Put")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.container", b.container),
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

	opts := &azblob.UploadStreamOptions{
		HTTPHeaders: &blob.HTTPHeaders{
			BlobContentType: &contentType,
		},
		Metadata: toAzureMetadata(meta.Custom),
	}

	start := now()
	_, err = b.client.UploadStream(ctx, key, validated, opts)
	b.metrics.observeOp(b.instance, "put", start, err)
	if err != nil {
		if capacity := translateAzureCapacity(err); capacity != nil {
			span.SetStatus(codes.Error, storage.SpanErrorDescription(capacity))
			return capacity
		}
		opErr := storage.WrapSafe("azurebackend: put failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	return nil
}

// translateAzureCapacity inspects an Azure Blob upload error and returns
// [storage.ErrInsufficientCapacity] when the service rejected the upload
// because the storage account / container is at capacity. Returns nil for
// non-capacity errors so the caller can fall back to its generic mapping.
//
// Azure Storage returns HTTP 507 "InsufficientStorage" when an account is
// full (rare; usually surfaces via quota policies) and HTTP 413
// "RequestBodyTooLarge" when a single PUT exceeds the per-blob limit. The
// generic *azcore.ResponseError carries both the status code and the
// service-supplied error code so we can pattern-match safely.
func translateAzureCapacity(err error) error {
	if err == nil {
		return nil
	}
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) {
		return nil
	}
	switch respErr.ErrorCode {
	// "InsufficientAccountPermissions" is deliberately NOT mapped here: it is
	// an authorization / account-disabled condition (HTTP 403), not a
	// capacity failure. Classifying it as ErrInsufficientCapacity would make
	// a misconfigured SAS/RBAC Put satisfy apperror.IsStorageFull and trip
	// capacity alerts instead of surfacing an auth failure. Let it fall
	// through to the caller's generic WrapSafe mapping.
	case "InsufficientStorage":
		return fmt.Errorf("azurebackend: account out of capacity: %w (cause: %w)", storage.ErrInsufficientCapacity, err) // kit:ok-fmt-errorf-wrap
	case "RequestBodyTooLarge":
		return fmt.Errorf("azurebackend: blob exceeds size limit: %w (cause: %w)", storage.ErrInsufficientCapacity, err) // kit:ok-fmt-errorf-wrap
	}
	if respErr.StatusCode == 507 || respErr.StatusCode == 413 {
		return fmt.Errorf("azurebackend: insufficient storage: %w (cause: %w)", storage.ErrInsufficientCapacity, err) // kit:ok-fmt-errorf-wrap
	}
	return nil
}

// Get downloads a blob. Caller must close the returned ReadCloser.
func (b *Backend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "azure.Get")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.container", b.container),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	start := now()
	resp, err := b.client.DownloadStream(ctx, key, nil)
	// Not-found is an expected outcome for object stores (cache miss /
	// CAS probe / orphan reaper sweep). Counting it as an operation
	// error would inflate operation_errors_total against the dashboard
	// contract; route through azureMetricErr so dashboards stay
	// comparable across providers (matches S3 / GCS / SFTP).
	b.metrics.observeOp(b.instance, "get", start, azureMetricErr(err))
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("azurebackend: get: %w", storage.ErrObjectNotFound)
		}
		opErr := storage.WrapSafe("azurebackend: get failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return nil, storage.ObjectMeta{}, opErr
	}

	meta := storage.ObjectMeta{
		Custom: fromAzureMetadata(resp.Metadata),
	}
	if resp.ContentType != nil {
		meta.ContentType = *resp.ContentType
	}
	if resp.ContentLength != nil {
		meta.Size = *resp.ContentLength
	}
	if resp.ETag != nil {
		meta.ETag = string(*resp.ETag)
	}
	if resp.LastModified != nil {
		meta.LastModified = *resp.LastModified
	}

	return resp.Body, meta, nil
}

// Delete removes a blob. Returns nil if the blob does not exist.
func (b *Backend) Delete(ctx context.Context, key string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "azure.Delete")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.container", b.container),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	start := now()
	_, err := b.client.DeleteBlob(ctx, key, nil)
	b.metrics.observeOp(b.instance, "delete", start, azureMetricErr(err))
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil
		}
		opErr := storage.WrapSafe("azurebackend: delete failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return opErr
	}
	return nil
}

// Exists checks whether a blob exists using a single-byte ranged
// DownloadStream. A zero-byte blob (markers, .keep, placeholders) cannot
// satisfy any byte range, so Azure answers with HTTP 416 / error code
// InvalidRange even though the blob is present; that case is treated as
// exists=true to match the metadata-probe semantics of s3backend
// (HeadObject) and gcsbackend (Attrs).
func (b *Backend) Exists(ctx context.Context, key string) (bool, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "azure.Exists")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.container", b.container),
		attribute.Int("storage.key_len", len(key)),
	)

	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	start := now()
	resp, err := b.client.DownloadStream(ctx, key, &azblob.DownloadStreamOptions{
		Range: blob.HTTPRange{Offset: 0, Count: 1},
	})
	b.metrics.observeOp(b.instance, "exists", start, azureExistsMetricErr(err))
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return false, nil
		}
		// An empty blob exists but has no byte to range over; Azure
		// reports InvalidRange (416) rather than returning a body.
		if bloberror.HasCode(err, bloberror.InvalidRange) {
			return true, nil
		}
		opErr := storage.WrapSafe("azurebackend: exists failed", err)
		span.SetStatus(codes.Error, storage.SpanErrorDescription(opErr))
		return false, opErr
	}
	_ = resp.Body.Close()
	return true, nil
}

func azureMetricErr(err error) error {
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return nil
	}
	return err
}

// azureExistsMetricErr classifies a DownloadStream error from the Exists
// probe for metrics. Not-found is an expected probe outcome, and an empty
// blob answers a ranged read with InvalidRange while still existing; neither
// is an operation error, so both are suppressed to keep
// operation_errors_total comparable with the other backends.
func azureExistsMetricErr(err error) error {
	if bloberror.HasCode(err, bloberror.InvalidRange) {
		return nil
	}
	return azureMetricErr(err)
}

// azureBlobClient wraps the real Azure SDK client to implement BlobClient.
type azureBlobClient struct {
	client    *azblob.Client
	container string
}

func (c *azureBlobClient) UploadStream(ctx context.Context, blobName string, body io.Reader, opts *azblob.UploadStreamOptions) (azblob.UploadStreamResponse, error) {
	return c.client.UploadStream(ctx, c.container, blobName, body, opts)
}

func (c *azureBlobClient) DownloadStream(ctx context.Context, blobName string, opts *azblob.DownloadStreamOptions) (azblob.DownloadStreamResponse, error) {
	return c.client.DownloadStream(ctx, c.container, blobName, opts)
}

func (c *azureBlobClient) DeleteBlob(ctx context.Context, blobName string, opts *azblob.DeleteBlobOptions) (azblob.DeleteBlobResponse, error) {
	return c.client.DeleteBlob(ctx, c.container, blobName, opts)
}

func (c *azureBlobClient) NewListBlobsFlatPager(opts *container.ListBlobsFlatOptions) BlobPager {
	return c.client.NewListBlobsFlatPager(c.container, opts)
}

// Healthy reports whether the container is accessible.
func (b *Backend) Healthy() bool {
	if b == nil || b.client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pager := b.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{
		MaxResults: ptrInt32(1),
	})
	if !pager.More() {
		return true
	}
	_, err := pager.NextPage(ctx)
	return err == nil
}

func ptrInt32(v int32) *int32 { return &v }

// toAzureMetadata converts map[string]string to Azure's map[string]*string.
func toAzureMetadata(m map[string]string) map[string]*string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]*string, len(m))
	for k, v := range m {
		v := v
		out[k] = &v
	}
	return out
}

// fromAzureMetadata converts Azure's map[string]*string to map[string]string.
func fromAzureMetadata(m map[string]*string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}
