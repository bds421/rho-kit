package cache

import "encoding/json"

// Codec defines how values of type T are serialized to and from bytes.
// Implementations must be safe for concurrent use.
type Codec[T any] interface {
	Marshal(v T) ([]byte, error)
	Unmarshal(data []byte, v *T) error
}

// JSONCodec is the default Codec that uses encoding/json.
type JSONCodec[T any] struct{}

// Marshal serializes v to JSON bytes.
func (JSONCodec[T]) Marshal(v T) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal deserializes JSON bytes into v.
func (JSONCodec[T]) Unmarshal(data []byte, v *T) error {
	return json.Unmarshal(data, v)
}
