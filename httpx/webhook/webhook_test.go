package webhook_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/crypto/v2/signing"
	"github.com/bds421/rho-kit/httpx/v2/webhook"
	"github.com/bds421/rho-kit/resilience/v2/retry"
)

func newDispatcher(t *testing.T, client *http.Client, opts ...webhook.Option) *webhook.Dispatcher {
	return newDispatcherWithSigner(t, client, signing.NewSigner(), opts...)
}

func newDispatcherWithSigner(t *testing.T, client *http.Client, signer *signing.Signer, opts ...webhook.Option) *webhook.Dispatcher {
	t.Helper()
	d, err := webhook.New(webhook.Config{
		HTTPClient:               client,
		Signer:                   signer,
		Secret:                   signing.Secret("test-secret-32-bytes-padded-12345"),
		AllowPrivateDestinations: true, // httptest + local receivers
	}, opts...)
	require.NoError(t, err)
	return d
}

func fastRetryPolicy() retry.Policy {
	p := retry.DefaultPolicy()
	p.BaseDelay = time.Millisecond
	p.MaxDelay = time.Millisecond
	p.Factor = 1
	p.Jitter = 0
	return p
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

// countingBody reports how many bytes were read from it and never reaches EOF
// within the drain window, emulating a hostile receiver streaming an unbounded
// response body.
type countingBody struct {
	read atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.read.Add(int64(len(p)))
	return len(p), nil
}

func (b *countingBody) Close() error { return nil }

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestSend_BoundsResponseBodyDrain verifies that a 2xx delivery whose receiver
// streams an unbounded response body does not read the whole stream: the
// dispatcher drains at most a bounded amount for keep-alive reuse, then closes.
func TestSend_BoundsResponseBodyDrain(t *testing.T) {
	body := &countingBody{}
	client := &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       body,
				Header:     make(http.Header),
			}, nil
		}),
	}

	d := newDispatcher(t, client)
	err := d.Send(context.Background(), webhook.Delivery{
		URL:  "https://receiver.example/hook",
		Body: []byte(`{"ok":true}`),
	})
	require.NoError(t, err)

	// The drain must be bounded: well under the unbounded stream the receiver
	// would otherwise feed. Allow a small slack over the cap for the final
	// io.CopyN buffer chunk.
	const cap = 64 * 1024
	require.LessOrEqual(t, body.read.Load(), int64(cap)+64*1024,
		"response body drain must be bounded, read %d bytes", body.read.Load())
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
		webhook.WithRetryPolicy(fastRetryPolicy()),
	)
	require.NoError(t, d.Send(context.Background(), webhook.Delivery{URL: srv.URL}))
	require.GreaterOrEqual(t, attempts.Load(), int32(3))
}

// TestSend_TimestampFreshPerAttempt verifies the B1 fix: each retry
// re-signs with a NEW timestamp so a slow retry stays inside the
// receiver's signing.Verify maxAge window. Otherwise a single-sign
// dispatcher would silently rot past 30s+ of cumulative backoff.
func TestSend_TimestampFreshPerAttempt(t *testing.T) {
	var (
		mu         sync.Mutex
		timestamps []string
		attempts   atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		timestamps = append(timestamps, r.Header.Get("X-Kit-Timestamp"))
		mu.Unlock()
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	var clockTick atomic.Int64
	baseTime := time.Unix(1_700_000_000, 0)
	signer := signing.NewSigner(signing.WithClock(func() time.Time {
		return baseTime.Add(time.Duration(clockTick.Add(1)) * time.Second)
	}))
	d := newDispatcherWithSigner(t, srv.Client(), signer,
		webhook.WithRetryPolicy(fastRetryPolicy()),
	)
	require.NoError(t, d.Send(context.Background(), webhook.Delivery{URL: srv.URL}))
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(timestamps), 3)
	// The whole point: timestamps must NOT all be identical. If the
	// sign was outside the loop, every retry would carry the same
	// X-Kit-Timestamp and we'd see len(unique)=1.
	unique := map[string]struct{}{}
	for _, ts := range timestamps {
		unique[ts] = struct{}{}
	}
	require.Greater(t, len(unique), 1, "retries reused the same timestamp; got %v — sign moved outside the closure?", timestamps)
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
		webhook.WithRetryPolicy(fastRetryPolicy()),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := d.Send(ctx, webhook.Delivery{URL: srv.URL})
	require.Error(t, err)
}

func TestSend_HonoursCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		cancel()
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client(),
		webhook.WithRetryPolicy(fastRetryPolicy()),
	)
	err := d.Send(ctx, webhook.Delivery{URL: srv.URL})
	require.Error(t, err)
	// The receiver returns a retryable status and cancels the caller context.
	// Send must stop before exhausting its retry budget and retain the
	// cancellation cause in the returned error chain.
	require.ErrorIs(t, err, context.Canceled,
		"expected context cancellation to terminate retries, got %v", err)
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
		Secret:     signing.Secret("test-secret-32-bytes-padded-12345"),
	}, nil)
	require.Error(t, err)
}

