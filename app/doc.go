// Package app provides the standard service bootstrap and infrastructure wiring.
//
// It exists to keep every service main() identical: load config, wire logging and
// tracing, start infra clients, expose health/metrics, and shut down gracefully.
// The Builder fails fast on misconfiguration and ensures dependencies are ready
// before the public server starts accepting traffic. Background goroutines
// registered through Infrastructure.Background are tracked and drained on exit.
package app
