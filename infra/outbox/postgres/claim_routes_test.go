package postgres

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Existing callers may retain New as a typed factory, not just call it directly.
var _ func(*pgxpool.Pool) *Store = New

func TestClaimRoutesRejectEmptyAndInvalidSelection(t *testing.T) {
	assert.Panics(t, func() { WithClaimRoutes() })
	var empty []ClaimRoute
	assert.Panics(t, func() { WithClaimRoutes(empty...) })
	assert.Panics(t, func() { WithClaimRoutes(make([]ClaimRoute, maxClaimRoutes+1)...) })
	for _, route := range []ClaimRoute{
		{}, {Topic: "events"}, {RoutingKey: "web"},
		{Topic: "events\x00", RoutingKey: "web"},
		{Topic: "events", RoutingKey: "web api"},
		{Topic: "events", RoutingKey: strings.Repeat("x", 256)},
		{Topic: string([]byte{0xff}), RoutingKey: "web"},
	} {
		route.MessageType = "Created"
		assert.Panics(t, func() { WithClaimRoutes(route) })
	}
	for _, messageType := range []string{"", "bad type", "bad\x00type", string([]byte{0xff}), strings.Repeat("x", 256)} {
		assert.Panics(t, func() {
			WithClaimRoutes(ClaimRoute{Topic: "events", RoutingKey: "web", MessageType: messageType, SchemaVersion: 1})
		})
	}
	assert.NotPanics(t, func() {
		WithClaimRoutes(ClaimRoute{Topic: "events", RoutingKey: "web", MessageType: "Created", SchemaVersion: 0})
	})
	if strconv.IntSize == 64 {
		tooLarge := uint64(math.MaxInt64) + 1
		assert.Panics(t, func() {
			WithClaimRoutes(ClaimRoute{Topic: "events", RoutingKey: "web", MessageType: "Created", SchemaVersion: uint(tooLarge)})
		})
	}

	assert.Panics(t, func() { NewWithOptions(&pgxpool.Pool{}, nil) })
	assert.Panics(t, func() { NewWithOptions(nil) })
}

func TestClaimRoutesCopyInputAndPreserveExactTuples(t *testing.T) {
	routes := []ClaimRoute{{Topic: "events", RoutingKey: "web", MessageType: "Created", SchemaVersion: 1}, {Topic: "changes", RoutingKey: "audit", MessageType: "Changed", SchemaVersion: 2}}
	option := WithClaimRoutes(routes...)
	routes[0] = ClaimRoute{Topic: "deferred", RoutingKey: "other"}
	for range 2 {
		store := NewWithOptions(&pgxpool.Pool{}, option)
		require.Equal(t, []string{"events", "changes"}, store.claimTopics)
		require.Equal(t, []string{"web", "audit"}, store.claimRoutingKeys)
		require.Equal(t, []string{"Created", "Changed"}, store.claimMessageTypes)
		require.Equal(t, []int64{1, 2}, store.claimSchemaVersions)
	}
	// An unset filter preserves the old constructor's claim-all mode.
	store := New(&pgxpool.Pool{})
	assert.Nil(t, store.claimTopics)
	assert.Nil(t, store.claimRoutingKeys)
	configured := NewWithOptions(&pgxpool.Pool{})
	assert.Nil(t, configured.claimTopics)
	assert.Nil(t, configured.claimRoutingKeys)
}

func TestClaimRoutesRepeatedOptionReplacesSelection(t *testing.T) {
	store := NewWithOptions(&pgxpool.Pool{},
		WithClaimRoutes(ClaimRoute{Topic: "events", RoutingKey: "web", MessageType: "Created", SchemaVersion: 1}),
		WithClaimRoutes(ClaimRoute{Topic: "changes", RoutingKey: "audit", MessageType: "Changed", SchemaVersion: 2}),
	)
	assert.Equal(t, []string{"changes"}, store.claimTopics)
	assert.Equal(t, []string{"audit"}, store.claimRoutingKeys)
}