// TestNew_RejectsShortSecret guards against the failure mode where New
// accepts a non-empty-but-too-short secret and then every Send fails at
// runtime: crypto/signing rejects secrets below 32 bytes, so New must
// enforce the same floor at construction time (matching sibling
// constructors like NewCursorSigner / NewStaticKeyStore).
func TestNew_RejectsShortSecret(t *testing.T) {
	tests := []struct {
		name    string
		secret  signing.Secret
		wantErr bool
	}{
		{"empty", signing.Secret(""), true},
		{"one byte", signing.Secret("k"), true},
		{"31 bytes", signing.Secret("0123456789012345678901234567890"), true},
		{"32 bytes", signing.Secret("01234567890123456789012345678901"), false},
		{"33 bytes", signing.Secret("012345678901234567890123456789012"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := webhook.New(webhook.Config{
				HTTPClient: http.DefaultClient,
				Signer:     signing.NewSigner(),
				Secret:     tt.secret,
			})
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSend_URLRequired(t *testing.T) {
	d := newDispatcher(t, http.DefaultClient)
	err := d.Send(context.Background(), webhook.Delivery{})
	require.Error(t, err)
}

// TestSend_RejectsNonHTTPSchemes is an SSRF hardening guard: the
// dispatcher signs a body and POSTs it to a caller/customer-supplied
// URL, so a non-http(s) scheme (file://, gopher://, etc.) must be
// rejected before any request is built — the kit only ever speaks
// HTTP(S). The error must be permanent (no retry) and never reach the
// transport.
func TestSend_RejectsNonHTTPSchemes(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"file scheme", "file:///etc/passwd", true},
		{"gopher scheme", "gopher://127.0.0.1:6379/_INFO", true},
		{"ftp scheme", "ftp://internal/secret", true},
		{"no scheme", "internal.host/path", true},
		{"uppercase HTTPS allowed", "HTTPS://example.com/hook", false},
		{"http allowed", "http://example.com/hook", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
				hits.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(http.NoBody),
				}, nil
			})
			d := newDispatcher(t, &http.Client{Transport: rt})
			err := d.Send(context.Background(), webhook.Delivery{URL: tt.url})
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, int32(0), hits.Load(),
					"rejected scheme must never reach the transport")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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

func TestSend_RetriesOn429ThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client(),
		webhook.WithRetryPolicy(fastRetryPolicy()),
	)
	require.NoError(t, d.Send(context.Background(), webhook.Delivery{URL: srv.URL}))
	require.GreaterOrEqual(t, attempts.Load(), int32(3), "429 must be retried")
}

func TestSend_RetriesOn408ThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 2 {
			w.WriteHeader(http.StatusRequestTimeout)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := newDispatcher(t, srv.Client(),
		webhook.WithRetryPolicy(fastRetryPolicy()),
	)
	require.NoError(t, d.Send(context.Background(), webhook.Delivery{URL: srv.URL}))
	require.GreaterOrEqual(t, attempts.Load(), int32(2), "408 must be retried")
}

// TestNew_InstallsSafeCheckRedirectWhenUnset verifies New clones a client
// without CheckRedirect and installs the SSRF-safe policy.
func TestNew_InstallsSafeCheckRedirectWhenUnset(t *testing.T) {
	client := &http.Client{}
	d, err := webhook.New(webhook.Config{
		HTTPClient: client,
		Signer:     signing.NewSigner(),
		Secret:     signing.Secret("test-secret-32-bytes-padded-12345"),
	})
	require.NoError(t, err)
	require.NotNil(t, d)

	// Original must not be mutated.
	require.Nil(t, client.CheckRedirect, "New must clone, not mutate caller's client")
}

// TestNew_PreservesExplicitCheckRedirect pins that an already-configured
// redirect policy is never overwritten (kit resilient clients block all
// redirects).
func TestNew_PreservesExplicitCheckRedirect(t *testing.T) {
	sentinel := errors.New("caller policy")
	policy := func(*http.Request, []*http.Request) error { return sentinel }
	client := &http.Client{CheckRedirect: policy}
	d, err := webhook.New(webhook.Config{
		HTTPClient: client,
		Signer:     signing.NewSigner(),
		Secret:     signing.Secret("test-secret-32-bytes-padded-12345"),
	})
	require.NoError(t, err)
	require.NotNil(t, d)
	// Caller's client and its CheckRedirect must be untouched.
	require.Equal(t, fmt.Sprintf("%p", policy), fmt.Sprintf("%p", client.CheckRedirect))
	err = client.CheckRedirect(nil, nil)
	require.ErrorIs(t, err, sentinel)
}

// TestSend_BlocksRedirectToPrivateIP is the SSRF-via-redirect regression:
// a receiver that 302s to a link-local/metadata address must not receive
// the signed delivery body. The default CheckRedirect refuses private nets.
func TestSend_BlocksRedirectToPrivateIP(t *testing.T) {
	// Redirector returns a 302 to a literal private IP URL. CheckRedirect
	// must refuse before following (no dial to 169.254.169.254).
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redirector.Close()

	client := redirector.Client()
	client.CheckRedirect = nil // so New installs the safe policy

	d := newDispatcher(t, client, webhook.WithRetryPolicy(retry.Policy{
		MaxRetries: 0,
		BaseDelay:  time.Millisecond,
		MaxDelay:   time.Millisecond,
		Factor:     1,
		Jitter:     0,
	}))

	err := d.Send(context.Background(), webhook.Delivery{
		URL:  redirector.URL,
		Body: []byte(`{"ok":true}`),
	})
	require.Error(t, err, "redirect to private IP must fail the delivery")
}

// TestSend_BlocksRedirectToNonHTTPS refuses http upgrade targets that are
// not https — the default policy only allows public https hops.
func TestSend_BlocksRedirectToNonHTTPS(t *testing.T) {
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer next.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, next.URL, http.StatusFound) // http, not https
	}))
	defer redirector.Close()

	client := redirector.Client()
	client.CheckRedirect = nil

	d := newDispatcher(t, client, webhook.WithRetryPolicy(retry.Policy{
		MaxRetries: 0,
		BaseDelay:  time.Millisecond,
		MaxDelay:   time.Millisecond,
		Factor:     1,
		Jitter:     0,
	}))

	err := d.Send(context.Background(), webhook.Delivery{
		URL:  redirector.URL,
		Body: []byte(`{"ok":true}`),
	})
	require.Error(t, err, "redirect to non-https must be refused")
}
