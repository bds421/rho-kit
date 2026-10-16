package postgres

import "github.com/bds421/rho-kit/infra/v2/messaging"

const maxClaimRoutes = 256

// ClaimRoute selects one exact persisted topic and routing-key pair. It has no
// wildcard semantics and does not grant authority to publish or consume a route.
type ClaimRoute struct {
	Topic      string
	RoutingKey string
}

// Option configures a Store before it is used. Options must not be applied to a
// live Store; constructor configuration is immutable and safe for concurrent use.
type Option func(*Store)

// WithClaimRoutes limits FetchPending to the supplied exact route pairs. The
// selection happens before the batch limit and row locking, leaving other
// pending rows unchanged, including their attempts and claim tokens.
//
// Omitting this option preserves claim-all behavior. An explicitly empty list,
// more than 256 pairs or an invalid route panics instead of enabling claim-all.
// Both fields are required and follow maintained messaging route validation.
// The input is
// copied when this option is created; reusing an option is safe. Repeated options
// replace the selection, matching other kit constructor options.
//
// This is claim selection only: Insert, CountPending, retention, stale recovery
// and outcome fencing retain their existing table-wide/ownership semantics.
func WithClaimRoutes(routes ...ClaimRoute) Option {
	if len(routes) == 0 || len(routes) > maxClaimRoutes {
		panic("outbox/postgres: WithClaimRoutes requires 1 to 256 routes")
	}
	topics := make([]string, len(routes))
	keys := make([]string, len(routes))
	for i, route := range routes {
		if route.RoutingKey == "" || messaging.ValidatePublishRoute(route.Topic, route.RoutingKey) != nil {
			panic("outbox/postgres: WithClaimRoutes requires valid nonempty route pairs")
		}
		topics[i], keys[i] = route.Topic, route.RoutingKey
	}
	return func(s *Store) {
		s.claimTopics, s.claimRoutingKeys = topics, keys
	}
}
