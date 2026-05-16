package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/infra/v2/outbox"
)

// testTxKey + errAssertedTx mirror the way real callers wire a tx check
// without pulling a SQL backend into the outbox module's tests.
type testTxKey struct{}

var errAssertedTx = errors.New("tx required")

// fakeStore is an in-memory Store implementation for unit testing.
// Safe for concurrent use.
type fakeStore struct {
	mu       sync.Mutex
	entries  []outbox.Entry
	updateAt map[string]time.Time

	insertErr            error
	fetchPendingErr      error
	markPublishedErr     error
	markFailedErr        error
	incrementAttemptsErr error
	deletePublishedErr   error
	deleteFailedErr      error
	resetStaleErr        error
	countPendingErr      error
	heartbeatErr         error

	heartbeatCalls atomic.Int64
}

func (s *fakeStore) Insert(_ context.Context, entry outbox.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertErr != nil {
		return s.insertErr
	}
	s.entries = append(s.entries, entry)
	return nil
}

func (s *fakeStore) FetchPending(_ context.Context, limit int) ([]outbox.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetchPendingErr != nil {
		return nil, s.fetchPendingErr
	}

	var result []outbox.Entry
	for i := range s.entries {
		if s.entries[i].Status == outbox.StatusPending && len(result) < limit {
			s.entries[i] = withStatus(s.entries[i], outbox.StatusProcessing)
			result = append(result, s.entries[i])
		}
	}
	return result, nil
}

func (s *fakeStore) MarkPublished(_ context.Context, id string, publishedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markPublishedErr != nil {
		return s.markPublishedErr
	}
	for i := range s.entries {
		if s.entries[i].ID.String() == id {
			if s.entries[i].Status != outbox.StatusProcessing {
				return outbox.ErrStaleState
			}
			e := s.entries[i]
			e.Status = outbox.StatusPublished
			e.PublishedAt = &publishedAt
			s.entries[i] = e
			return nil
		}
	}
	return outbox.ErrNotFound
}

func (s *fakeStore) MarkFailed(_ context.Context, id string, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markFailedErr != nil {
		return s.markFailedErr
	}
	for i := range s.entries {
		if s.entries[i].ID.String() == id {
			if s.entries[i].Status != outbox.StatusProcessing {
				return outbox.ErrStaleState
			}
			e := s.entries[i]
			e.Status = outbox.StatusFailed
			e.LastError = &lastError
			s.entries[i] = e
			return nil
		}
	}
	return outbox.ErrNotFound
}

func (s *fakeStore) Heartbeat(_ context.Context, ids []string) (int64, error) {
	s.heartbeatCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heartbeatErr != nil {
		return 0, s.heartbeatErr
	}
	if s.updateAt == nil {
		s.updateAt = make(map[string]time.Time)
	}
	now := time.Now().UTC()
	var touched int64
	for _, id := range ids {
		for i := range s.entries {
			if s.entries[i].ID.String() == id && s.entries[i].Status == outbox.StatusProcessing {
				s.updateAt[id] = now
				touched++
			}
		}
	}
	return touched, nil
}

func (s *fakeStore) IncrementAttempts(_ context.Context, id string, lastError string, nextRetryAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.incrementAttemptsErr != nil {
		return s.incrementAttemptsErr
	}
	for i := range s.entries {
		if s.entries[i].ID.String() == id {
			if s.entries[i].Status != outbox.StatusProcessing {
				return outbox.ErrStaleState
			}
			e := s.entries[i]
			e.Attempts++
			e.LastError = &lastError
			e.Status = outbox.StatusPending
			retry := nextRetryAt
			e.NextRetryAt = &retry
			s.entries[i] = e
			return nil
		}
	}
	return outbox.ErrNotFound
}

