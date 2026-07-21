// Package webhook — implementation.
package webhook

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/crypto/v2/signing"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
	"github.com/bds421/rho-kit/resilience/v2/retry"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// Default header names. The dispatcher signs the body only (via
// [crypto/signing] Signer.Sign), so receivers verify deliveries with
// [crypto/signing.Verify], reading the timestamp and signature from
// this triplet. (This wire format differs from
// [httpx/middleware/signedrequest], which expects its own
// X-Signature-* headers over a method/path/nonce canonical string and
// will not validate these X-Kit-* deliveries.)
const (
	DefaultSignatureHeader = "X-Kit-Signature"
	DefaultTimestampHeader = "X-Kit-Timestamp"
	DefaultIDHeader        = "X-Kit-Delivery-Id"
)

// minSecretLen mirrors crypto/signing's unexported 32-byte HMAC-SHA256
// secret floor. New rejects shorter secrets so misconfiguration surfaces
// at construction instead of failing every Send at runtime.
const minSecretLen = 32

// maxDrainBytes caps how many response-body bytes are drained for keep-alive
// connection reuse after a delivery attempt. A webhook receiver's body is
// never consumed by the dispatcher (only the status code matters), so we drain
// just enough to allow connection reuse without letting a hostile endpoint
// stream unbounded data into io.Discard.
const maxDrainBytes = 64 * 1024

// Config configures a [Dispatcher].
type Config struct {
	// HTTPClient is used for every Send. Required. Pass
	// [httpx.NewResilientHTTPClient] for kit-default mTLS-aware
	// transport + per-attempt timeouts.
	HTTPClient *http.Client
	// Signer is the HMAC signer used to mint X-Kit-Signature on
	// every delivery. Required.
	Signer *signing.Signer
	// Secret is the HMAC key. Required.
	Secret signing.Secret
	// AllowPrivateDestinations permits Delivery.URL hosts that resolve to
	// private/link-local/metadata addresses. Default false (SSRF-safe).
	// Set true only for VPC-internal receivers or local e2e tests.
	AllowPrivateDestinations bool
}

// Dispatcher sends signed outbound webhooks. Safe for concurrent use.
type Dispatcher struct {
	cfg             Config
	logger          *slog.Logger
	signatureHeader string
	timestampHeader string
	idHeader        string
	retryPolicy     retry.Policy
}

// Option configures a [Dispatcher].
type Option func(*Dispatcher)

// WithLogger overrides slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(d *Dispatcher) { d.logger = l }
}

// WithSignatureHeader overrides DefaultSignatureHeader.
func WithSignatureHeader(name string) Option {
	if name == "" {
		panic("webhook: WithSignatureHeader requires non-empty name")
	}
	return func(d *Dispatcher) { d.signatureHeader = name }
}

// WithTimestampHeader overrides DefaultTimestampHeader.
func WithTimestampHeader(name string) Option {
	if name == "" {
		panic("webhook: WithTimestampHeader requires non-empty name")
	}
	return func(d *Dispatcher) { d.timestampHeader = name }
}

// WithIDHeader overrides DefaultIDHeader.
func WithIDHeader(name string) Option {
	if name == "" {
		panic("webhook: WithIDHeader requires non-empty name")
	}
	return func(d *Dispatcher) { d.idHeader = name }
}

// WithRetryPolicy overrides the default retry policy
// ([retry.DefaultPolicy]). Use for tighter or looser retry budgets.
func WithRetryPolicy(p retry.Policy) Option {
	return func(d *Dispatcher) { d.retryPolicy = p }
}

