// Package readreplica routes Postgres read-only workloads to one of a
// pool of replica connections while keeping writes (and reads that
// MUST see the latest write) on the primary. Replica health is tracked
// continuously: unhealthy replicas are removed from rotation and
// re-probed periodically; if all replicas are unhealthy the routing
// pool falls back to the primary with a warn-level log.
//
// # Use this package when
//
//   - Your read traffic dominates writes and you have one or more
//     physical Postgres read replicas configured.
//   - You can tolerate replication lag for SELECT workloads that don't
//     have a strict read-your-write requirement.
//
// # Do NOT use this package for
//
//   - Single-primary deployments. Use [infra/sqldb/pgx] directly.
//   - Workloads that MUST see writes the caller just made on the same
//     connection (read-your-writes). The pool exposes
//     [AcquireOption.WithReadAfterWrite] but the simpler policy is
//     "don't route those reads to a replica" — use a plain primary
//     Acquire for the whole transactional unit instead.
//   - Cross-database transactions. Postgres has no XA equivalent in
//     the kit; replicas are read-only.
//
// # Sibling packages
//
//   - [infra/sqldb/pgx]    — the kit's primary Postgres pool wrapper
//     ([pgx.Pool]). RoutingPool composes one Primary pgx.Pool plus N
//     Replicas pgx.Pool.
//   - [app/postgres]       — Builder adapter. A future
//     [postgres.WithReadReplicas] option threads a RoutingPool through
//     the kit's lifecycle.
//
// # Quick start
//
//	primary, _ := pgx.Connect(ctx, primaryCfg)
//	replicaA, _ := pgx.Connect(ctx, replicaCfgA)
//	replicaB, _ := pgx.Connect(ctx, replicaCfgB)
//
//	rp, err := readreplica.New(readreplica.Config{
//	    Primary:  primary,
//	    Replicas: []*pgx.Pool{replicaA, replicaB},
//	}, readreplica.WithLogger(logger))
//	if err != nil { return err }
//	defer rp.Close()
//
//	// Read — round-robin to a healthy replica, fall back to primary
//	// if none are healthy.
//	conn, err := rp.Acquire(ctx, readreplica.WithReadOnly())
//
//	// Write — always primary.
//	conn, err := rp.Acquire(ctx)
package readreplica
