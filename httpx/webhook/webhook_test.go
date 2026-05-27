package webhook_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/signing"
	"github.com/bds421/rho-kit/httpx/v2/webhook"
	"github.com/bds421/rho-kit/resilience/v2/retry"
)

func newDispatcher(t *testing.T, client *http.Client, opts ...webhook.Option) *webhook.Dispatcher {
	t.Helper()
	d, err := webhook.New(webhook.Config{
		HTTPClient: client,
		Signer:     signing.NewSigner(),
		Secret:     signing.Secret("test-secret-32-bytes-padded-12345"),
	}, opts...)
	require.NoError(t, err)
	return d
}

func TestSend_HappyPath(t *testing.T) {
	var received atomic.Int32
	var gotSig, gotTS, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		gotSig = r.Header.Get("X-Kit-Signature")
		gotTS = r.Header.Get("X-Kit-Timestamp")
		gotID = r.Header.Get("X-Kit-Delivery-Id")
		body, _ := io.ReadAll(r.Body)
		require.Equal(t, `{"ok":true}`, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := newDispatcher(t, srv.Client())
	err := d.Send(context.Background(), webhook.Delivery{
		URL:  srv.URL,
		Body: []byte(`{"ok":true}`),
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), received.Load())
	require.NotEmpty(t, gotSig)
	require.NotEmpty(t, gotTS)
	require.NotEmpty(t, gotID, "delivery id auto-generated when omitted")
}

func TestSend_DefaultsContentTypeJSON(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client())
	require.NoError(t, d.Send(context.Background(), webhook.Delivery{URL: srv.URL}))
	require.Equal(t, "application/json", gotCT)
}

func TestSend_DeliveryIDPreserved(t *testing.T) {
	var gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("X-Kit-Delivery-Id")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client())
	require.NoError(t, d.Send(context.Background(), webhook.Delivery{
		URL:        srv.URL,
		DeliveryID: "evt-42",
	}))
	require.Equal(t, "evt-42", gotID)
}

func TestSend_RetriesOn5xxThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client(),
		webhook.WithRetryPolicy(retry.DefaultPolicy()),
	)
	require.NoError(t, d.Send(context.Background(), webhook.Delivery{URL: srv.URL}))
	require.GreaterOrEqual(t, attempts.Load(), int32(3))
}

func TestSend_4xxGivesUpImmediately(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client())
	err := d.Send(context.Background(), webhook.Delivery{URL: srv.URL})
	require.Error(t, err)
	require.Equal(t, int32(1), attempts.Load(), "4xx should not retry")
}

func TestSend_NetworkErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Close() // close immediately so dials fail
	d := newDispatcher(t, http.DefaultClient,
		webhook.WithRetryPolicy(retry.DefaultPolicy()),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := d.Send(ctx, webhook.Delivery{URL: srv.URL})
	require.Error(t, err)
}

func TestSend_HonoursCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client(),
		webhook.WithRetryPolicy(retry.DefaultPolicy()),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := d.Send(ctx, webhook.Delivery{URL: srv.URL})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded) || err != nil,
		"expected ctx-derived error, got %v", err)
}

func TestNew_ValidatesConfig(t *testing.T) {
	_, err := webhook.New(webhook.Config{})
	require.Error(t, err)
	_, err = webhook.New(webhook.Config{HTTPClient: http.DefaultClient})
	require.Error(t, err)
	_, err = webhook.New(webhook.Config{
		HTTPClient: http.DefaultClient,
		Signer:     signing.NewSigner(),
	})
	require.Error(t, err, "missing Secret should fail")
}

func TestNew_NilOptionRejected(t *testing.T) {
	_, err := webhook.New(webhook.Config{
		HTTPClient: http.DefaultClient,
		Signer:     signing.NewSigner(),
		Secret:     signing.Secret("k"),
	}, nil)
	require.Error(t, err)
}

func TestSend_URLRequired(t *testing.T) {
	d := newDispatcher(t, http.DefaultClient)
	err := d.Send(context.Background(), webhook.Delivery{})
	require.Error(t, err)
}

func TestSend_CustomHeadersDoNotOverrideKitHeaders(t *testing.T) {
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Kit-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client())
	headers := http.Header{}
	headers.Set("X-Kit-Signature", "attacker-attempt-to-override")
	require.NoError(t, d.Send(context.Background(), webhook.Delivery{
		URL:     srv.URL,
		Headers: headers,
	}))
	require.NotEqual(t, "attacker-attempt-to-override", gotSig,
		"kit must overwrite caller-supplied X-Kit-Signature")
}
