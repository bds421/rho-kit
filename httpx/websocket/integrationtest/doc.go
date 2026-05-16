// Package integrationtest exercises httpx/websocket against a real
// net.Listener so the production module does not need a test-only
// dependency on the WebSocket dial path. Run with the `integration`
// build tag:
//
//	go test -tags integration ./...
package integrationtest
