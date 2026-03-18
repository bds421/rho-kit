package azurebackend

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/bds421/rho-kit/infra/storage"
)

const tracerName = "kit/storage/azure"

// Compile-time interface compliance check.
var _ storage.Storage = (*AzureBackend)(nil)

// BlobClient abstracts the Azure Blob container methods used by AzureBackend.
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

// AzureBackend implements [storage.Storage] using Azure Blob Storage.
type AzureBackend struct {
	client     BlobClient
	container  string
	cfg        AzureConfig
	instance   string
	validators []storage.Validator
}

// Option configures an AzureBackend.
type Option func(*AzureBackend)

// WithInstance sets the metrics/tracing instance label.
func WithInstance(name string) Option {
	return func(b *AzureBackend) {
		if name == "" {
			panic("azurebackend: instance name must not be empty")
		}
		b.instance = name
	}
}

// WithValidators sets upload validators applied in order before every Put.
func WithValidators(validators ...storage.Validator) Option {
	return func(b *AzureBackend) {
		b.validators = append(b.validators, validators...)
	}
}

// New creates a new AzureBackend from config.
func New(cfg AzureConfig, opts ...Option) (*AzureBackend, error) {
	if cfg.ContainerName == "" {
		panic("azurebackend: AzureConfig.ContainerName is required")
	}

	cred, err := azblob.NewSharedKeyCredential(cfg.AccountName, cfg.AccountKey)
	if err != nil {
		return nil, fmt.Errorf("azurebackend: create credential: %w", err)
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.blob.core.windows.net", cfg.AccountName)
	}

	client, err := azblob.NewClientWithSharedKeyCredential(endpoint, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azurebackend: create client: %w", err)
	}

	b := &AzureBackend{
		client:    &azureBlobClient{client: client, container: cfg.ContainerName},
		container: cfg.ContainerName,
		cfg:       cfg,
		instance:  "default",
	}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// NewWithClient creates an AzureBackend with a custom BlobClient for testing.
func NewWithClient(client BlobClient, containerName string, opts ...Option) *AzureBackend {
	if containerName == "" {
		panic("azurebackend: containerName must not be empty")
	}
	b := &AzureBackend{
		client:    client,
		container: containerName,
		instance:  "default",
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Put uploads content from r to the given blob key.
func (b *AzureBackend) Put(ctx context.Context, key string, r io.Reader, meta storage.ObjectMeta) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "azure.Put")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.container", b.container),
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

	opts := &azblob.UploadStreamOptions{
		HTTPHeaders: &blob.HTTPHeaders{
			BlobContentType: &contentType,
		},
		Metadata: toAzureMetadata(meta.Custom),
	}

	_, err = b.client.UploadStream(ctx, key, validated, opts)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("azurebackend: put %q: %w", key, err)
	}
	return nil
}

// Get downloads a blob. Caller must close the returned ReadCloser.
func (b *AzureBackend) Get(ctx context.Context, key string) (io.ReadCloser, storage.ObjectMeta, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "azure.Get")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.container", b.container),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return nil, storage.ObjectMeta{}, err
	}

	resp, err := b.client.DownloadStream(ctx, key, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil, storage.ObjectMeta{}, fmt.Errorf("azurebackend: get %q: %w", key, storage.ErrObjectNotFound)
		}
		span.SetStatus(codes.Error, err.Error())
		return nil, storage.ObjectMeta{}, fmt.Errorf("azurebackend: get %q: %w", key, err)
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
func (b *AzureBackend) Delete(ctx context.Context, key string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "azure.Delete")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.container", b.container),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return err
	}

	_, err := b.client.DeleteBlob(ctx, key, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil
		}
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("azurebackend: delete %q: %w", key, err)
	}
	return nil
}

// Exists checks whether a blob exists using DownloadStream with range 0-0.
func (b *AzureBackend) Exists(ctx context.Context, key string) (bool, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "azure.Exists")
	defer span.End()
	span.SetAttributes(
		attribute.String("storage.container", b.container),
		attribute.String("storage.key", key),
	)

	if err := storage.ValidateKey(key); err != nil {
		return false, err
	}

	resp, err := b.client.DownloadStream(ctx, key, &azblob.DownloadStreamOptions{
		Range: blob.HTTPRange{Offset: 0, Count: 1},
	})
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return false, nil
		}
		span.SetStatus(codes.Error, err.Error())
		return false, fmt.Errorf("azurebackend: exists %q: %w", key, err)
	}
	_ = resp.Body.Close()
	return true, nil
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
func (b *AzureBackend) Healthy() bool {
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
