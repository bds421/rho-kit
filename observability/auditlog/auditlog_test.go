package auditlog

import (
	"context"
	"testing"
	"time"

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
		TraceID:   "abc123",
	})

	events := store.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "custom-id", events[0].ID)
	assert.Equal(t, now, events[0].Timestamp)
	assert.Equal(t, "abc123", events[0].TraceID)
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

	l.Log(context.Background(), Event{Actor: "a", Action: "x", Resource: "r", Status: "s", Timestamp: past})
	l.Log(context.Background(), Event{Actor: "a", Action: "x", Resource: "r", Status: "s", Timestamp: recent})

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

	l.LogAction(context.Background(), "a", "x", "r", "s")
	assert.Len(t, store.Events(), 1)

	store.Reset()
	assert.Empty(t, store.Events())
}

func TestNew_PanicsOnNilStore(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}
