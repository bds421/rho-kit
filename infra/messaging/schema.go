package messaging

import (
	"encoding/json"
	"fmt"
	"sync"
)

// SchemaVersion identifies the version of a message schema.
type SchemaVersion = int

// SchemaRegistry stores and retrieves JSON schemas for message types and versions.
// Implementations must be safe for concurrent use.
type SchemaRegistry interface {
	// Register stores a schema for the given message type and version.
	// Returns an error if the type/version combination is already registered.
	Register(msgType string, version SchemaVersion, schema json.RawMessage) error

	// Lookup retrieves the schema for the given message type and version.
	// Returns an error if no schema is found.
	Lookup(msgType string, version SchemaVersion) (json.RawMessage, error)

	// Versions returns all registered versions for the given message type,
	// sorted in ascending order. Returns nil if the message type is unknown.
	Versions(msgType string) []SchemaVersion
}

// InMemorySchemaRegistry is a thread-safe, in-memory implementation of SchemaRegistry.
// Suitable for testing and single-process applications.
type InMemorySchemaRegistry struct {
	mu      sync.RWMutex
	schemas map[schemaKey]json.RawMessage
}

type schemaKey struct {
	msgType string
	version SchemaVersion
}

// NewInMemorySchemaRegistry creates a new empty InMemorySchemaRegistry.
func NewInMemorySchemaRegistry() *InMemorySchemaRegistry {
	return &InMemorySchemaRegistry{
		schemas: make(map[schemaKey]json.RawMessage),
	}
}

// Register stores a schema for the given message type and version.
// Returns an error if the type/version combination is already registered.
func (r *InMemorySchemaRegistry) Register(msgType string, version SchemaVersion, schema json.RawMessage) error {
	if msgType == "" {
		return fmt.Errorf("message type must not be empty")
	}
	if schema == nil {
		return fmt.Errorf("schema must not be nil")
	}

	key := schemaKey{msgType: msgType, version: version}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.schemas[key]; exists {
		return fmt.Errorf("schema already registered for %s v%d", msgType, version)
	}

	// Store a copy to prevent external mutation.
	stored := make(json.RawMessage, len(schema))
	copy(stored, schema)
	r.schemas[key] = stored

	return nil
}

// Lookup retrieves the schema for the given message type and version.
// Returns an error if no schema is found.
func (r *InMemorySchemaRegistry) Lookup(msgType string, version SchemaVersion) (json.RawMessage, error) {
	key := schemaKey{msgType: msgType, version: version}

	r.mu.RLock()
	defer r.mu.RUnlock()

	schema, ok := r.schemas[key]
	if !ok {
		return nil, fmt.Errorf("no schema found for %s v%d", msgType, version)
	}

	// Return a copy to prevent external mutation.
	result := make(json.RawMessage, len(schema))
	copy(result, schema)
	return result, nil
}

// Versions returns all registered versions for the given message type,
// sorted in ascending order. Returns nil if the message type is unknown.
func (r *InMemorySchemaRegistry) Versions(msgType string) []SchemaVersion {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var versions []SchemaVersion
	for key := range r.schemas {
		if key.msgType == msgType {
			versions = append(versions, key.version)
		}
	}

	sortVersions(versions)
	return versions
}

// sortVersions sorts a slice of SchemaVersion in ascending order.
func sortVersions(versions []SchemaVersion) {
	// Simple insertion sort — version lists are typically tiny.
	for i := 1; i < len(versions); i++ {
		v := versions[i]
		j := i - 1
		for j >= 0 && versions[j] > v {
			versions[j+1] = versions[j]
			j--
		}
		versions[j+1] = v
	}
}
