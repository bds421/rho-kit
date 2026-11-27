package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// WithNotify makes Insert send a PostgreSQL notification on channel with
// the entry, in the same transaction: it reaches the listeners when the
// entry commits and never for one rolled back, so a relay in another
// process ([Listen]) learns of it without waiting for its poll.
// PostgreSQL folds the notifications of one transaction into one. An empty
// or invalid channel name panics.
func WithNotify(channel string) Option {
	if err := validateSchema(channel); err != nil {
		panic("outbox/postgres: WithNotify channel: " + err.Error())
	}
	return func(s *Store) { s.notifyChannel = channel }
}

// insert runs the INSERT, and the notification of WithNotify, in the
// caller's transaction, or in one of their own.
func (s *Store) insert(ctx context.Context, query string, args []any) error {
	exec := func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return redact.WrapError("outbox/postgres: insert", err)
		}
		if s.notifyChannel == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, "SELECT pg_notify($1, '')", s.notifyChannel); err != nil {
			return redact.WrapError("outbox/postgres: notify", err)
		}
		return nil
	}
	if tx, ok := TxFromContext(ctx); ok {
		return exec(tx)
	}
	if s.notifyChannel == "" {
		if _, err := s.pool.Exec(ctx, query, args...); err != nil {
			return redact.WrapError("outbox/postgres: insert", err)
		}
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, exec)
}

// ListenOption configures [Listen].
type ListenOption func(*listenConfig)

type listenConfig struct{ retry time.Duration }

// WithListenRetry sets how long Listen waits before it connects again after
// its connection failed (default 1 s); the relay polls meanwhile.
func WithListenRetry(d time.Duration) ListenOption {
	if d <= 0 {
		panic("outbox/postgres: WithListenRetry requires a positive duration")
	}
	return func(c *listenConfig) { c.retry = d }
}

// Listen holds a connection of its own from pool that LISTENs on channel
// and calls wake for every notification, typically [outbox.Relay.Wake], so
// a relay learns at once of entries another process commits through a
// Store with [WithNotify]. It calls wake also after each (re)connect, for
// what was committed while it was not listening. When the connection
// fails it connects again after the retry wait; the relay's poll covers
// the gap. It blocks until ctx ends and then returns nil, so it fits
// app.Infrastructure.Background. An empty or invalid channel name fails.
func Listen(ctx context.Context, pool *pgxpool.Pool, channel string, wake func(), opts ...ListenOption) error {
	if pool == nil || wake == nil {
		return errors.New("outbox/postgres: Listen requires a pool and a wake function")
	}
	if err := validateSchema(channel); err != nil {
		return fmt.Errorf("outbox/postgres: Listen channel: %w", err)
	}
	cfg := listenConfig{retry: time.Second}
	for _, opt := range opts {
		opt(&cfg)
	}
	for {
		_ = listenOnce(ctx, pool, channel, wake) // a failure is retried; the poll covers the gap
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.retry):
		}
	}
}

// listenOnce listens until its connection fails or ctx ends. The
// connection leaves the pool afterwards: it still listens.
func listenOnce(ctx context.Context, pool *pgxpool.Pool, channel string, wake func()) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	raw := conn.Hijack()
	defer func() { _ = raw.Close(context.WithoutCancel(ctx)) }()
	if _, err := raw.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return err
	}
	wake()
	for {
		if _, err := raw.WaitForNotification(ctx); err != nil {
			return err
		}
		wake()
	}
}
