// Package redis provides Redis-backed [oauth2.SessionStore] and
// [oauth2.StateStore] implementations for multi-replica browser OIDC flows.
//
// The adapter stores access and refresh tokens server-side; callers must use a
// protected Redis deployment (TLS and authentication outside loopback) and
// must not expose its keys or backups as application logs. Session and state
// TTLs are enforced by Redis. The optional dependency is isolated here so JWT
// resource services do not pull a Redis client.
package redis
