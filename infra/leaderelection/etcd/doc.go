// Package etcd implements [leaderelection.Elector] on top of etcd's
// concurrency primitives via go.etcd.io/etcd/client/v3/concurrency.
//
// One leader per election key across every replica connected to the
// same etcd cluster: each replica creates a [concurrency.Session]
// backed by an etcd lease and competes for the election key prefix.
// The lease's auto-keepalive keeps leadership alive; loss of
// keepalive (network partition, etcd quorum loss, process stall)
// cancels the leader ctx so OnAcquired can drain before another
// replica takes over.
//
// # When to use
//
//   - Bare-metal, VM, or hybrid deployments that already run etcd
//     for service-discovery or configuration purposes. Reusing
//     etcd avoids pulling in another coordination dependency.
//   - Workloads that need strong-consistency leader election
//     without sitting on top of Kubernetes (see
//     [github.com/bds421/rho-kit/infra/leaderelection/k8slease/v2]
//     for the K8s-native equivalent that uses coordination.k8s.io
//     Lease objects).
//   - Multi-tenant clusters where leader-state visibility through
//     `etcdctl get` is operationally useful.
//
// # When NOT to use
//
//   - Services already on Kubernetes — prefer
//     [github.com/bds421/rho-kit/infra/leaderelection/k8slease/v2]
//     so the leadership state lives in the same control plane as
//     the workload.
//   - Services with only a Postgres dependency — prefer
//     [github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2]
//     which avoids running an etcd cluster purely for coordination.
//   - Services with only a Redis dependency where exact mutual
//     exclusion is not required — prefer
//     [github.com/bds421/rho-kit/infra/leaderelection/redislock/v2].
//     etcd is overkill if Redis lock-overlap windows are tolerable.
//
// # Fencing model
//
// etcd's concurrency package implements leader election via a queue
// of session-keyed entries under the election prefix; the entry with
// the smallest revision wins. The session's underlying lease has a
// TTL with a server-side renewal cadence — when the renewal misses
// the TTL window the lease is revoked and every entry holding it is
// deleted in one revision step, atomically transferring leadership
// to the next entry.
//
// As with any TTL-based lock, a stalled leader (GC pause, kernel
// freeze, network partition longer than the TTL) past the lease
// expiry opens a brief window where a second replica can become
// leader before the first notices it lost. Application-level fencing
// is required for work that must NEVER overlap; see the
// [redislock] doc for the same caveat written long-hand. The kit
// uses [concurrency.Session.Done] to cancel the leader ctx so the
// in-process callback drains as soon as the local client detects the
// loss, but this is best-effort and bounded by network round-trip.
//
// # Heavy-SDK boundary
//
// This module is the only place inside the kit that depends on
// go.etcd.io/etcd/client/v3. Consumers that do not run on etcd never
// import this package and never pull the dep transitively — the
// dependency-boundary check enforces this isolation, the same shape
// applied to k8s.io/client-go (via k8slease) and the messaging
// adapters.
package etcd
