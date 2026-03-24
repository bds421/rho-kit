package messaging

import (
	"log/slog"
	"time"
)

// OutboxPublisher is an alias for [BufferedPublisher].
//
// Deprecated: Use [BufferedPublisher] instead.
type OutboxPublisher = BufferedPublisher

// OutboxOption is an alias for [BufferedPublisherOption].
//
// Deprecated: Use [BufferedPublisherOption] instead.
type OutboxOption = BufferedPublisherOption

// OutboxMetrics is an alias for [BufferedPublisherMetrics].
//
// Deprecated: Use [BufferedPublisherMetrics] instead.
type OutboxMetrics = BufferedPublisherMetrics

// NewOutboxPublisher creates a [BufferedPublisher].
//
// Deprecated: Use [NewBufferedPublisher] instead.
func NewOutboxPublisher(inner MessagePublisher, conn Connector, logger *slog.Logger, opts ...BufferedPublisherOption) *BufferedPublisher {
	return NewBufferedPublisher(inner, conn, logger, opts...)
}

// WithOutboxMaxSize sets the maximum number of buffered messages.
//
// Deprecated: Use [WithBufferedMaxSize] instead.
func WithOutboxMaxSize(n int) BufferedPublisherOption {
	return WithBufferedMaxSize(n)
}

// WithOutboxStateFile enables persistent storage.
//
// Deprecated: Use [WithBufferedStateFile] instead.
func WithOutboxStateFile(path string) BufferedPublisherOption {
	return WithBufferedStateFile(path)
}

// WithOutboxMetrics sets the metrics callbacks.
//
// Deprecated: Use [WithBufferedMetrics] instead.
func WithOutboxMetrics(m *BufferedPublisherMetrics) BufferedPublisherOption {
	return WithBufferedMetrics(m)
}

// WithOutboxFinalDrainTimeout sets the final drain timeout.
//
// Deprecated: Use [WithBufferedFinalDrainTimeout] instead.
func WithOutboxFinalDrainTimeout(d time.Duration) BufferedPublisherOption {
	return WithBufferedFinalDrainTimeout(d)
}
