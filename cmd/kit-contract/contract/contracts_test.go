package contract_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bds421/rho-kit/cmd/kit-contract/v2/contract"
)

func writeBundle(t *testing.T, root, manifest, document string) string {
	t.Helper()
	dir := filepath.Join(root, "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, contract.ManifestFilename), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "event.schema.json"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const manifest = `{"format":1,"artifacts":[{"id":"orders.created","owner":"orders","kind":"event-jsonschema","version":"1.0.0","path":"event.schema.json","schema_version":1,"compatibility":{"mode":"backward"}}]}`
const oldEvent = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"id":{"type":"string"},"total":{"type":"number"}},"required":["id","total"]}`

func TestCompareDirs_RejectsRemovedEventProperty(t *testing.T) {
	base := writeBundle(t, filepath.Join(t.TempDir(), "base"), manifest, oldEvent)
	candidate := writeBundle(t, filepath.Join(t.TempDir(), "candidate"), manifest, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
	report, err := contract.CompareDirs(candidate, base)
	if err != nil {
		t.Fatal(err)
	}
	if report.Compatible {
		t.Fatal("removed event property must be incompatible")
	}
}

func TestCompareDirs_AllowsAdditiveEventProperty(t *testing.T) {
	base := writeBundle(t, filepath.Join(t.TempDir(), "base"), manifest, oldEvent)
	candidate := writeBundle(t, filepath.Join(t.TempDir(), "candidate"), manifest, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"id":{"type":"string"},"total":{"type":"number"},"currency":{"type":"string"}},"required":["id","total"]}`)
	report, err := contract.CompareDirs(candidate, base)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Compatible {
		t.Fatalf("additive event property must be compatible: %#v", report.Findings)
	}
}

func TestCompareDirs_RejectsRequiredOutputBecomingOptional(t *testing.T) {
	base := writeBundle(t, filepath.Join(t.TempDir(), "base"), manifest, oldEvent)
	candidate := writeBundle(t, filepath.Join(t.TempDir(), "candidate"), manifest, `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"id":{"type":"string"},"total":{"type":"number"}},"required":["id"]}`)
	report, err := contract.CompareDirs(candidate, base)
	if err != nil {
		t.Fatal(err)
	}
	if report.Compatible {
		t.Fatal("making a required output optional must be incompatible")
	}
}

func TestValidateDir_RejectsUnsupportedSchemaKeyword(t *testing.T) {
	dir := writeBundle(t, t.TempDir(), manifest, `{"$schema":"https://json-schema.org/draft/2020-12/schema","oneOf":[]}`)
	if err := contract.ValidateDir(dir); err == nil {
		t.Fatal("unsupported schema keyword must fail closed")
	}
}

func writeOpenAPIBundle(t *testing.T, root, document string) string {
	t.Helper()
	dir := filepath.Join(root, "bundle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"format":1,"artifacts":[{"id":"orders.http","owner":"orders","kind":"openapi","version":"1.0.0","path":"openapi.json","compatibility":{"mode":"backward"}}]}`
	if err := os.WriteFile(filepath.Join(dir, contract.ManifestFilename), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.json"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCompareDirs_RejectsRemovedHTTPResponseFromFixture(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "testing", "fixtures", "contracts", "orders.openapi.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	base := writeOpenAPIBundle(t, filepath.Join(t.TempDir(), "base"), string(fixture))
	candidate := writeOpenAPIBundle(t, filepath.Join(t.TempDir(), "candidate"), `{"openapi":"3.1.0","info":{"title":"orders","version":"1.1.0"},"paths":{"/orders":{"post":{"requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}}},"responses":{}}}}}`)
	report, err := contract.CompareDirs(candidate, base)
	if err != nil {
		t.Fatal(err)
	}
	if report.Compatible {
		t.Fatal("removed HTTP response must be incompatible")
	}
}
