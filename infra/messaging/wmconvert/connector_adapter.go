package wmconvert

import "github.com/bds421/rho-kit/infra/messaging"

// ConnectorAdapter wraps a health check function and closer to implement
// messaging.Connector for Watermill-backed providers.
type ConnectorAdapter struct {
	healthFn func() bool
	closeFn  func() error
}

// NewConnectorAdapter creates a ConnectorAdapter with the given health and close functions.
func NewConnectorAdapter(healthFn func() bool, closeFn func() error) *ConnectorAdapter {
	return &ConnectorAdapter{
		healthFn: healthFn,
		closeFn:  closeFn,
	}
}

// Healthy reports whether the underlying connection is alive.
func (c *ConnectorAdapter) Healthy() bool {
	if c.healthFn == nil {
		return true
	}
	return c.healthFn()
}

// Close shuts down the underlying connection.
func (c *ConnectorAdapter) Close() error {
	if c.closeFn == nil {
		return nil
	}
	return c.closeFn()
}

// Compile-time interface check.
var _ messaging.Connector = (*ConnectorAdapter)(nil)
