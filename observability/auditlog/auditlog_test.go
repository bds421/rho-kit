package auditlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogger_Log_AutoPopulates(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	l.Log(context.Background(), Event{
		Actor:    "user-1",
		Action:   "create",
		Resource: "orders/123",
		Status:   "success",
	})

	events := store.Events()
	require.Len(t, events, 1)
	assert.NotEmpty(t, events[0].ID, "ID should be auto-generated")
	assert.False(t, events[0].Timestamp.IsZero(), "Timestamp should be auto-set")
	assert.Equal(t, "user-1", events[0].Actor)
	assert.Equal(t, "create", events[0].Action)
	assert.Equal(t, "orders/123", events[0].Resource)
	assert.Equal(t, "success", events[0].Status)
}

func TestLogger_LogAction(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	l.LogAction(context.Background(), "admin", "delete", "users/456", "success")

	events := store.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "admin", events[0].Actor)
	assert.Equal(t, "delete", events[0].Action)
	assert.Equal(t, "users/456", events[0].Resource)
}

func TestLogger_Log_PreservesExplicitFields(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	now := time.Now()
	l.Log(context.Background(), Event{
		ID:        "custom-id",
		Timestamp: now,
		Actor:     "svc",
		Action:    "sync",
		Resource:  "data",
		Status:    "success",
		TraceID:   "0123456789abcdef0123456789abcdef",
	})

	events := store.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "custom-id", events[0].ID)
	assert.Equal(t, now, events[0].Timestamp)
	assert.Equal(t, "0123456789abcdef0123456789abcdef", events[0].TraceID)
}

