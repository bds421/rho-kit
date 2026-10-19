// Package redis provides Redis-backed [oauth2.SessionStore] and
// [oauth2.StateStore] implementations for multi-replica browser OIDC flows.
//
// The adapter stores access, refresh and ID tokens server-side; callers must
// use a protected Redis deployment (TLS and authentication outside loopback)
// and must not expose its keys or backups as application logs. Enabling
// [WithEncryption] is recommended so tokens, claims and PKCE verifiers are
// stored as AES-256-GCM ciphertext (with key rotation) rather than plain
// JSON. Session and state TTLs are enforced by Redis. The optional
// dependency is isolated here so JWT resource services do not pull a Redis
// client.
package redis
