package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		assert.Panics(t, func() { WithClaimRoutes(route) })
	}
	assert.Panics(t, func() { New(&pgxpool.Pool{}, nil) })
}

func TestClaimRoutesCopyInputAndPreserveExactPairs(t *testing.T) {
	routes := []ClaimRoute{{Topic: "events", RoutingKey: "web"}, {Topic: "changes", RoutingKey: "audit"}}
	option := WithClaimRoutes(routes...)
	routes[0] = ClaimRoute{Topic: "deferred", RoutingKey: "other"}
	for range 2 {
		store := New(&pgxpool.Pool{}, option)
		require.Equal(t, []string{"events", "changes"}, store.claimTopics)
		require.Equal(t, []string{"web", "audit"}, store.claimRoutingKeys)
	}
	// An unset filter preserves the old constructor's claim-all mode.
	store := New(&pgxpool.Pool{})
	assert.Nil(t, store.claimTopics)
	assert.Nil(t, store.claimRoutingKeys)
}

func TestClaimRoutesRepeatedOptionReplacesSelection(t *testing.T) {
	store := New(&pgxpool.Pool{},
		WithClaimRoutes(ClaimRoute{Topic: "events", RoutingKey: "web"}),
		WithClaimRoutes(ClaimRoute{Topic: "changes", RoutingKey: "audit"}),
	)
	assert.Equal(t, []string{"changes"}, store.claimTopics)
	assert.Equal(t, []string{"audit"}, store.claimRoutingKeys)
}
