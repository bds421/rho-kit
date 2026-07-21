package encryption

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/storage"
	"github.com/bds421/rho-kit/infra/v2/storage/membackend"
)

func TestWithRegisterer_PanicsOnNil(t *testing.T) {
	require.Panics(t, func() {
		WithRegisterer(nil)
	})
}

func TestNewMetricsReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(WithRegisterer(reg))
	m2 := NewMetrics(WithRegisterer(reg))
	assert.Equal(t, m1.openReaderAcquire, m2.openReaderAcquire)
	assert.Equal(t, m1.openReaderWait, m2.openReaderWait)
}

func TestOpenReaderAcquireMetrics_OkAndTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := prometheus.NewRegistry()

	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)),
		WithMaxOpenPlaintextReaders(1),
		WithMetricsRegisterer(reg),
	)

	require.NoError(t, enc.Put(ctx, "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))

	rc1, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)

	// Immediate acquire → result=ok.
	inner, ok := enc.(*EncryptedStorage)
	if !ok {
		// New may wrap as encryptedLister when backend lists; membackend does.
		if l, lok := enc.(*encryptedLister); lok {
			inner = l.EncryptedStorage
		}
	}
	require.NotNil(t, inner)
	require.NotNil(t, inner.metrics)
	assert.Equal(t, float64(1), testutil.ToFloat64(inner.metrics.openReaderAcquire.WithLabelValues("ok")))

	// Saturated budget → timeout on short deadline.
	waitCtx, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	defer cancel()
	_, _, err = enc.Get(waitCtx, "file.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, float64(1), testutil.ToFloat64(inner.metrics.openReaderAcquire.WithLabelValues("timeout")))
	assert.Equal(t, float64(0), testutil.ToFloat64(inner.metrics.openReaderAcquire.WithLabelValues("canceled")))

	// Wait histogram observed for both ok and timeout attempts.
	families, err := reg.Gather()
	require.NoError(t, err)
	foundWait := false
	for _, mf := range families {
		if mf.GetName() == "storage_encryption_open_reader_wait_seconds" {
			foundWait = true
			require.NotEmpty(t, mf.GetMetric())
			// sample_count should be at least 2 (ok + timeout)
			count := mf.GetMetric()[0].GetHistogram().GetSampleCount()
			assert.GreaterOrEqual(t, count, uint64(2))
		}
	}
	assert.True(t, foundWait, "storage_encryption_open_reader_wait_seconds must be registered")

	require.NoError(t, rc1.Close())
}

func TestOpenReaderAcquireMetrics_Canceled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := prometheus.NewRegistry()

	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)),
		WithMaxOpenPlaintextReaders(1),
		WithMetricsRegisterer(reg),
	)
	require.NoError(t, enc.Put(ctx, "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))

	rc1, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	defer func() { _ = rc1.Close() }()

	// Hold the slot and cancel while the second Get waits.
	waitCtx, cancel := context.WithCancel(ctx)
	// Cancel after a short delay so the Get is blocked on openSem.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, _, err = enc.Get(waitCtx, "file.txt")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	inner := encryptionInner(t, enc)
	assert.Equal(t, float64(1), testutil.ToFloat64(inner.metrics.openReaderAcquire.WithLabelValues("canceled")))
}

func TestOpenReaderAcquireMetrics_OptionalWhenUnset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := membackend.New()
	enc := New(backend, StaticKey(testKey(t)), WithMaxOpenPlaintextReaders(1))
	require.NoError(t, enc.Put(ctx, "file.txt", bytes.NewReader([]byte("payload")), storage.ObjectMeta{}))
	rc, _, err := enc.Get(ctx, "file.txt")
	require.NoError(t, err)
	_ = rc.Close()
	assert.Nil(t, encryptionInner(t, enc).metrics)
}

func encryptionInner(t *testing.T, s storage.Storage) *EncryptedStorage {
	t.Helper()
	switch v := s.(type) {
	case *EncryptedStorage:
		return v
	case *encryptedLister:
		return v.EncryptedStorage
	default:
		t.Fatalf("unexpected storage type %T", s)
		return nil
	}
}
