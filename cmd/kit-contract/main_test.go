package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bds421/rho-kit/cmd/kit-contract/v2/contract"
)

func bundle(t *testing.T, root, schema string) string {
	t.Helper()
	dir := filepath.Join(root, "contracts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"format":1,"artifacts":[{"id":"order","owner":"orders","kind":"event-jsonschema","version":"1.0.0","path":"event.json","schema_version":1,"compatibility":{"mode":"backward"}}]}`
	if err := os.WriteFile(filepath.Join(dir, contract.ManifestFilename), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "event.json"), []byte(schema), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunCompareJSON(t *testing.T) {
	old := `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`
	base := bundle(t, filepath.Join(t.TempDir(), "base"), old)
	candidate := bundle(t, filepath.Join(t.TempDir(), "candidate"), old)
	var stdout, stderr bytes.Buffer
	if got := run([]string{"compare", "-baseline", base, "-candidate", candidate, "-format", "json"}, &stdout, &stderr); got != 0 {
		t.Fatalf("exit=%d stderr=%s", got, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"compatible":true`)) {
		t.Fatalf("report = %s", stdout.String())
	}
}

func TestRunCompare_UsesSharedEventFixture(t *testing.T) {
	schema, err := os.ReadFile(filepath.Join("..", "..", "testing", "fixtures", "contracts", "order-created.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	base := bundle(t, filepath.Join(t.TempDir(), "base"), string(schema))
	candidate := bundle(t, filepath.Join(t.TempDir(), "candidate"), string(schema))
	var stdout, stderr bytes.Buffer
	if got := run([]string{"compare", "-baseline", base, "-candidate", candidate}, &stdout, &stderr); got != 0 {
		t.Fatalf("exit=%d stderr=%s", got, stderr.String())
	}
}
