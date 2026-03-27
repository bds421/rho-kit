package messaging

// Delivery wraps a decoded Message with transport metadata.
// The consumer handles ACK/NACK — handlers just return nil or error.
type Delivery struct {
	Message       Message
	ReplyTo       string
	CorrelationID string
	Exchange      string // exchange name (AMQP) or stream name (Redis)
	RoutingKey    string // routing key (AMQP) or message type (Redis)
	SchemaVersion uint   // schema version from transport header (0 = unversioned)
	Redelivered   bool
	Headers       map[string]any
}
