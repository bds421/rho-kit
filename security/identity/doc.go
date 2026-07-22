// Package identity defines the transport-agnostic canonical Principal, actor
// classification, audit formatting, and authorization seam shared by HTTP and
// gRPC auth. Use [Allow] for canonical authorization subjects and [AuditActor]
// in HTTP audit middleware extractors.
package identity