func (s *fakeStore) DeletePublishedBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deletePublishedErr != nil {
		return 0, s.deletePublishedErr
	}

	var kept []outbox.Entry
	var deleted int64
	for _, e := range s.entries {
		if e.Status == outbox.StatusPublished && e.PublishedAt != nil && e.PublishedAt.Before(before) {
			deleted++
			continue
		}
		kept = append(kept, e)
	}
	s.entries = kept
	return deleted, nil
}

func (s *fakeStore) DeleteFailedBefore(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteFailedErr != nil {
		return 0, s.deleteFailedErr
	}

	var kept []outbox.Entry
	var deleted int64
	for _, e := range s.entries {
		// fakeStore stores no UpdatedAt; approximate with CreatedAt for the cutoff.
		if e.Status == outbox.StatusFailed && e.CreatedAt.Before(before) {
			deleted++
			continue
		}
		kept = append(kept, e)
	}
	s.entries = kept
	return deleted, nil
}

func (s *fakeStore) ResetStaleProcessing(_ context.Context, staleDuration time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetStaleErr != nil {
		return 0, s.resetStaleErr
	}

	cutoff := time.Now().UTC().Add(-staleDuration)
	var reset int64
	for i := range s.entries {
		if s.entries[i].Status != outbox.StatusProcessing {
			continue
		}
		// Heartbeat refreshes updateAt; if a row has heartbeated more
		// recently than the cutoff, it is NOT stale even if its CreatedAt
		// predates the cutoff (the whole point of heartbeating).
		if hb, ok := s.updateAt[s.entries[i].ID.String()]; ok && hb.After(cutoff) {
			continue
		}
		if s.entries[i].CreatedAt.Before(cutoff) {
			s.entries[i] = withStatus(s.entries[i], outbox.StatusPending)
			reset++
		}
	}
	return reset, nil
}

func (s *fakeStore) CountPending(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.countPendingErr != nil {
		return 0, s.countPendingErr
	}

	var count int64
	for _, e := range s.entries {
		if e.Status == outbox.StatusPending {
			count++
		}
	}
	return count, nil
}

func (s *fakeStore) findByID(id uuid.UUID) (outbox.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return outbox.Entry{}, false
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// withStatus returns a copy of the entry with the given status.
func withStatus(e outbox.Entry, status outbox.Status) outbox.Entry {
	e.Status = status
	return e
}

func TestNewWriter_PanicsOnNilStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	outbox.NewWriter(nil, func(context.Context) error { return nil })
}

func TestNewWriter_PanicsOnNilTxCheck(t *testing.T) {
	store := &fakeStore{}
	assert.Panics(t, func() {
		outbox.NewWriter(store, nil)
	})
}

func TestNewWriter_PanicsOnNilOption(t *testing.T) {
	store := &fakeStore{}
	check := func(context.Context) error { return nil }
	assert.Panics(t, func() {
		outbox.NewWriter(store, check, nil)
	})
}

func TestNewWriterWithoutTransactionCheck_PanicsOnNilStore(t *testing.T) {
	assert.Panics(t, func() {
		outbox.NewWriterWithoutTransactionCheck(nil)
	})
}

func TestNewWriterWithoutTransactionCheck_PanicsOnNilOption(t *testing.T) {
	store := &fakeStore{}
	assert.Panics(t, func() {
		outbox.NewWriterWithoutTransactionCheck(store, nil)
	})
}

