package gormstore

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb/memdb"
	"github.com/bds421/rho-kit/observability/auditlog"
)

func setupStore(t *testing.T) *GormStore {
	t.Helper()
	sub, err := fs.Sub(Migrations, "migrations")
	require.NoError(t, err)
	db := memdb.New(t, sub)
	s := New(db)
	return s
}

func TestAppendAndQuery(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	event := auditlog.Event{
		ID:        "evt-1",
		Timestamp: time.Now(),
		Actor:     "alice",
		Action:    "create",
		Resource:  "orders/1",
		Status:    "success",
	}
	require.NoError(t, s.Append(ctx, event))

	events, cursor, err := s.Query(ctx, auditlog.Filter{}, "", 10)
	require.NoError(t, err)
	assert.Len(t, events, 1)
	assert.Empty(t, cursor)
	assert.Equal(t, "evt-1", events[0].ID)
	assert.Equal(t, "alice", events[0].Actor)
}

func TestQuery_Filters(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	now := time.Now()
	events := []auditlog.Event{
		{ID: "1", Timestamp: now.Add(-2 * time.Hour), Actor: "alice", Action: "create", Resource: "orders/1", Status: "success"},
		{ID: "2", Timestamp: now.Add(-1 * time.Hour), Actor: "bob", Action: "update", Resource: "orders/1", Status: "success"},
		{ID: "3", Timestamp: now, Actor: "alice", Action: "delete", Resource: "users/1", Status: "failure"},
	}
	for _, e := range events {
		require.NoError(t, s.Append(ctx, e))
	}

	// Filter by actor.
	result, _, err := s.Query(ctx, auditlog.Filter{Actor: "alice"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// Filter by action.
	result, _, err = s.Query(ctx, auditlog.Filter{Action: "update"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, result, 1)

	// Filter by resource prefix.
	result, _, err = s.Query(ctx, auditlog.Filter{Resource: "orders"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// Filter by time range.
	result, _, err = s.Query(ctx, auditlog.Filter{Since: now.Add(-90 * time.Minute)}, "", 10)
	require.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestDeleteBefore(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()

	now := time.Now()
	events := []auditlog.Event{
		{ID: "old-1", Timestamp: now.Add(-48 * time.Hour), Actor: "a", Action: "x", Resource: "r", Status: "s"},
		{ID: "old-2", Timestamp: now.Add(-25 * time.Hour), Actor: "a", Action: "x", Resource: "r", Status: "s"},
		{ID: "new-1", Timestamp: now, Actor: "a", Action: "x", Resource: "r", Status: "s"},
	}
	for _, e := range events {
		require.NoError(t, s.Append(ctx, e))
	}

	deleted, err := s.DeleteBefore(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	remaining, _, err := s.Query(ctx, auditlog.Filter{}, "", 10)
	require.NoError(t, err)
	assert.Len(t, remaining, 1)
	assert.Equal(t, "new-1", remaining[0].ID)
}

func TestNew_PanicsOnNilDB(t *testing.T) {
	assert.Panics(t, func() { New(nil) })
}