// New constructs a Dispatcher. Validates Config; returns an error
// rather than panicking so callers can surface misconfiguration up to
// startup wiring.
func New(cfg Config, opts ...Option) (*Dispatcher, error) {
	if cfg.HTTPClient == nil {
		return nil, errors.New("webhook: Config.HTTPClient is required")
	}
	if cfg.Signer == nil {
		return nil, errors.New("webhook: Config.Signer is required")
	}
	if len(cfg.Secret) == 0 {
		return nil, errors.New("webhook: Config.Secret is required (non-empty)")
	}
	if len(cfg.Secret) < minSecretLen {
		// crypto/signing rejects secrets below this floor at Sign time;
		// enforce it here so a too-short secret fails fast at
		// construction instead of on every runtime Send.
		return nil, fmt.Errorf("webhook: Config.Secret must be at least %d bytes", minSecretLen)
	}
	// Install a fail-closed redirect policy when the caller did not
	// supply one. A zero-value http.Client (and any client without
	// CheckRedirect) follows up to 10 redirects and re-sends the signed
	// body + X-Kit-* headers to the redirect target — a customer-
	// controlled 302 can therefore pivot a valid signature onto an
	// internal address (classic SSRF-via-redirect). Callers that already
	// set CheckRedirect (e.g. httpx.NewResilientHTTPClient, which blocks
	// all redirects) are left untouched.
	if cfg.HTTPClient.CheckRedirect == nil {
		cloned := *cfg.HTTPClient
		cloned.CheckRedirect = safeWebhookCheckRedirect
		cfg.HTTPClient = &cloned
	}

	d := &Dispatcher{
		cfg:             cfg,
		signatureHeader: DefaultSignatureHeader,
		timestampHeader: DefaultTimestampHeader,
		idHeader:        DefaultIDHeader,
		retryPolicy:     retry.DefaultPolicy(),
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("webhook: option must not be nil")
		}
		opt(d)
	}
	if d.logger == nil {
		d.logger = slog.Default()
	}
	return d, nil
}

// Delivery is one outbound webhook.
type Delivery struct {
	// URL is the absolute target URL. Required.
	URL string
	// Body is the request body. May be empty; signature still computed.
	Body []byte
	// ContentType is the Content-Type header value. Defaults to
	// "application/json" when empty.
	ContentType string
	// Headers carries additional caller-supplied headers. The kit's
	// signature / timestamp / delivery-id headers are set by Send and
	// take precedence over caller-supplied entries with the same name.
	Headers http.Header
	// DeliveryID is a stable identifier transmitted as X-Kit-Delivery-Id
	// for receiver-side idempotency / dedupe stores. Generated by Send if
	// empty.
	//
	// It is NOT covered by the HMAC signature (which MACs timestamp+body
	// only). Delivery-ID dedupe is an operational convenience, not a
	// cryptographic anti-replay control — receivers that need crypto-bound
	// replay protection must key on the signed timestamp+body (or verify
	// via a CanonicalContext that includes a delivery nonce).
	DeliveryID string
}

// Send delivers the webhook synchronously with retry. Returns nil on
// any 2xx response. Retries on transport errors and 5xx; gives up on
// 4xx (the receiver said "your fault — don't retry").
//
// The retry policy supplied via WithRetryPolicy bounds the total
// attempts; ctx cancellation halts the loop immediately.
//
// Each attempt re-signs with a FRESH timestamp so a retry that lands
// minutes after the original Send still sits inside the receiver's
// signing.Verify maxAge window. Signing once outside the loop would
// silently get rejected as "expired signature" past the first 30s
// (signing's default future-skew) on retries with long backoff.
func (d *Dispatcher) Send(ctx context.Context, del Delivery) error {
	if del.URL == "" {
		return errors.New("webhook: Delivery.URL is required")
	}
	if err := validateURLWithOpts(del.URL, d.cfg.AllowPrivateDestinations); err != nil {
		return err
	}
	if del.DeliveryID == "" {
		del.DeliveryID = id.New()
	}
	if del.ContentType == "" {
		del.ContentType = "application/json"
	}

	return retry.DoWith(ctx, d.retryPolicy, func(ctx context.Context) error {
		signature, timestamp, err := d.cfg.Signer.Sign(d.cfg.Secret, del.Body)
		if err != nil {
			// Signing failures are permanent (programmer bug — bad
			// secret, broken signer). Mark non-retryable.
			return permanent(fmt.Errorf("webhook: sign: %w", err))
		}
		return d.attempt(ctx, del, signature, timestamp)
	}, retry.WithRetryIf(isRetryable))
}

