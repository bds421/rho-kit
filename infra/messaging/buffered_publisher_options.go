package messaging

import (
	"path/filepath"
	"time"
)

// BufferedPublisherOption configures a BufferedPublisher.
type BufferedPublisherOption func(*BufferedPublisher)

// WithMaxSize sets the maximum number of buffered messages. When the
// buffer is full, Publish returns an error (back-pressure).
//
// Panics on n <= 0 — pre-fix any non-positive value silently disabled
// the cap and allowed unbounded memory growth during broker outages.
// Use [WithUnlimitedBuffer] when an unbounded buffer is genuinely intended.
func WithMaxSize(n int) BufferedPublisherOption {
	if n <= 0 {
		panic("messaging: WithMaxSize requires n > 0; use WithUnlimitedBuffer to opt out")
	}
	return func(o *BufferedPublisher) { o.maxSize = n }
}

// WithUnlimitedBuffer opts out of the per-buffer cap. Use only when an
// external mechanism (disk persistence, downstream rate limit) bounds
// memory growth — otherwise a long broker outage will OOM the service.
func WithUnlimitedBuffer() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.maxSize = -1 }
}

// WithStateDirectory sets the directory inside which the buffered
// publisher's state file must live. The directory is the only
// containment boundary for paths passed to [WithStateFile] — a
// caller-supplied relative path that resolves outside this directory
// causes [NewBufferedPublisher] to panic.
//
// The path is cleaned at option time so callers see the same
// containment regardless of trailing slashes or `./` components, but
// must be an absolute path: relative state directories pick up the
// process's working directory at construction time, which is rarely
// the operator's intent and complicates the symlink-traversal review
// in THREAT_MODEL §4.3 M-05.
//
// Panics when dir is empty or relative. Use [WithEphemeralBuffer] for
// memory-only operation; do not pass an empty string here.
func WithStateDirectory(dir string) BufferedPublisherOption {
	if dir == "" {
		panic("messaging: WithStateDirectory requires a non-empty directory")
	}
	cleaned := filepath.Clean(dir)
	if !filepath.IsAbs(cleaned) {
		panic("messaging: WithStateDirectory requires an absolute path")
	}
	return func(o *BufferedPublisher) { o.stateDir = cleaned }
}

// WithStateFile names the state file inside the directory configured
// by [WithStateDirectory]. Messages are written to this file
// atomically (write-temp + rename) so they survive process crashes.
//
// Path containment (THREAT_MODEL §4.3 M-05): the argument MUST be a
// relative path that resolves inside the configured state directory
// after [filepath.Clean]. Absolute paths and paths whose cleaned form
// escapes the directory via `..` segments are rejected at
// construction time with a panic. Calling [WithStateFile] without a
// prior [WithStateDirectory] also panics — the option pair must be
// used together so a hostile or buggy STATE_FILE env value cannot
// write outside the operator-chosen directory.
//
// The relative path may include nested components (e.g.
// `"shard-1/state.json"`), in which case the parent directories must
// already exist or be creatable by the calling process; the kit does
// not auto-create the tree to keep filesystem-side effects out of
// constructor code.
func WithStateFile(path string) BufferedPublisherOption {
	if path == "" {
		panic("messaging: WithStateFile requires a non-empty path")
	}
	return func(o *BufferedPublisher) { o.stateFileRel = path }
}

// WithMetrics sets the metrics callbacks for the buffered publisher.
func WithMetrics(m *BufferedPublisherMetrics) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.metrics = m }
}

// WithLossyMode opts in to the legacy behavior where Publish returns nil
// even when persistence to the configured state file fails. The default
// behavior is to surface the persistence error so callers can react
// before a process crash drops the buffered message. This option only
// affects publishers configured with [WithStateFile]; ephemeral
// buffers do not persist regardless.
func WithLossyMode() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.lossyMode = true }
}

// WithLossyStateRecovery opts in to "start with an empty buffer when the
// configured state file fails to load". The default fails startup so a
// corrupt or unreadable state file does not silently drop the messages
// buffering exists to preserve. Use only when the surrounding system has
// its own at-least-once guarantee (e.g. an upstream outbox), or for tests.
func WithLossyStateRecovery() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.lossyStateRecovery = true }
}

// WithLossyStateValidation opts in to skipping individual entries that
// fail per-message validation when loading persisted state. By default,
// any invalid entry is fatal at construction so corrupt entries cannot
// silently disappear — wave 66 closed a hostile-review finding that
// load() silently dropped invalid entries with only a Warn log. Use
// this option only when recovering from a known-bad state file or in
// tests; production wiring should fail loudly and force a deliberate
// recovery decision.
func WithLossyStateValidation() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.lossyStateValidation = true }
}

// WithEphemeralBuffer opts in to memory-only buffering. By default,
// [NewBufferedPublisher] panics when no state file is configured — a
// process restart would silently drop every buffered message, which is
// exactly the scenario buffering exists to prevent. Set this option only
// when the surrounding system has its own at-least-once guarantee
// (e.g. an upstream outbox), or for tests. The check is unconditional
// — there is no KIT_ENV escape hatch.
func WithEphemeralBuffer() BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.allowEphemeralBuffer = true }
}

// WithFinalDrainTimeout sets how long the buffered publisher waits to
// drain remaining messages during shutdown. Default: 15 seconds.
func WithFinalDrainTimeout(d time.Duration) BufferedPublisherOption {
	if d <= 0 {
		panic("messaging: WithFinalDrainTimeout requires a positive duration")
	}
	return func(o *BufferedPublisher) {
		o.finalDrainTimeout = d
	}
}

// WithMessageSizeLimiter replaces the buffered publisher's message-size
// policy. The check runs before direct publishing or buffering, so an
// over-large message is never persisted into the retry buffer.
func WithMessageSizeLimiter(l MessageSizeLimiter) BufferedPublisherOption {
	return func(o *BufferedPublisher) { o.sizeLimiter = l }
}

// WithMaxMessageBytes sets the default serialized message-size limit.
func WithMaxMessageBytes(maxBytes int) BufferedPublisherOption {
	return func(o *BufferedPublisher) {
		o.sizeLimiter = o.sizeLimiter.WithDefaultMaxBytes(maxBytes)
	}
}

// WithoutMaxMessageBytes disables the default size limit. Route-specific
// overrides — supplied via [WithMessageSizeLimiter] using a
// [MessageSizeLimiter] built with [NewMessageSizeLimiter](default,
// overrides...) — still apply.
func WithoutMaxMessageBytes() BufferedPublisherOption {
	return func(o *BufferedPublisher) {
		o.sizeLimiter = o.sizeLimiter.WithoutDefaultMaxBytes()
	}
}
