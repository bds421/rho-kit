// Package lifecycle provides graceful startup and shutdown helpers.
//
// Run starts the HTTP server, listens for SIGINT/SIGTERM, invokes the shutdown
// hook, waits for background goroutines, and then drains in-flight requests
// before exiting.
package lifecycle