func TestQuery_FilterByActor(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	l.LogAction(context.Background(), "alice", "create", "r/1", "success")
	l.LogAction(context.Background(), "bob", "create", "r/2", "success")
	l.LogAction(context.Background(), "alice", "update", "r/1", "success")

	events, _, err := l.Query(context.Background(), Filter{Actor: "alice"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestQuery_FilterByAction(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	l.LogAction(context.Background(), "a", "create", "r/1", "success")
	l.LogAction(context.Background(), "a", "delete", "r/2", "success")

	events, _, err := l.Query(context.Background(), Filter{Action: "delete"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Equal(t, "delete", events[0].Action)
}

func TestQuery_FilterByResource(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	l.LogAction(context.Background(), "a", "x", "orders/1", "success")
	l.LogAction(context.Background(), "a", "x", "orders/2", "success")
	l.LogAction(context.Background(), "a", "x", "users/1", "success")

	events, _, err := l.Query(context.Background(), Filter{Resource: "orders"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

func TestQuery_FilterByTime(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	past := time.Now().Add(-2 * time.Hour)
	recent := time.Now()

	l.Log(context.Background(), Event{Actor: "a", Action: "x", Resource: "r", Status: "success", Timestamp: past})
	l.Log(context.Background(), Event{Actor: "a", Action: "x", Resource: "r", Status: "success", Timestamp: recent})

	events, _, err := l.Query(context.Background(), Filter{Since: time.Now().Add(-1 * time.Hour)}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestQuery_Pagination(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	for i := range 5 {
		l.LogAction(context.Background(), "a", "x", "r/"+string(rune('a'+i)), "success")
	}

	// Page 1: 2 events.
	page1, cursor1, err := l.Query(context.Background(), Filter{}, "", 2)
	require.NoError(t, err)
	assert.Len(t, page1, 2)
	assert.NotEmpty(t, cursor1)

	// Page 2: 2 events.
	page2, cursor2, err := l.Query(context.Background(), Filter{}, cursor1, 2)
	require.NoError(t, err)
	assert.Len(t, page2, 2)
	assert.NotEmpty(t, cursor2)

	// Page 3: 1 event (last page).
	page3, cursor3, err := l.Query(context.Background(), Filter{}, cursor2, 2)
	require.NoError(t, err)
	assert.Len(t, page3, 1)
	assert.Empty(t, cursor3)
}

func TestMemoryStore_Reset(t *testing.T) {
	store := NewMemoryStore()
	l := New(store)

	l.LogAction(context.Background(), "a", "x", "r", "success")
	assert.Len(t, store.Events(), 1)

	store.Reset()
	assert.Empty(t, store.Events())
}

func TestNew_PanicsOnNilStore(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	store := NewMemoryStore()
	assert.Panics(t, func() { New(store, nil) })
}

func TestValidateEventRejectsInvalidFields(t *testing.T) {
	valid := Event{
		ID:        "1",
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    "login",
		Resource:  "sessions",
		Status:    "success",
		IPAddress: "10.0.0.1",
		TraceID:   "0123456789abcdef0123456789abcdef",
		Metadata:  json.RawMessage(`{"ok":true}`),
	}
	require.NoError(t, ValidateEvent(valid))

	for name, mutate := range map[string]func(*Event){
		"zero timestamp": func(e *Event) { e.Timestamp = time.Time{} },
		"empty id":       func(e *Event) { e.ID = "" },
		"id too long":    func(e *Event) { e.ID = strings.Repeat("i", MaxEventIDBytes+1) },
		"actor newline":  func(e *Event) { e.Actor = "alice\nbob" },
		"empty action":   func(e *Event) { e.Action = "" },
		"resource space": func(e *Event) { e.Resource = "orders 1" },
		"bad status":     func(e *Event) { e.Status = "ok" },
		"bad ip":         func(e *Event) { e.IPAddress = "not an ip" },
		"bad trace":      func(e *Event) { e.TraceID = strings.Repeat("A", MaxTraceIDBytes) },
		"bad metadata":   func(e *Event) { e.Metadata = json.RawMessage(`{"broken"`) },
		"large metadata": func(e *Event) { e.Metadata = json.RawMessage(`"` + strings.Repeat("x", MaxMetadataBytes) + `"`) },
	} {
		t.Run(name, func(t *testing.T) {
			event := valid
			mutate(&event)
			err := ValidateEvent(event)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidEvent)
			switch name {
			case "id too long":
				assert.NotContains(t, err.Error(), "36")
				assert.NotContains(t, err.Error(), "37")
			case "large metadata":
				assert.NotContains(t, err.Error(), "65536")
				assert.NotContains(t, err.Error(), "65537")
			}
		})
	}
}

func TestLogger_LogERejectsInvalidEventBeforeStore(t *testing.T) {
	store := &captureStore{}
	l := New(store)

	err := l.LogE(context.Background(), Event{
		Actor:    "alice smith",
		Action:   "login",
		Resource: "sessions",
		Status:   "success",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidEvent)
	assert.Empty(t, store.event)
	assert.Equal(t, uint64(1), l.DroppedCount())
}

type failingStore struct {
	err error
}

func (s failingStore) Append(context.Context, Event) error {
	return s.err
}

func (s failingStore) Query(context.Context, Filter, string, int) ([]Event, string, error) {
	return nil, "", s.err
}

func TestLogger_LogEReturnsIDGenerationError(t *testing.T) {
	idErr := errors.New("rng unavailable")
	prev := newAuditID
	newAuditID = func() (uuid.UUID, error) { return uuid.Nil, idErr }
	t.Cleanup(func() { newAuditID = prev })

	l := New(NewMemoryStore())
	err := l.LogE(context.Background(), Event{
		Actor:    "a",
		Action:   "x",
		Resource: "r",
		Status:   "failure",
	})

	require.ErrorIs(t, err, idErr)
	assert.Equal(t, uint64(1), l.DroppedCount())
}

type captureStore struct {
	event Event
}

func (s *captureStore) Append(_ context.Context, event Event) error {
	s.event = event
	return nil
}

func (s *captureStore) Query(context.Context, Filter, string, int) ([]Event, string, error) {
	return []Event{s.event}, "", nil
}

func TestLogger_LogEClonesMetadataBeforeStore(t *testing.T) {
	store := &captureStore{}
	l := New(store)
	metadata := json.RawMessage(`{"token":"original"}`)

	require.NoError(t, l.LogE(context.Background(), Event{
		Actor:    "alice",
		Action:   "login",
		Resource: "sessions",
		Status:   "success",
		Metadata: metadata,
	}))
	metadata[10] = 'X'

	assert.JSONEq(t, `{"token":"original"}`, string(store.event.Metadata))
}

func TestLogger_QueryClonesMetadataFromStore(t *testing.T) {
	store := &captureStore{
		event: Event{
			ID:       "1",
			Metadata: json.RawMessage(`{"token":"original"}`),
		},
	}
	l := New(store)

	events, _, err := l.Query(context.Background(), Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	events[0].Metadata[10] = 'X'

	again, _, err := l.Query(context.Background(), Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.JSONEq(t, `{"token":"original"}`, string(again[0].Metadata))
}

func TestLogger_LogE_OnDropPanicDoesNotPanic(t *testing.T) {
	storeErr := errors.New("store unavailable")
	l := New(failingStore{err: storeErr}, WithOnDrop(func(context.Context, Event, error) {
		panic("drop hook exploded")
	}))

	var err error
	assert.NotPanics(t, func() {
		err = l.LogE(context.Background(), Event{Actor: "a", Action: "x", Resource: "r", Status: "failure"})
	})
	require.ErrorIs(t, err, storeErr)
	assert.Equal(t, uint64(1), l.DroppedCount())
}

func TestLogger_LogEAppendFailureLogRedactsEventAndError(t *testing.T) {
	storeErr := errors.New("append failed token=tenant-secret")
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	l := New(failingStore{err: storeErr}, WithLogger(logger))

	err := l.LogE(context.Background(), Event{
		ID:       "event-secret",
		Actor:    "alice",
		Action:   "download-token",
		Resource: "sessions",
		Status:   "failure",
	})

	require.ErrorIs(t, err, storeErr)
	got := logs.String()
	assert.Contains(t, got, "auditlog: failed to append event")
	assert.Contains(t, got, "<redacted error")
	assert.NotContains(t, got, "tenant-secret")
	assert.NotContains(t, got, "event-secret")
	assert.NotContains(t, got, "download-token")
}

func TestWithLogger_NilNormalizesToDefault(t *testing.T) {
	store := NewMemoryStore()
	l := New(store, WithLogger(nil))
	require.NotNil(t, l.logger)
}

func TestMemoryStore_QueryFiltersByIPAddress(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	require.NoError(t, store.Append(ctx, Event{
		ID: "1", Actor: "alice", Action: "login", Resource: "sessions", Status: "success",
		IPAddress: "10.0.0.1", Timestamp: time.Now(),
	}))
	require.NoError(t, store.Append(ctx, Event{
		ID: "2", Actor: "alice", Action: "login", Resource: "sessions", Status: "success",
		IPAddress: "10.0.0.2", Timestamp: time.Now(),
	}))

	got, _, err := store.Query(ctx, Filter{IPAddress: "10.0.0.1"}, "", 100)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "1", got[0].ID)
}

func TestMemoryStore_CopiesMetadataOnPublicBoundaries(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	metadata := json.RawMessage(`{"token":"original"}`)

	require.NoError(t, store.Append(ctx, Event{
		ID:        "1",
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    "login",
		Resource:  "sessions",
		Status:    "success",
		Metadata:  metadata,
	}))
	metadata[10] = 'X'

	events := store.Events()
	require.Len(t, events, 1)
	assert.JSONEq(t, `{"token":"original"}`, string(events[0].Metadata))

	events[0].Metadata[10] = 'X'
	events = store.Events()
	require.Len(t, events, 1)
	assert.JSONEq(t, `{"token":"original"}`, string(events[0].Metadata))

	queried, _, err := store.Query(ctx, Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, queried, 1)
	queried[0].Metadata[10] = 'X'

	queried, _, err = store.Query(ctx, Filter{}, "", 10)
	require.NoError(t, err)
	require.Len(t, queried, 1)
	assert.JSONEq(t, `{"token":"original"}`, string(queried[0].Metadata))
}

func TestMemoryStore_AppendRejectsInvalidEvent(t *testing.T) {
	store := NewMemoryStore()

	err := store.Append(context.Background(), Event{
		ID:        "1",
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    "login",
		Resource:  "sessions",
		Status:    "not-a-status",
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidEvent)
	assert.Empty(t, store.Events())
}

func TestRetentionJobPanicsOnInvalidConfig(t *testing.T) {
	assert.Panics(t, func() {
		RetentionJob(nil, time.Hour, nil)
	})
	assert.Panics(t, func() {
		RetentionJob(&retentionCaptureStore{}, 0, nil)
	})
	assert.Panics(t, func() {
		RetentionJob(&retentionCaptureStore{}, -time.Hour, nil)
	})
}

func TestRetentionJobDeletesBeforeCutoff(t *testing.T) {
	store := &retentionCaptureStore{}
	retention := time.Hour
	job := RetentionJob(store, retention, nil)

	beforeRun := time.Now().Add(-retention)
	require.NoError(t, job(context.Background()))
	afterRun := time.Now().Add(-retention)

	assert.Equal(t, 1, store.deleteCalls)
	assert.False(t, store.before.Before(beforeRun.Add(-time.Second)))
	assert.False(t, store.before.After(afterRun.Add(time.Second)))
}

func TestRetentionJobLogRedactsCleanupError(t *testing.T) {
	storeErr := errors.New("delete failed token=tenant-secret")
	store := &retentionCaptureStore{deleteErr: storeErr}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	job := RetentionJob(store, time.Hour, logger)

	err := job(context.Background())

	require.ErrorIs(t, err, storeErr)
	got := logs.String()
	assert.Contains(t, got, "audit retention cleanup failed")
	assert.Contains(t, got, "<redacted error")
	assert.NotContains(t, got, "tenant-secret")
}

type retentionCaptureStore struct {
	before      time.Time
	deleteCalls int
	deleteErr   error
}

func (s *retentionCaptureStore) Append(context.Context, Event) error {
	return nil
}

func (s *retentionCaptureStore) Query(context.Context, Filter, string, int) ([]Event, string, error) {
	return nil, "", nil
}

func (s *retentionCaptureStore) DeleteBefore(_ context.Context, before time.Time) (int64, error) {
	s.before = before
	s.deleteCalls++
	if s.deleteErr != nil {
		return 0, s.deleteErr
	}
	return 0, nil
}
