// Package riverqueue implements [data/queue.Publisher] and a
// kit-friendly Consumer around riverqueue/river — a Postgres-backed
// durable job queue. v2 made this the default for "must not lose
// this job" workloads; the Redis queue (data/queue/redisqueue) is
// now positioned as the lightweight option.
//
// Why River:
//   - No new infrastructure: uses your existing Postgres.
//   - Atomic enqueue + business write: the Publish call accepts a
//     pgx.Tx, so the job appears iff the transaction commits.
//   - Replay + introspection: River's web UI shows queued, running,
//     and failed jobs against the same database operators already
//     have access to.
//   - Backpressure: River's worker pool is goroutine-bounded, so a
//     burst doesn't fork unbounded handlers.
//
// asvs: V11.1.2
package riverqueue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	kitqueue "github.com/bds421/rho-kit/data/v2/queue"
)

// Publisher enqueues kit [kitqueue.Message]s as River jobs. The
// implementation is a thin wrapper that maps the kit's
// type+payload+id triple onto a single River job kind ("rho.envelope")
// so River's own type system stays minimal.
//
// For richer routing — different worker pools per message type, retry
// policies per kind — register types directly with River and bypass
// this adapter. The adapter is meant for the common case where a
// service uses the kit's [kitqueue.Publisher] surface and a Postgres
// is already available.
type Publisher struct {
	client          *river.Client[pgx.Tx]
	uniqueByID      bool
	maxPayloadBytes int
}

// NewPublisher builds a Publisher backed by an already-running River
// client. Callers construct the client (workers, queues, retry
// policies all configured) and hand it to the kit so the kit doesn't
// take ownership of the worker lifecycle.
//
// The kit's data/queue.Publisher signature only enqueues; consume is
// done by registering River workers directly against the client.
//
// Default: dedupe by (queue, args) for messages whose Message.ID is
// non-empty (audit FR-059). Pass [WithoutUniqueByID] for "every
// Enqueue executes" semantics.
func NewPublisher(client *river.Client[pgx.Tx], opts ...PublisherOption) *Publisher {
	if client == nil {
		panic("riverqueue: NewPublisher client must not be nil")
	}
	p := &Publisher{
		client:          client,
		uniqueByID:      true,
		maxPayloadBytes: kitqueue.DefaultMaxPayloadBytes,
	}
	for _, o := range opts {
		if o == nil {
			panic("riverqueue: NewPublisher publisher option must not be nil")
		}
		o(p)
	}
	return p
}

func (p *Publisher) ready() error {
	if p == nil || p.client == nil {
		return kitqueue.ErrInvalidQueue
	}
	return nil
}

// Enqueue implements [kitqueue.Publisher]. The queue argument maps
// to River's queue field (River uses string queue names natively).
// Empty or invalid queue names are rejected before River can silently
// default to its "default" queue.
//
// FR-059 [MED]: by default, River is configured to dedupe by (queue,
// kind, args) for jobs that share a Message.ID - a second Enqueue with
// the same ID is a no-op rather than a duplicate execution. Callers who
// want classic "every Enqueue executes" semantics can opt out via
// [WithoutUniqueByID].
func (p *Publisher) Enqueue(ctx context.Context, queue string, msg kitqueue.Message) error {
	if err := p.ready(); err != nil {
		return err
	}
	if err := kitqueue.ValidateName(queue, "queue"); err != nil {
		return fmt.Errorf("riverqueue: %w", err)
	}
	if err := kitqueue.ValidateMessage(msg, p.maxPayloadBytes); err != nil {
		return fmt.Errorf("riverqueue: %w", err)
	}
	job := envelopeArgs{
		ID:      msg.ID,
		Type:    msg.Type,
		Payload: clonePayload(msg.Payload),
	}
	opts := &river.InsertOpts{Queue: queue}
	if p.uniqueByID && msg.ID != "" {
		opts.UniqueOpts = river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
		}
	}
	if _, err := p.client.Insert(ctx, job, opts); err != nil {
		return fmt.Errorf("riverqueue: insert: %w", err)
	}
	return nil
}

// WithoutUniqueByID opts out of the FR-059 deduplication-by-args
// behaviour. Use only when the caller's Message.ID is *not* an
// idempotency token and re-enqueues should always run.
func WithoutUniqueByID() PublisherOption {
	return func(p *Publisher) { p.uniqueByID = false }
}

