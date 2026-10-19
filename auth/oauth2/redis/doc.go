// Package redis provides Redis-backed [oauth2.SessionStore] and
// [oauth2.StateStore] implementations for multi-replica browser OIDC flows.
//
// The adapter stores access, refresh and ID tokens server-side; callers must
// use a protected Redis deployment (TLS and authentication outside loopback)
// and must not expose its keys or backups as application logs. Enabling
// [WithEncryption] is recommended so tokens, claims and PKCE verifiers are
// stored as AES-256-GCM ciphertext (with key rotation) rather than plain
// JSON. Session and state TTLs are enforced by Redis.
//
// Besides [oauth2.SessionStore] and [oauth2.SessionUpdater], the store
// implements [oauth2.SessionIndex] (SHA-256-hashed subject and sid index
// sets written in the same MULTI as the session, stale members pruned on
// lookup), [oauth2.RefreshLocker] (SET NX PX per session with a
// token-checked release) and [oauth2.ReplayCache] (SET NX), so back-channel
// logout, subject-wide deletion and cross-replica refresh work across
// replicas sharing one Redis. The optional
// dependency is isolated here so JWT resource services do not pull a Redis
// client.
package redis
