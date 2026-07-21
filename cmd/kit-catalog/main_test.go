package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failWriter returns errFailWriter once okBytes have been written,
// simulating a broken pipe / short write mid-stream.
type failWriter struct {
	written int
	okBytes int
}

var errFailWriter = errors.New("write failed")

func (f *failWriter) Write(p []byte) (int, error) {
	if f.written >= f.okBytes {
		return 0, errFailWriter
	}
	f.written += len(p)
	return len(p), nil
}

func sampleManifest() manifest {
	return manifest{
		ScannedAt:    "2026-05-16T00:00:00Z",
		ServiceCount: 1,
		Services: []service{{
			Module:      "github.com/example/svc",
			Path:        "/tmp/svc",
			KitPackages: []string{"github.com/bds421/rho-kit/httpx/v2"},
			KitVersions: map[string]string{"github.com/bds421/rho-kit/httpx/v2": "v2.0.3"},
		}},
	}
}

func TestParseGoMod_ExtractsModuleAndKitVersions(t *testing.T) {
	content := `module github.com/example/orders-api

go 1.26.2

require (
	github.com/bds421/rho-kit/httpx/v2 v2.0.3
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/some/other v1.5.0
)

require github.com/bds421/rho-kit/observability/v2 v2.0.1 // indirect
`
	mod, versions := parseGoMod(content)
	assert.Equal(t, "github.com/example/orders-api", mod)
	assert.Equal(t, map[string]string{
		"github.com/bds421/rho-kit/httpx/v2":         "v2.0.3",
		"github.com/bds421/rho-kit/data/v2":          "v2.0.0",
		"github.com/bds421/rho-kit/observability/v2": "v2.0.1",
	}, versions)
}

func TestCollectKitImports_PicksUpProductionFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`
package main

import (
	"context"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/data/v2/idempotency/pgstore"
)

var _ = httpx.RequestID
`), 0o600))

	imports, err := collectKitImports(dir)
	require.NoError(t, err)

	assert.Contains(t, imports, "github.com/bds421/rho-kit/httpx/v2")
	assert.Contains(t, imports, "github.com/bds421/rho-kit/data/v2/idempotency/pgstore")
}

func TestCollectKitImports_SkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(`
package main

import "github.com/bds421/rho-kit/testing/kittest/v2"

var _ = kittest.Setup
`), 0o600))

	imports, err := collectKitImports(dir)
	require.NoError(t, err)
	assert.Empty(t, imports, "test files must not contribute to the production composition manifest")
}

func TestCollectKitImports_SkipsVendorAndHidden(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "vendor", "x"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "vendor", "x", "v.go"), []byte(`
package x
import "github.com/bds421/rho-kit/should-not-appear"
var _ = struct{}{}
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".cache", "c.go"), []byte(`
package c
import "github.com/bds421/rho-kit/also-should-not-appear"
var _ = struct{}{}
`), 0o600))

	imports, err := collectKitImports(dir)
	require.NoError(t, err)
	assert.Empty(t, imports, "vendor + hidden dirs must be skipped")
}

func TestModuleForImport_PicksLongestPrefix(t *testing.T) {
	versions := map[string]string{
		"github.com/bds421/rho-kit/data/v2":                     "v2.0.0",
		"github.com/bds421/rho-kit/data/idempotency/pgstore/v2": "v2.0.1",
	}
	mod := moduleForImport(
		"github.com/bds421/rho-kit/data/idempotency/pgstore/v2",
		versions,
	)
	assert.Equal(t, "github.com/bds421/rho-kit/data/idempotency/pgstore/v2", mod,
		"longest-prefix match wins so the right module pin is attributed")
}

func TestScanService_BuildsManifestForOneService(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(`module github.com/example/svc

go 1.26.2

require (
	github.com/bds421/rho-kit/httpx/v2 v2.0.3
)
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`
package main

import "github.com/bds421/rho-kit/httpx/v2"

func main() { _ = httpx.RequestID }
`), 0o600))

	svc, err := scanService(dir)
	require.NoError(t, err)
	require.NotNil(t, svc)

	assert.Equal(t, "github.com/example/svc", svc.Module)
	assert.Equal(t, []string{"github.com/bds421/rho-kit/httpx/v2"}, svc.KitPackages)
	assert.Equal(t, "v2.0.3", svc.KitVersions["github.com/bds421/rho-kit/httpx/v2"])
}

func TestScanService_ReturnsNilWhenNoGoMod(t *testing.T) {
	dir := t.TempDir()
	svc, err := scanService(dir)
	require.NoError(t, err)
	assert.Nil(t, svc, "no go.mod is not fatal; caller decides")
}