// WithMaxMessageBytes sets the maximum kit message payload size accepted by
// Enqueue. The default is [kitqueue.DefaultMaxPayloadBytes].
func WithMaxMessageBytes(maxBytes int) PublisherOption {
	if maxBytes <= 0 {
		panic("riverqueue: WithMaxMessageBytes requires maxBytes > 0")
	}
	return func(p *Publisher) { p.maxPayloadBytes = maxBytes }
}

// WithoutMaxPayloadBytes disables the publisher-level payload cap. Use only
// when an outer product contract or River queue policy already applies a
// stricter bound.
func WithoutMaxPayloadBytes() PublisherOption {
	return func(p *Publisher) { p.maxPayloadBytes = 0 }
}

// PublisherOption configures a [Publisher] at construction time.
type PublisherOption func(*Publisher)

// envelopeArgs is the River job payload for kit-mediated messages.
// River requires args to implement Kind() and JSON-serialise.
type envelopeArgs struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Kind is the River job kind for kit-mediated envelopes.
func (envelopeArgs) Kind() string { return "rho.envelope" }

// EnvelopeWorker is the River worker for kit envelope jobs. Register
// it on a [river.Workers] before constructing the client so the
// adapter can publish to it.
type EnvelopeWorker struct {
	river.WorkerDefaults[envelopeArgs]
	handler         kitqueue.Handler
	maxPayloadBytes int
}

// NewEnvelopeWorker returns a worker that hands every dequeued job
// to the supplied [kitqueue.Handler].
func NewEnvelopeWorker(handler kitqueue.Handler, opts ...EnvelopeWorkerOption) *EnvelopeWorker {
	if handler == nil {
		panic("riverqueue: NewEnvelopeWorker handler must not be nil")
	}
	w := &EnvelopeWorker{
		handler:         handler,
		maxPayloadBytes: kitqueue.DefaultMaxPayloadBytes,
	}
	for _, o := range opts {
		if o == nil {
			panic("riverqueue: NewEnvelopeWorker envelope worker option must not be nil")
		}
		o(w)
	}
	return w
}

// WithWorkerMaxPayloadBytes sets the maximum kit message payload size accepted
// by [EnvelopeWorker]. The default is [kitqueue.DefaultMaxPayloadBytes].
func WithWorkerMaxPayloadBytes(maxBytes int) EnvelopeWorkerOption {
	if maxBytes <= 0 {
		panic("riverqueue: WithWorkerMaxPayloadBytes requires maxBytes > 0")
	}
	return func(w *EnvelopeWorker) { w.maxPayloadBytes = maxBytes }
}

// WithoutWorkerMaxPayloadBytes disables the worker-level payload cap. Use only
// when River queue policy or an outer product contract applies a stricter
// bound before work dispatch.
func WithoutWorkerMaxPayloadBytes() EnvelopeWorkerOption {
	return func(w *EnvelopeWorker) { w.maxPayloadBytes = 0 }
}

// EnvelopeWorkerOption configures an [EnvelopeWorker] at construction time.
type EnvelopeWorkerOption func(*EnvelopeWorker)

// Work implements [river.Worker].
func (w *EnvelopeWorker) Work(ctx context.Context, job *river.Job[envelopeArgs]) error {
	if w == nil || w.handler == nil || job == nil {
		return kitqueue.ErrInvalidQueue
	}
	msg := kitqueue.Message{
		ID:      job.Args.ID,
		Type:    job.Args.Type,
		Payload: clonePayload(job.Args.Payload),
	}
	if err := kitqueue.ValidateMessage(msg, w.maxPayloadBytes); err != nil {
		return fmt.Errorf("riverqueue: invalid envelope: %w", err)
	}
	return w.handler(ctx, msg)
}

func clonePayload(payload []byte) []byte {
	if payload == nil {
		return nil
	}
	return append(payload[:0:0], payload...)
}

// JobState mirrors [rivertype.JobState] for callers that want to
// introspect kit-published jobs without taking a direct rivertype
// import.
type JobState = rivertype.JobState

// DriverFromPool wraps a pgxpool.Pool in River's pgxv5 driver. The
// kit exposes this so bridge-module wiring stays a one-liner — see the
// app/postgres + app/queue bridges for the canonical setup.
func DriverFromPool(pool *pgxpool.Pool) *riverpgxv5.Driver {
	return riverpgxv5.New(pool)
}