func TestWriter_RequireTransaction_RejectsWithoutTx(t *testing.T) {
	store := &fakeStore{}
	check := func(ctx context.Context) error {
		if ctx.Value(testTxKey{}) == nil {
			return errAssertedTx
		}
		return nil
	}
	writer := outbox.NewWriter(store, check)

	err := writer.Write(context.Background(), outbox.WriteParams{
		Topic:       "t",
		RoutingKey:  "rk",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected error when ctx has no tx")
	}
	if !errors.Is(err, errAssertedTx) {
		t.Errorf("error %q does not wrap errAssertedTx", err.Error())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.entries) != 0 {
		t.Errorf("Write must not insert when tx-check fails; entries=%d", len(store.entries))
	}
}

func TestWriter_RequireTransaction_AcceptsWithTx(t *testing.T) {
	store := &fakeStore{}
	check := func(ctx context.Context) error {
		if ctx.Value(testTxKey{}) == nil {
			return errAssertedTx
		}
		return nil
	}
	writer := outbox.NewWriter(store, check)

	ctx := context.WithValue(context.Background(), testTxKey{}, "fake-tx")
	err := writer.Write(ctx, outbox.WriteParams{
		Topic:       "t",
		RoutingKey:  "rk",
		MessageID:   "msg-1",
		MessageType: "test.event",
		Payload:     json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Write with tx in ctx failed: %v", err)
	}
}

func TestWriter_Write(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
	ctx := context.Background()

	payload, err := json.Marshal(map[string]string{"key": "value"})
	require.NoError(t, err)

	params := outbox.WriteParams{
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     payload,
	}

	err = writer.Write(ctx, params)
	require.NoError(t, err)

	require.Equal(t, 1, store.count())

	store.mu.Lock()
	entry := store.entries[0]
	store.mu.Unlock()

	assert.Equal(t, "orders", entry.Topic)
	assert.Equal(t, "order.created", entry.RoutingKey)
	assert.Equal(t, "msg-1", entry.MessageID)
	assert.Equal(t, "order.created", entry.MessageType)
	assert.Equal(t, outbox.StatusPending, entry.Status)
	assert.Equal(t, 0, entry.Attempts)
	assert.Nil(t, entry.PublishedAt)
}

func TestWriter_Write_CopiesPayloadBeforeInsert(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
	payload := []byte(`{"key":"value"}`)

	err := writer.Write(context.Background(), outbox.WriteParams{
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     payload,
	})
	require.NoError(t, err)

	payload[8] = 'X'

	store.mu.Lock()
	entry := store.entries[0]
	store.mu.Unlock()
	assert.JSONEq(t, `{"key":"value"}`, string(entry.Payload))
}

func TestWriter_Write_RejectsOversizedMessageBeforeInsert(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)

	payload := json.RawMessage(`"` + strings.Repeat("x", messaging.DefaultMaxMessageBytes) + `"`)
	err := writer.Write(context.Background(), outbox.WriteParams{
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     payload,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
	assert.Equal(t, 0, store.count())
}

func TestWriter_Write_RouteMaxMessageBytesOverridesDefault(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store,
		outbox.WithMaxMessageBytes(64),
		outbox.WithRouteMaxMessageBytes("orders", "order.created", 512),
	)

	err := writer.Write(context.Background(), outbox.WriteParams{
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     json.RawMessage(`"` + strings.Repeat("x", 128) + `"`),
	})

	require.NoError(t, err)
	assert.Equal(t, 1, store.count())
}

func TestWriter_Write_EmptyTopic(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
	ctx := context.Background()

	err := writer.Write(ctx, outbox.WriteParams{
		Topic:       "",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     []byte(`{}`),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "topic must not be empty")
}

func TestWriter_Write_EmptyRoutingKey(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
	ctx := context.Background()

	err := writer.Write(ctx, outbox.WriteParams{
		Topic:       "orders",
		RoutingKey:  "",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     []byte(`{}`),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "routing key must not be empty")
}

func TestWriter_Write_RejectsUnsafeEntryFields(t *testing.T) {
	valid := outbox.WriteParams{
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     []byte(`{}`),
	}

	tests := []struct {
		name        string
		mutate      func(*outbox.WriteParams)
		want        string
		wantErrorIs error
	}{
		{
			name: "invalid topic route",
			mutate: func(p *outbox.WriteParams) {
				p.Topic = "orders prod"
			},
			want:        "invalid publish route",
			wantErrorIs: messaging.ErrInvalidRoute,
		},
		{
			name: "invalid routing key route",
			mutate: func(p *outbox.WriteParams) {
				p.RoutingKey = "order\ncreated"
			},
			want:        "invalid publish route",
			wantErrorIs: messaging.ErrInvalidRoute,
		},
		{
			name: "missing message id",
			mutate: func(p *outbox.WriteParams) {
				p.MessageID = ""
			},
			want: "message id must not be empty",
		},
		{
			name: "unsafe message id",
			mutate: func(p *outbox.WriteParams) {
				p.MessageID = "msg\n1"
			},
			want: "message id contains whitespace or control characters",
		},
		{
			name: "message id too long",
			mutate: func(p *outbox.WriteParams) {
				p.MessageID = strings.Repeat("m", messaging.MaxRouteNameBytes+1)
			},
			want: "message id exceeds maximum length",
		},
		{
			name: "missing message type",
			mutate: func(p *outbox.WriteParams) {
				p.MessageType = ""
			},
			want: "message type must not be empty",
		},
		{
			name: "empty payload",
			mutate: func(p *outbox.WriteParams) {
				p.Payload = nil
			},
			want: "payload must not be empty",
		},
		{
			name: "invalid payload",
			mutate: func(p *outbox.WriteParams) {
				p.Payload = []byte(`{"unterminated"`)
			},
			want: "payload must be valid JSON",
		},
		{
			name: "invalid headers",
			mutate: func(p *outbox.WriteParams) {
				p.Headers = map[string]string{"Bad Header": "value"}
			},
			want:        "invalid headers",
			wantErrorIs: messaging.ErrInvalidMessageHeader,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}
			writer := outbox.NewWriterWithoutTransactionCheck(store)
			params := valid
			tt.mutate(&params)

			err := writer.Write(context.Background(), params)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			if tt.wantErrorIs != nil {
				assert.ErrorIs(t, err, tt.wantErrorIs)
			}
			if tt.name == "message id too long" {
				assert.NotContains(t, err.Error(), "255")
				assert.NotContains(t, err.Error(), "256")
			}
			assert.Equal(t, 0, store.count())
		})
	}
}

func TestWriter_Write_PreservesHeaders(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriterWithoutTransactionCheck(store)
	ctx := context.Background()

	err := writer.Write(ctx, outbox.WriteParams{
		Topic:       "orders",
		RoutingKey:  "order.created",
		MessageID:   "msg-1",
		MessageType: "order.created",
		Payload:     []byte(`{}`),
		Headers:     map[string]string{"X-Correlation-Id": "abc-123"},
	})
	require.NoError(t, err)

	store.mu.Lock()
	entry := store.entries[0]
	store.mu.Unlock()

	headers, err := entry.HeadersMap()
	require.NoError(t, err)
	assert.Equal(t, "abc-123", headers["X-Correlation-Id"])
}

func TestEntry_HeadersMap(t *testing.T) {
	headers, _ := json.Marshal(map[string]string{"X-Request-Id": "req-1"})

	entry := outbox.Entry{
		Headers: headers,
	}

	got, err := entry.HeadersMap()
	require.NoError(t, err)
	assert.Equal(t, "req-1", got["X-Request-Id"])
}

func TestEntry_HeadersMap_NilHeaders(t *testing.T) {
	entry := outbox.Entry{}

	got, err := entry.HeadersMap()
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestEntry_HeadersMap_RejectsInvalidStoredHeaders(t *testing.T) {
	headers, err := json.Marshal(map[string]string{"Bad Header": "value"})
	require.NoError(t, err)

	entry := outbox.Entry{
		Headers: headers,
	}

	_, err = entry.HeadersMap()
	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrInvalidMessageHeader)
}

func TestEntry_HeadersMapErrorsDoNotReflectEntryID(t *testing.T) {
	entryID := uuid.UUID(id.NewBytes())

	entry := outbox.Entry{
		ID:      entryID,
		Headers: []byte(`{"bad"`),
	}

	_, err := entry.HeadersMap()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), entryID.String())
}
