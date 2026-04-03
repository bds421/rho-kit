package outbox_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/outbox"
)

// fakeStore is an in-memory Store implementation for unit testing.
// Safe for concurrent use.
type fakeStore struct {
	mu      sync.Mutex
	entries []outbox.Entry

	insertErr            error
	fetchPendingErr      error
	markPublishedErr     error
	markFailedErr        error
	incrementAttemptsErr error
	deletePublishedErr   error
	resetStaleErr        error
	countPendingErr      error
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
			e := s.entries[i]
			e.Status = outbox.StatusPublished
			e.PublishedAt = &publishedAt
			s.entries[i] = e
			return nil
		}
	}
	return nil
}

func (s *fakeStore) MarkFailed(_ context.Context, id string, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markFailedErr != nil {
		return s.markFailedErr
	}
	for i := range s.entries {
		if s.entries[i].ID.String() == id {
			e := s.entries[i]
			e.Status = outbox.StatusFailed
			e.LastError = &lastError
			s.entries[i] = e
			return nil
		}
	}
	return nil
}

func (s *fakeStore) IncrementAttempts(_ context.Context, id string, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.incrementAttemptsErr != nil {
		return s.incrementAttemptsErr
	}
	for i := range s.entries {
		if s.entries[i].ID.String() == id {
			e := s.entries[i]
			e.Attempts++
			e.LastError = &lastError
			e.Status = outbox.StatusPending
			s.entries[i] = e
			return nil
		}
	}
	return nil
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

func (s *fakeStore) ResetStaleProcessing(_ context.Context, staleDuration time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resetStaleErr != nil {
		return 0, s.resetStaleErr
	}

	cutoff := time.Now().UTC().Add(-staleDuration)
	var reset int64
	for i := range s.entries {
		if s.entries[i].Status == outbox.StatusProcessing && s.entries[i].CreatedAt.Before(cutoff) {
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

func TestWriter_Write(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriter(store)
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

func TestWriter_Write_EmptyTopic(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriter(store)
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
	writer := outbox.NewWriter(store)
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

func TestWriter_Write_PreservesHeaders(t *testing.T) {
	store := &fakeStore{}
	writer := outbox.NewWriter(store)
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