func validateURLWithOpts(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return permanent(fmt.Errorf("webhook: parse Delivery.URL: %w", err))
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return permanent(fmt.Errorf("webhook: Delivery.URL scheme %q not allowed (must be http or https)", u.Scheme))
	}
	host := u.Hostname()
	if host == "" {
		return permanent(fmt.Errorf("webhook: Delivery.URL host is empty"))
	}
	if allowPrivate {
		return nil
	}
	if _, err := netutil.ResolveAndValidate(context.Background(), host, nil); err != nil {
		return permanent(fmt.Errorf("webhook: Delivery.URL host rejected: %w", err))
	}
	return nil
}

// errWebhookRedirectRefused is returned from the default CheckRedirect when
// a delivery response tries to redirect to an unsafe target. It is not
// exported: callers observe it as a transport error from Client.Do.
var errWebhookRedirectRefused = errors.New("webhook: redirect refused")

// safeWebhookCheckRedirect is the default CheckRedirect installed by [New]
// when Config.HTTPClient has none. It refuses any redirect that is not
// https, or whose host resolves to a private/reserved address (via
// [netutil.ResolveAndValidate]). Allowing only public https hops closes
// the SSRF-via-redirect pivot where a customer endpoint 302s a signed
// delivery onto 169.254.169.254 / an internal admin API.
//
// Callers that need different redirect policy (block all, allow private
// for local e2e, etc.) must set HTTPClient.CheckRedirect themselves
// before calling [New]; an explicit policy is never overwritten.
func safeWebhookCheckRedirect(req *http.Request, via []*http.Request) error {
	if req == nil || req.URL == nil {
		return errWebhookRedirectRefused
	}
	if strings.ToLower(req.URL.Scheme) != "https" {
		return fmt.Errorf("%w: non-https scheme %q", errWebhookRedirectRefused, req.URL.Scheme)
	}
	host := req.URL.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", errWebhookRedirectRefused)
	}
	// ResolveAndValidate rejects private/reserved/link-local/metadata
	// addresses and empty/control hosts. Use the request context so a
	// cancelled Send aborts DNS promptly.
	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := netutil.ResolveAndValidate(ctx, host, nil); err != nil {
		return fmt.Errorf("%w: %v", errWebhookRedirectRefused, err)
	}
	// Cap the hop count at Go's default (10) so a long public-https chain
	// cannot burn the Send retry budget unbounded.
	if len(via) >= 10 {
		return fmt.Errorf("%w: stopped after 10 redirects", errWebhookRedirectRefused)
	}
	return nil
}

