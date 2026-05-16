package etcd

import (
	"context"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
)

// session abstracts the subset of [*concurrency.Session] used by
// [Elector]. Defined as an interface so the elector's leader-loop
// can be exercised in unit tests without standing up a real etcd
// cluster.
//
// The interface is unexported because callers should never construct
// their own implementation — the production wiring is bound by
// [defaultSessionFactory] and tests inject fakes through the
// internal [Elector.sessionFactory] hook.
type session interface {
	Done() <-chan struct{}
	Close() error
}

// election abstracts the subset of [*concurrency.Election] used by
// [Elector]. Same testability rationale as [session].
type election interface {
	Campaign(ctx context.Context, val string) error
	Resign(ctx context.Context) error
}

// sessionFactory constructs the (session, election) pair tied to the
// caller-supplied election key prefix and lease TTL. Production code
// uses [defaultSessionFactory]; tests inject a fake.
type sessionFactory func(ctx context.Context, leaseTTLSeconds int, electionKey string) (session, election, error)

// defaultSessionFactory binds [sessionFactory] to the real
// [concurrency] package. The factory is curried over the etcd client
// so the elector does not need to hold the client reference past
// construction.
func defaultSessionFactory(client *clientv3.Client) sessionFactory {
	return func(ctx context.Context, ttl int, key string) (session, election, error) {
		s, err := concurrency.NewSession(client,
			concurrency.WithTTL(ttl),
			concurrency.WithContext(ctx),
		)
		if err != nil {
			return nil, nil, err
		}
		e := concurrency.NewElection(s, key)
		return sessionAdapter{s}, electionAdapter{e}, nil
	}
}

// sessionAdapter wraps [*concurrency.Session] so the production type
// satisfies the unexported [session] interface. Done is forwarded
// without modification; Close releases the lease without revoking it
// (etcd's TTL handles eventual cleanup).
type sessionAdapter struct{ s *concurrency.Session }

func (a sessionAdapter) Done() <-chan struct{} { return a.s.Done() }
func (a sessionAdapter) Close() error          { return a.s.Close() }

// electionAdapter wraps [*concurrency.Election] so the production
// type satisfies the unexported [election] interface.
type electionAdapter struct{ e *concurrency.Election }

func (a electionAdapter) Campaign(ctx context.Context, val string) error { return a.e.Campaign(ctx, val) }
func (a electionAdapter) Resign(ctx context.Context) error               { return a.e.Resign(ctx) }
