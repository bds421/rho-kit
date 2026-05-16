package messaging

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// SchemaVersion identifies the version of a message schema.
// It is a type alias for uint to allow ergonomic use without explicit conversions.
// Version 0 represents unversioned (legacy) messages.
type SchemaVersion = uint

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
	schemas map[schemaKey]schemaEntry
}

type schemaKey struct {
	msgType string
	version SchemaVersion
}

// schemaEntry holds both the raw JSON schema bytes and the compiled validator.
type schemaEntry struct {
	raw      json.RawMessage
	compiled *jsonschema.Schema
}

// NewInMemorySchemaRegistry creates a new empty InMemorySchemaRegistry.
func NewInMemorySchemaRegistry() *InMemorySchemaRegistry {
	return &InMemorySchemaRegistry{
		schemas: make(map[schemaKey]schemaEntry),
	}
}

// Register stores a schema for the given message type and version.
// Returns an error if the type/version combination is already registered,
// if the schema is nil or empty, or if the schema fails to compile as
// valid JSON Schema. The schema is compiled at registration time for
// fail-fast behavior.
func (r *InMemorySchemaRegistry) Register(msgType string, version SchemaVersion, schema json.RawMessage) error {
	if msgType == "" {
		return fmt.Errorf("message type must not be empty")
	}
	if len(schema) == 0 {
		return fmt.Errorf("schema must not be nil or empty")
	}

	// Compile the JSON schema at registration time (fail-fast).
	compiled, err := compileJSONSchema(msgType, version, schema)
	if err != nil {
		return redact.WrapError("compile schema", err)
	}

	key := schemaKey{msgType: msgType, version: version}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.schemas[key]; exists {
		return fmt.Errorf("schema already registered")
	}

	// Store a copy to prevent external mutation.
	stored := make(json.RawMessage, len(schema))
	copy(stored, schema)
	r.schemas[key] = schemaEntry{raw: stored, compiled: compiled}

	return nil
}

// Lookup retrieves the schema for the given message type and version.
// Returns an error if no schema is found.
func (r *InMemorySchemaRegistry) Lookup(msgType string, version SchemaVersion) (json.RawMessage, error) {
	key := schemaKey{msgType: msgType, version: version}

	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.schemas[key]
	if !ok {
		return nil, fmt.Errorf("no schema found")
	}

	// Return a copy to prevent external mutation.
	result := make(json.RawMessage, len(entry.raw))
	copy(result, entry.raw)
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

	slices.Sort(versions)
	return versions
}

// ValidateMessage validates the message payload against the schema registered
// for its SchemaVersion. Returns nil if no schema is registered for the version
// (backward compat: unversioned messages pass through).
func (r *InMemorySchemaRegistry) ValidateMessage(msg Message) error {
	key := schemaKey{msgType: msg.Type, version: msg.SchemaVersion}

	r.mu.RLock()
	entry, ok := r.schemas[key]
	r.mu.RUnlock()

	if !ok {
		return nil
	}

	return validatePayload(entry.compiled, msg.Payload)
}

// compileJSONSchema parses and compiles a JSON schema from raw bytes.
func compileJSONSchema(_ string, version SchemaVersion, raw json.RawMessage) (*jsonschema.Schema, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, redact.WrapError("invalid JSON", err)
	}

	resourceURL := fmt.Sprintf("schema://message/v%d", version)
	c := jsonschema.NewCompiler()
	if err := c.AddResource(resourceURL, doc); err != nil {
		return nil, redact.WrapError("add resource", err)
	}

	compiled, err := c.Compile(resourceURL)
	if err != nil {
		return nil, redact.WrapError("compile", err)
	}

	return compiled, nil
}

// validatePayload validates a JSON payload against a compiled schema.
func validatePayload(schema *jsonschema.Schema, payload json.RawMessage) error {
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return redact.WrapError("unmarshal payload for validation", err)
	}

	err := schema.Validate(doc)
	if err == nil {
		return nil
	}

	return fmt.Errorf("schema validation failed")
}
