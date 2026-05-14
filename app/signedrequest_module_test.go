package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
)

func TestWithSignedRequests_PanicsOnNilResolver(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil resolver")
		}
	}()
	b.WithSignedRequests(nil, signedrequest.NewMemoryNonceStore(time.Minute))
}

func TestWithSignedRequests_PanicsOnNilStore(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil store (replay risk)")
		}
	}()
	b.WithSignedRequests(func(_ context.Context, _ string) ([]byte, error) { return nil, nil }, nil)
}

func TestWithSignedRequests_RegistersOnBuilder(t *testing.T) {
	b := New("test", "v1", BaseConfig{}).WithSignedRequests(
		func(_ context.Context, _ string) ([]byte, error) { return make([]byte, 32), nil },
		signedrequest.NewMemoryNonceStore(time.Minute),
	)
	assert.NotNil(t, b.signedSpec)
	assert.NotNil(t, b.signedRequestMiddleware())
}

func TestWithSignedRequests_ClonesOptions(t *testing.T) {
	opts := []signedrequest.Option{signedrequest.WithBodyMaxSize(1024)}

	b := New("test", "v1", BaseConfig{}).WithSignedRequests(
		func(_ context.Context, _ string) ([]byte, error) { return make([]byte, 32), nil },
		signedrequest.NewMemoryNonceStore(time.Minute),
		opts...,
	)
	opts[0] = nil

	assert.NotNil(t, b.signedSpec)
	assert.Len(t, b.signedSpec.opts, 1)
	assert.NotNil(t, b.signedSpec.opts[0])
}

func TestSignedRequestMiddleware_NilWhenNotConfigured(t *testing.T) {
	b := New("test", "v1", BaseConfig{})
	assert.Nil(t, b.signedRequestMiddleware())
}