// attempt performs one HTTP delivery. Returns nil on 2xx, a wrapped
// retryable error on 5xx / network failures, and a permanent error
// on 4xx (so the retry policy gives up).
func (d *Dispatcher) attempt(ctx context.Context, del Delivery, signature string, timestamp int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, del.URL, bytes.NewReader(del.Body))
	if err != nil {
		return permanent(fmt.Errorf("webhook: build request: %w", err))
	}
	for k, vs := range del.Headers {
		// Content-Type is owned by Delivery.ContentType (set below);
		// skip caller entries so we never emit a duplicate header.
		if strings.EqualFold(k, "Content-Type") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// Kit headers (and Content-Type) take precedence over caller-supplied headers.
	req.Header.Set("Content-Type", del.ContentType)
	req.Header.Set(d.signatureHeader, signature)
	req.Header.Set(d.timestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(d.idHeader, del.DeliveryID)

	resp, err := d.cfg.HTTPClient.Do(req)
	if err != nil {
		if isPermanentTransportError(err) {
			return permanent(fmt.Errorf("webhook: do: %w", err))
		}
		return retryable(fmt.Errorf("webhook: do: %w", err))
	}
	defer func() {
		// Drain a BOUNDED amount so a hostile or broken receiver streaming an
		// unbounded body cannot tie up Send (and bandwidth) until the client
		// timeout — and indefinitely if the supplied http.Client has no
		// Timeout. Draining only what we need for connection reuse mirrors the
		// kit's security/netutil FR-016 defense. Bytes beyond the cap are left
		// unread; Close then tears down the (now non-reusable) connection.
		_, _ = io.CopyN(io.Discard, resp.Body, maxDrainBytes)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		d.logger.Debug("webhook delivered",
			slog.String("url", logURL(del.URL)),
			slog.String("delivery_id", del.DeliveryID),
			slog.Int("status", resp.StatusCode),
		)
		return nil
	}
	// 429 / 408 are transient receiver pressure — retry (honour Retry-After
	// at the policy layer via retry.DoWith; we only classify retryability).
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		d.logger.Warn("webhook rate-limited/timeout; will retry",
			slog.String("url", logURL(del.URL)),
			slog.String("delivery_id", del.DeliveryID),
			slog.Int("status", resp.StatusCode),
		)
		return retryable(fmt.Errorf("webhook: receiver returned %d", resp.StatusCode))
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// Redirect responses that survived CheckRedirect are permanent —
		// retrying will observe the same 3xx. Do not mislabel as 4xx.
		d.logger.Warn("webhook 3xx; giving up",
			slog.String("url", logURL(del.URL)),
			slog.String("delivery_id", del.DeliveryID),
			slog.Int("status", resp.StatusCode),
		)
		return permanent(fmt.Errorf("webhook: receiver returned %d", resp.StatusCode))
	}
	if resp.StatusCode >= 500 {
		d.logger.Warn("webhook 5xx; will retry",
			slog.String("url", logURL(del.URL)),
			slog.String("delivery_id", del.DeliveryID),
			slog.Int("status", resp.StatusCode),
		)
		return retryable(fmt.Errorf("webhook: receiver returned %d", resp.StatusCode))
	}
	// 4xx (and other non-2xx/3xx/5xx): receiver said "don't retry".
	d.logger.Warn("webhook 4xx; giving up",
		slog.String("url", logURL(del.URL)),
		slog.String("delivery_id", del.DeliveryID),
		slog.Int("status", resp.StatusCode),
	)
	return permanent(fmt.Errorf("webhook: receiver returned %d", resp.StatusCode))
}

// isPermanentTransportError reports Do errors that will not succeed on
// retry: blocked redirects (kit clients refuse 3xx by default), TLS
// certificate verification failures, and an open circuit breaker.
func isPermanentTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, httpx.ErrRedirectBlocked) || errors.Is(err, httpx.ErrRedirectLimitExceeded) {
		return true
	}
	if errors.Is(err, errWebhookRedirectRefused) {
		return true
	}
	if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
		return true
	}
	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) {
		return true
	}
	var he x509.HostnameError
	if errors.As(err, &he) {
		return true
	}
	var ce x509.CertificateInvalidError
	if errors.As(err, &ce) {
		return true
	}
	var sre x509.SystemRootsError
	return errors.As(err, &sre)
}

// logURL returns scheme://host only so capability tokens embedded in
// webhook paths/queries never land in application logs.
func logURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[redacted]"
	}
	return u.Scheme + "://" + u.Host
}

// retryableErr / permanentErr are the kit's local marker types so the
// retry policy can distinguish "try again" from "give up". They are
// not exported because callers should not construct them — Send maps
// HTTP/transport outcomes itself.
type retryableErr struct{ inner error }

func (e *retryableErr) Error() string { return e.inner.Error() }
func (e *retryableErr) Unwrap() error { return e.inner }

type permanentErr struct{ inner error }

func (e *permanentErr) Error() string { return e.inner.Error() }
func (e *permanentErr) Unwrap() error { return e.inner }

func retryable(err error) error { return &retryableErr{inner: err} }
func permanent(err error) error { return &permanentErr{inner: err} }

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var perm *permanentErr
	if errors.As(err, &perm) {
		return false
	}
	var retr *retryableErr
	return errors.As(err, &retr)
}
