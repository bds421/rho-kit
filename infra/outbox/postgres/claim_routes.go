package postgres

import (
	"fmt"
	"math"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

const maxClaimRoutes = 256

// ClaimRoute selects one exact persisted route and message contract. All fields
// match literally: SchemaVersion zero selects legacy entries, not all versions.
// Selection does not grant authority to publish or consume a route.
type ClaimRoute struct {
	Topic         string
	RoutingKey    string
	MessageType   string
	SchemaVersion uint
}

// Option configures a Store through NewWithOptions before it is used. Options
// must not be applied to a live Store; constructor configuration is immutable
// and safe for concurrent use.
type Option func(*Store)

// WithClaimRoutes limits relay operations to the supplied exact route tuples. The
// selection happens before the batch limit and row locking, leaving other
// pending rows unchanged, including their attempts and claim tokens.
//
// Omitting this option preserves claim-all behavior. An explicitly empty list,
// more than 256 tuples or an invalid route panics instead of enabling claim-all.
// String fields are required and follow maintained messaging route validation.
// Schema versions must fit the persisted nonnegative BIGINT column.
// The input is copied when this option is created; reusing an option is safe.
// Repeated options replace the selection, matching other kit constructor options.
//
// FetchPending, CountPending, retention and stale recovery share the selection.
// Insert remains unfiltered; outcome updates still require an owned claim token.
func WithClaimRoutes(routes ...ClaimRoute) Option {
	if len(routes) == 0 || len(routes) > maxClaimRoutes {
		panic("outbox/postgres: WithClaimRoutes requires 1 to 256 routes")
	}
	topics := make([]string, len(routes))
	keys := make([]string, len(routes))
	types := make([]string, len(routes))
	versions := make([]int64, len(routes))
	for i, route := range routes {
		if route.RoutingKey == "" || route.MessageType == "" ||
			messaging.ValidatePublishRoute(route.Topic, route.RoutingKey) != nil ||
			messaging.ValidatePublishRoute(route.MessageType, route.RoutingKey) != nil ||
			uint64(route.SchemaVersion) > math.MaxInt64 {
			panic("outbox/postgres: WithClaimRoutes requires valid exact route tuples")
		}
		topics[i], keys[i] = route.Topic, route.RoutingKey
		types[i], versions[i] = route.MessageType, int64(route.SchemaVersion)
	}
	return func(s *Store) {
		s.claimTopics, s.claimRoutingKeys = topics, keys
		s.claimMessageTypes, s.claimSchemaVersions = types, versions
	}
}

// routeSelection appends copied constructor values to a statement's existing
// arguments. Only placeholder numbers are interpolated; route data is always
// bound. Zipped arrays prevent independent allowlists from admitting cross-tuples.
func (s *Store) routeSelection(args []any) (string, []any) {
	if len(s.claimTopics) == 0 {
		return "", args
	}
	first := len(args) + 1
	predicate := fmt.Sprintf(`
  AND EXISTS (
      SELECT 1 FROM unnest($%d::text[], $%d::text[], $%d::text[], $%d::bigint[])
          AS route(topic, routing_key, message_type, schema_version)
      WHERE route.topic = outbox_entries.topic
        AND route.routing_key = outbox_entries.routing_key
        AND route.message_type = outbox_entries.message_type
        AND route.schema_version = outbox_entries.schema_version
  )`, first, first+1, first+2, first+3)
	return predicate, append(args, s.claimTopics, s.claimRoutingKeys, s.claimMessageTypes, s.claimSchemaVersions)
}
