package interceptor

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bds421/rho-kit/resilience/v2/retry"
)

// RetryOption configures the unary-retry interceptor.
type RetryOption func(*retryConfig)

type retryConfig struct {
	policy       retry.Policy
	retryOnCodes map[codes.Code]struct{}
}

// DefaultRetryableCodes is the conservative kit default for which
// gRPC codes warrant a retry: UNAVAILABLE (transient), RESOURCE_EXHAUSTED
// (rate-limit recovery), and ABORTED (optimistic-concurrency race).
//
// Notably absent: DEADLINE_EXCEEDED (the caller asked for X seconds —
// retrying takes longer than that), INTERNAL (server-side bug, more
// likely to repeat), UNAUTHENTICATED / PERMISSION_DENIED (a retry
// without re-auth will hit the same wall).
//
// RESOURCE_EXHAUSTED is retried for rate-limit recovery, but gRPC also
// emits it for a permanent message-size violation (a request/response
// larger than the client's configured MaxSend/RecvMsgSize). Those are
// guaranteed to fail again on the same client, so [RetryUnary] skips
// them regardless of the configured code set — see
// [isPermanentResourceExhausted].
func DefaultRetryableCodes() []codes.Code {
	return []codes.Code{
		codes.Unavailable,
		codes.ResourceExhausted,
		codes.Aborted,
	}
}

// WithRetryPolicy supplies the resilience/retry policy used between
// attempts. Defaults to [retry.DefaultPolicy].
func WithRetryPolicy(p retry.Policy) RetryOption {
	return func(c *retryConfig) { c.policy = p }
}

// WithRetryableCodes overrides [DefaultRetryableCodes].
func WithRetryableCodes(cs ...codes.Code) RetryOption {
	if len(cs) == 0 {
		panic("client/interceptor: WithRetryableCodes requires at least one code")
	}
	set := make(map[codes.Code]struct{}, len(cs))
	for _, c := range cs {
		set[c] = struct{}{}
	}
	return func(c *retryConfig) { c.retryOnCodes = set }
}

// RetryUnary returns a unary client interceptor that retries the call
// according to the supplied [retry.Policy] and code allowlist.
//
// Each attempt receives the caller's ctx unchanged; the retry loop
// terminates as soon as either the policy is exhausted or ctx is
// cancelled. Streaming is NOT supported by this interceptor (stream
// retry semantics require restarting before any message has been
// sent/received and are intentionally caller-controlled).
func RetryUnary(opts ...RetryOption) grpc.UnaryClientInterceptor {
	cfg := retryConfig{policy: retry.DefaultPolicy()}
	for _, opt := range opts {
		if opt == nil {
			panic("client/interceptor: RetryUnary option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.retryOnCodes == nil {
		cfg.retryOnCodes = map[codes.Code]struct{}{}
		for _, c := range DefaultRetryableCodes() {
			cfg.retryOnCodes[c] = struct{}{}
		}
	}
	shouldRetry := func(err error) bool {
		if err == nil {
			return false
		}
		c := codeOf(err)
		if _, ok := cfg.retryOnCodes[c]; !ok {
			return false
		}
		// A ResourceExhausted caused by a message-size violation is
		// permanent on this client: the next attempt sends/receives the
		// same oversized payload against the same limit. Retrying only
		// burns the budget, so skip it even though the code is in the
		// retryable set.
		if c == codes.ResourceExhausted && isPermanentResourceExhausted(err) {
			return false
		}
		return true
	}
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		return retry.DoWith(ctx, cfg.policy, func(ctx context.Context) error {
			return invoker(ctx, method, req, reply, cc, opts...)
		}, retry.WithRetryIf(shouldRetry))
	}
}

// isPermanentResourceExhausted reports whether a ResourceExhausted error
// was produced by gRPC's own message-size enforcement. grpc-go phrases
// these as "grpc: ... larger than max ..." or "grpc: message too large
// ..." (see google.golang.org/grpc/rpc_util.go). Such errors are
// permanent for a given client + payload, unlike a server-side
// rate-limit which the same code also represents.
func isPermanentResourceExhausted(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	msg := st.Message()
	if !strings.HasPrefix(msg, "grpc: ") {
		return false
	}
	return strings.Contains(msg, "larger than max") ||
		strings.Contains(msg, "message too large")
}
