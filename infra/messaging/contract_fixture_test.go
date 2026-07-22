package messaging_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// TestContractFixture_RuntimeRegistryPinsPublishedSchema proves the exact
// event-schema fixture checked by kit-contract is accepted and retained by the
// runtime registry. This keeps CI artifact checks and consumer validation on
// the same document rather than similar hand-maintained copies.
func TestContractFixture_RuntimeRegistryPinsPublishedSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testing", "fixtures", "contracts", "order-created.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := messaging.NewInMemorySchemaRegistry()
	if err := registry.Register("orders.created", 1, raw); err != nil {
		t.Fatal(err)
	}
	stored, err := registry.Lookup("orders.created", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, stored) {
		t.Fatal("runtime schema differs from published contract fixture")
	}
	if err := registry.ValidateMessage(messaging.Message{
		Type:          "orders.created",
		SchemaVersion: 1,
		Payload:       []byte(`{"id":"order-1","total":42.5}`),
	}); err != nil {
		t.Fatalf("fixture-valid event rejected by runtime registry: %v", err)
	}
}
