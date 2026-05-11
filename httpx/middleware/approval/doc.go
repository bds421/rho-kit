// Package approval provides HTTP middleware that converts a destructive
// request into a pending approval ticket.
//
// # Wire shape
//
// Inbound: any request that would cause a side effect a human (or
// higher-trust system) should sign off on. The service decides which
// routes the middleware wraps; typically those are limited to specific
// destructive verbs (DELETE /v1/users/{id}, POST /v1/billing/void).
//
// On a wrapped route the middleware:
//
//  1. Extracts the tenant from the configured tenant header (or fails
//     400 Bad Request — the audit trail is unusable without it).
//
//  2. Extracts the actor via the configured extractor.
//
//  3. Reads up to MaxBodyBytes of the request body and stashes it as
//     [approval.Request.Payload].
//
//  4. Calls [approval.Store.Create] with state=pending.
//
//  5. Responds 202 Accepted with the body
//
//     {"approval_id":"<uuid>","status":"pending"}
//
// Approval and execution are separate concerns:
//
//   - The kit does NOT define an approver endpoint. Services build
//     their own (typically POST /v1/approvals/{id}/decision) that
//     calls [approval.Store.Decide] directly. That's an
//     authentication/authorisation problem the service is better
//     placed to solve than the kit.
//
//   - The kit does NOT execute approved requests. Services replay the
//     stashed [approval.Request.Payload] through their own handler in
//     the approver endpoint (or a worker that polls
//     [approval.Store.List] for [approval.StateApproved]) and call
//     [approval.Store.MarkExecuted] on success.
//
// # Required configuration
//
// [Middleware] panics at construction if no [WithActorExtractor] is
// supplied. The kit will not default actors to "anonymous" on
// destructive operations; missing or empty actor at request time
// produces 401 Unauthorized.
//
// Prefer extracting actors from authenticated request context. If a
// deployment must trust a proxy-stamped actor header, wire
// [WithActorFromHeader] so duplicate, comma-combined, whitespace, and
// control-character values are rejected consistently with the rest of
// the kit's identity-header handling.
//
// # Body size cap
//
// Bodies larger than MaxBodyBytes are rejected with 413 Payload Too
// Large. The default is 64 KiB; override via [WithMaxBodyBytes]. The
// cap exists because the body lives in the approval store (postgres
// BYTEA column or a memory map) until the request is decided — an
// uncapped middleware would happily persist a 5 GiB request body.
//
// asvs: V4.2.1, V13.4.1
package approval