func TestFilterByImport_KeepsOnlyMatching(t *testing.T) {
	services := []service{
		{Module: "a", KitPackages: []string{"github.com/bds421/rho-kit/httpx/v2"}},
		{Module: "b", KitPackages: []string{"github.com/bds421/rho-kit/data/v2"}},
		{Module: "c", KitPackages: []string{"github.com/bds421/rho-kit/httpx/v2", "github.com/bds421/rho-kit/data/v2"}},
	}
	got := filterByImport(services, "github.com/bds421/rho-kit/httpx/v2")
	assert.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Module)
	assert.Equal(t, "c", got[1].Module)
}

func TestScanFleet_FindsMultipleServices(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"svc-a", "svc-b"} {
		dir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(
			"module github.com/example/"+name+"\n\ngo 1.26.2\n",
		), 0o600))
	}
	// Add a non-service directory (no go.mod).
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))

	services, err := scanFleet(root)
	require.NoError(t, err)
	require.Len(t, services, 2)
	assert.Equal(t, "github.com/example/svc-a", services[0].Module)
	assert.Equal(t, "github.com/example/svc-b", services[1].Module)
}

// TestManifest_JSONRoundTrip pins the on-the-wire shape so
// fleet-operator tooling that consumes the output can rely on it.
func TestManifest_JSONRoundTrip(t *testing.T) {
	m := manifest{
		ScannedAt:    "2026-05-16T00:00:00Z",
		ServiceCount: 1,
		Services: []service{{
			Module:      "github.com/example/svc",
			Path:        "/tmp/svc",
			KitPackages: []string{"github.com/bds421/rho-kit/httpx/v2"},
			KitVersions: map[string]string{"github.com/bds421/rho-kit/httpx/v2": "v2.0.3"},
		}},
	}
	buf, err := json.Marshal(m)
	require.NoError(t, err)

	var back manifest
	require.NoError(t, json.Unmarshal(buf, &back))
	assert.Equal(t, m, back)
}

func TestEmitJSON_WritesValidManifest(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, emitJSON(&buf, sampleManifest()))

	var back manifest
	require.NoError(t, json.Unmarshal(buf.Bytes(), &back))
	assert.Equal(t, sampleManifest(), back)
}

// TestEmitJSON_ReportsWriteError pins the contract that a failed
// write surfaces an error (so main exits non-zero) rather than being
// silently swallowed while the process exits 0.
func TestEmitJSON_ReportsWriteError(t *testing.T) {
	err := emitJSON(&failWriter{okBytes: 0}, sampleManifest())
	require.Error(t, err)
	assert.ErrorIs(t, err, errFailWriter)
}

func TestEmitTable_WritesRows(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, emitTable(&buf, sampleManifest()))

	out := buf.String()
	assert.Contains(t, out, "github.com/example/svc")
	assert.Contains(t, out, "github.com/bds421/rho-kit/httpx/v2")
	assert.Contains(t, out, "v2.0.3")
}

func TestEmitTable_ReportsWriteError(t *testing.T) {
	err := emitTable(&failWriter{okBytes: 0}, sampleManifest())
	require.Error(t, err)
	assert.ErrorIs(t, err, errFailWriter)
}

func TestEmitCSV_WritesRows(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, emitCSV(&buf, sampleManifest()))

	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2) // header + one row
	assert.Equal(t, []string{"service_module", "service_path", "kit_package", "kit_module", "kit_version"}, records[0])
	assert.Equal(t, []string{
		"github.com/example/svc",
		"/tmp/svc",
		"github.com/bds421/rho-kit/httpx/v2",
		"github.com/bds421/rho-kit/httpx/v2",
		"v2.0.3",
	}, records[1])
}

// TestEmitCSV_ReportsFlushError pins that the csv writer's buffered
// flush error (w.Error()) is surfaced, not swallowed. The header write
// succeeds into the bufio buffer, then the flush fails on the writer.
func TestEmitCSV_ReportsFlushError(t *testing.T) {
	err := emitCSV(&failWriter{okBytes: 0}, sampleManifest())
	require.Error(t, err)
	assert.ErrorIs(t, err, errFailWriter)
}

func TestCollectKitImports_IgnoresStringLiterals(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/s\n\ngo 1.22\n"), 0o644))
	src := `package main
const doc = "github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest"
func main() {}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644))
	imports, err := collectKitImports(dir)
	require.NoError(t, err)
	require.Empty(t, imports, "string literal must not count as an import")
}

func TestParseGoMod_IgnoresExcludeAndRetract(t *testing.T) {
	content := `module example.com/s

require github.com/bds421/rho-kit/httpx/v2 v2.0.3

exclude (
	github.com/bds421/rho-kit/httpx/v2 v2.0.1
)

retract (
	github.com/bds421/rho-kit/data/v2 v2.0.0
)
`
	_, versions := parseGoMod(content)
	require.Equal(t, map[string]string{
		"github.com/bds421/rho-kit/httpx/v2": "v2.0.3",
	}, versions)
}
