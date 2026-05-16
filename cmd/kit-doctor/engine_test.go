package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/cmd/kit-doctor/v2/rules"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestParseSeverity_DoesNotReflectRejectedValue(t *testing.T) {
	_, err := parseSeverity("secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-strict must be critical|high|warning|info")
	assert.NotContains(t, err.Error(), "secret")
}

func TestScan_FlagsJWTWithoutClaims(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/jwt/v2"

func wire() {
	_ = jwt.Module("https://issuer/.well-known/jwks.json")
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)

	if !hasRule(findings, "jwt-missing-claims") {
		t.Fatalf("expected jwt-missing-claims finding, got %+v", findings)
	}
}

func TestScan_DotRootScansCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/jwt/v2"

func wire() {
	_ = jwt.Module("https://issuer/.well-known/jwks.json")
}
`)
	t.Chdir(dir)

	findings, err := scan(".", rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "jwt-missing-claims"),
		`scan(".") must inspect the current directory, got %+v`, findings)
}

func TestScan_SkipsSymlinkedGoFiles(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "outside.go")
	require.NoError(t, os.WriteFile(target, []byte(`package svc

import "github.com/bds421/rho-kit/app/jwt/v2"

func wire() {
	_ = jwt.Module("https://issuer/.well-known/jwks.json")
}
`), 0o600))
	if err := os.Symlink(target, filepath.Join(dir, "linked.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "symlinked-go-file"),
		"symlinked Go files should be reported as skipped, got %+v", findings)
	assert.False(t, hasRule(findings, "jwt-missing-claims"),
		"scan must not parse code outside root through a symlink, got %+v", findings)
}

func TestScan_FlagsOnlyBuilderMissingClaims(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/jwt/v2"

func wire() {
	_ = jwt.Module("https://issuer/.well-known/jwks.json",
		jwt.WithIssuer("https://issuer.example.com"),
		jwt.WithAudience("svc-a"),
	)

	_ = jwt.Module("https://issuer/.well-known/jwks.json")
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)

	var jwtFindings []rules.Finding
	for _, f := range findings {
		if f.Rule == "jwt-missing-claims" {
			jwtFindings = append(jwtFindings, f)
		}
	}
	require.Len(t, jwtFindings, 2,
		"expected exactly two findings (issuer + audience) for the unconfigured builder, got %+v", findings)
	for _, f := range jwtFindings {
		assert.Equal(t, 11, f.Line,
			"finding must point at the second jwt.Module call, not the configured one")
	}
}

func TestScan_AcceptsJWTWithIssuerAndAudience(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/jwt/v2"

func wire() {
	_ = jwt.Module("https://issuer/.well-known/jwks.json",
		jwt.WithIssuer("https://issuer.example.com"),
		jwt.WithAudience("svc"),
	)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "jwt-missing-claims"),
		"chained issuer + audience should suppress finding, got %+v", findings)
}

func TestScan_FlagsIdempotencyMissingUserExtractor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mw.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/middleware/idempotency"

func wire() {
	idempotency.Middleware(store)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "idempotency-user-extractor"))
}

func TestScan_AcceptsIdempotencyWithUserExtractorOption(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mw.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/middleware/idempotency"

func wire() {
	idempotency.Middleware(store, idempotency.WithUserExtractor(fn))
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "idempotency-user-extractor"),
		"variadic WithUserExtractor option must suppress finding, got %+v", findings)
}

func TestScan_AcceptsIdempotencyWithAllowSharedKeysOption(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mw.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/middleware/idempotency"

func wire() {
	idempotency.Middleware(store, idempotency.WithAllowSharedKeys())
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "idempotency-user-extractor"),
		"variadic WithAllowSharedKeys must suppress finding, got %+v", findings)
}

func TestScan_FlagsIdempotencyMissingUserExtractor_AliasedImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mw.go", `package svc

import idem "github.com/bds421/rho-kit/httpx/v2/middleware/idempotency"

func wire() {
	idem.Middleware(store)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "idempotency-user-extractor"),
		"aliased idempotency import must still trigger rule, got %+v", findings)
}

func TestScan_DoesNotFlagLocalVarNamedIdempotency(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mw.go", `package svc

func wire() {
	idempotency := struct {
		Middleware func(any)
	}{}
	idempotency.Middleware(store)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "idempotency-user-extractor"),
		"local variable named idempotency must not trigger package rule, got %+v", findings)
}

func TestScan_IdempotencyUserExtractorSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "mw_test.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/middleware/idempotency"

func wire() {
	idempotency.Middleware(store)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "idempotency-user-extractor"),
		"_test.go must not be flagged for idempotency-user-extractor, got %+v", findings)
}

// TestScan_FlagsIdempotencyMemoryStoreInProduction pins THREAT_MODEL
// §4.9 I-05 / AGENTS.md anti-pattern: a production call to
// idempotency.NewMemoryStore must be flagged as critical so a
// multi-instance deployment with non-shared idempotency state never
// ships.
func TestScan_FlagsIdempotencyMemoryStoreInProduction(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/data/v2/idempotency"

func wire() {
	_ = idempotency.NewMemoryStore()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "idempotency-memory-store"),
		"production NewMemoryStore call must be flagged, got %+v", findings)
	for _, f := range findings {
		if f.Rule == "idempotency-memory-store" {
			assert.Equal(t, rules.Critical, f.Severity,
				"idempotency-memory-store must be critical severity")
		}
	}
}

// TestScan_FlagsIdempotencyMemoryStore_AliasedImport verifies the
// rule resolves through an import alias so smuggling it past as
// `idem.NewMemoryStore` still fails the check.
func TestScan_FlagsIdempotencyMemoryStore_AliasedImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import idem "github.com/bds421/rho-kit/data/v2/idempotency"

func wire() {
	_ = idem.NewMemoryStore()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "idempotency-memory-store"),
		"aliased import must still trigger rule, got %+v", findings)
}

// TestScan_IdempotencyMemoryStoreSkipsTestFiles confirms _test.go
// is treated as the test surface AGENTS.md sanctions for memory
// stores. The rule must stay silent there.
func TestScan_IdempotencyMemoryStoreSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire_test.go", `package svc

import "github.com/bds421/rho-kit/data/v2/idempotency"

func wire() {
	_ = idempotency.NewMemoryStore()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "idempotency-memory-store"),
		"_test.go usage must not be flagged, got %+v", findings)
}

// TestScan_IdempotencyMemoryStoreRespectsInlineSuppression confirms
// the standard `kit-doctor:allow` marker silences the rule for
// internal tooling that deliberately wires a memory store.
func TestScan_IdempotencyMemoryStoreRespectsInlineSuppression(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/data/v2/idempotency"

func wire() {
	_ = idempotency.NewMemoryStore() // kit-doctor:allow idempotency-memory-store reason="single-instance CLI"
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "idempotency-memory-store"),
		"inline suppression must skip finding, got %+v", findings)
}

// TestScan_IdempotencyMemoryStoreDoesNotFlagOtherMemoryStores
// verifies the rule is package-scoped: an unrelated NewMemoryStore
// (e.g. an audit-log backend) must not be flagged.
func TestScan_IdempotencyMemoryStoreDoesNotFlagOtherMemoryStores(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/observability/v2/auditlog"

func wire() {
	_ = auditlog.NewMemoryStore()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "idempotency-memory-store"),
		"unrelated NewMemoryStore must not be flagged, got %+v", findings)
}

// TestScan_IdempotencyMemoryStoreIgnoresLocalShadow verifies the
// rule never fires on a local variable named `idempotency` — the
// rule must be tied to the actual import path, not a substring match.
func TestScan_IdempotencyMemoryStoreIgnoresLocalShadow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

func wire() {
	idempotency := struct{ NewMemoryStore func() any }{}
	_ = idempotency.NewMemoryStore()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "idempotency-memory-store"),
		"shadowed local variable must not trigger rule, got %+v", findings)
}

func TestScan_FlagsTenantKeyPrefixFormatString(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keys.go", `package svc

import "fmt"

func key(tenantID, raw string) string {
	return fmt.Sprintf("tenant:%s:%s", tenantID, raw)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "tenant-key-prefix"))
}

func TestScan_FlagsTenantKeyPrefixConcatenation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keys.go", `package svc

func key(tenantID, raw string) string {
	return "tenant:" + tenantID + ":" + raw
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "tenant-key-prefix"))
}

func TestScan_AcceptsTenantKeyHelper(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keys.go", `package svc

import (
	"context"

	coretenant "github.com/bds421/rho-kit/core/v2/tenant"
)

func key(ctx context.Context, raw string) (string, error) {
	return coretenant.Key(ctx, raw)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "tenant-key-prefix"),
		"coretenant.Key should not be flagged, got %+v", findings)
}

func TestScan_TenantKeyPrefixIgnoresErrorMessages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "errors.go", `package svc

import "errors"

var errTenant = errors.New("tenant: invalid tenant ID")
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "tenant-key-prefix"),
		"human-readable tenant error should not be flagged, got %+v", findings)
}

func TestScan_TenantKeyPrefixSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keys_test.go", `package svc

func key(tenantID, raw string) string {
	return "tenant:" + tenantID + ":" + raw
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "tenant-key-prefix"),
		"_test.go must not be flagged for tenant-key-prefix, got %+v", findings)
}

func TestScan_ExemptsTenantKeyPrefixInCoreTenantPackage(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "core", "tenant"), 0o700))
	writeFile(t, dir, "core/go.mod", "module github.com/bds421/rho-kit/core/v2\n\ngo 1.26.2\n")
	writeFile(t, dir, "core/tenant/key.go", `package tenant

const keyPrefix = "tenant:"
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "tenant-key-prefix"),
		"core tenant encoder package must be exempt, got %+v", findings)
}

func TestScan_FlagsDefaultHTTPClient(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `package svc

import "net/http"

func wire() {
	c := http.DefaultClient
	_ = c
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "default-http-client"))
}

func TestScan_FlagsDefaultHTTPClient_AliasedImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `package svc

import nethttp "net/http"

func wire() {
	c := nethttp.DefaultClient
	_ = c
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "default-http-client"),
		"aliased net/http import must still trigger rule, got %+v", findings)
}

func TestScan_FlagsHTTPClientCompositeLiteral(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `package svc

import (
	"net/http"
	"time"
)

func wire() {
	c := &http.Client{Timeout: 2 * time.Second}
	_ = c
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "default-http-client"),
		"direct http.Client construction must trigger rule, got %+v", findings)
}

func TestScan_FlagsHTTPClientNew(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `package svc

import "net/http"

func wire() {
	c := new(http.Client)
	_ = c
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "default-http-client"),
		"new(http.Client) must trigger rule, got %+v", findings)
}

func TestScan_FlagsHTTPClientZeroValueVar(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `package svc

import "net/http"

var client http.Client
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "default-http-client"),
		"zero-value http.Client declarations must trigger rule, got %+v", findings)
}

func TestScan_FlagsHTTPClientPackageHelpers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `package svc

import (
	"io"
	nethttp "net/http"
	"strings"
)

func wire() error {
	if _, err := nethttp.Get("https://example.com"); err != nil {
		return err
	}
	if _, err := nethttp.Head("https://example.com"); err != nil {
		return err
	}
	if _, err := nethttp.Post("https://example.com", "text/plain", strings.NewReader("x")); err != nil {
		return err
	}
	resp, err := nethttp.PostForm("https://example.com", nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.Equal(t, 4, countRule(findings, "default-http-client"),
		"net/http client package helpers must trigger rule, got %+v", findings)
}

func TestScan_DoesNotFlagLocalVarNamedHTTP(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `package svc

func wire() {
	http := struct{ DefaultClient int }{DefaultClient: 1}
	_ = http.DefaultClient
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "default-http-client"),
		"local variable named http (no net/http import) must not trigger, got %+v", findings)
}

func TestScan_HTTPClientHelpersRespectShadowedHTTP(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `package svc

import "net/http"

var _ http.Handler

func wire() {
	http := struct {
		Get func(string) error
	}{}
	_ = http.Get("https://example.com")
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "default-http-client"),
		"shadowed http identifier must not be flagged, got %+v", findings)
}

func TestScan_SkipsGeneratedFileByHeader(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `// Code generated by stringer; DO NOT EDIT.

package svc

import "net/http"

func wire() {
	c := http.DefaultClient
	_ = c
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "default-http-client"),
		"file with canonical generated header must be skipped, got %+v", findings)
}

func TestScan_DoesNotSkipFileWithoutHeader(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client.go", `// Some unrelated comment

package svc

import "net/http"

func wire() {
	c := http.DefaultClient
	_ = c
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "default-http-client"),
		"file without generated header must be scanned, got %+v", findings)
}

func TestScan_FlagsHTTPServerDirectConstruction_Pointer(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

import "net/http"

func wire() {
	srv := &http.Server{Addr: ":8080"}
	_ = srv
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "http-server-direct-construction"),
		"&http.Server{...} must be flagged, got %+v", findings)
}

func TestScan_FlagsHTTPServerDirectConstruction_Value(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

import "net/http"

func wire() {
	srv := http.Server{Addr: ":8080"}
	_ = srv
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "http-server-direct-construction"),
		"http.Server{...} composite literal must be flagged, got %+v", findings)
}

func TestScan_FlagsHTTPServerDirectConstruction_New(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

import "net/http"

func wire() {
	srv := new(http.Server)
	_ = srv
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "http-server-direct-construction"),
		"new(http.Server) must be flagged, got %+v", findings)
}

func TestScan_FlagsHTTPServerPackageHelpers(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

import (
	"net"
	nethttp "net/http"
)

func wire(ln net.Listener, handler nethttp.Handler) error {
	if err := nethttp.ListenAndServe(":8080", handler); err != nil {
		return err
	}
	if err := nethttp.ListenAndServeTLS(":8443", "cert.pem", "key.pem", handler); err != nil {
		return err
	}
	if err := nethttp.Serve(ln, handler); err != nil {
		return err
	}
	return nethttp.ServeTLS(ln, handler, "cert.pem", "key.pem")
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.Equal(t, 4, countRule(findings, "http-server-direct-construction"),
		"net/http server package helpers must be flagged, got %+v", findings)
}

func TestScan_HTTPServerPackageHelpersRespectShadowedHTTP(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

import "net/http"

var _ http.Handler

func wire() {
	http := struct {
		ListenAndServe func(string, http.Handler) error
	}{}
	_ = http.ListenAndServe(":8080", nil)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "http-server-direct-construction"),
		"shadowed http identifier must not be flagged, got %+v", findings)
}

func TestScan_AcceptsHTTPxNewServer(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2"

func wire() {
	srv := httpx.NewServer(":8080", handler, httpx.WithErrorLog(l))
	_ = srv
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "http-server-direct-construction"),
		"httpx.NewServer must not be flagged, got %+v", findings)
}

func TestScan_SkipsHTTPServerInTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server_test.go", `package svc

import "net/http"

func wire() {
	srv := &http.Server{Addr: ":8080"}
	_ = srv
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "http-server-direct-construction"),
		"raw http.Server in _test.go must not be flagged, got %+v", findings)
}

// TestScan_ExemptsKitFactoryHTTPServerByPackagePath verifies that the
// canonical kit factory file (the one implementing httpx.NewServer)
// is exempt from http-server-direct-construction so kit-doctor stays
// clean against rho-kit itself.
func TestScan_ExemptsKitFactoryHTTPServerByPackagePath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "httpx"), 0o700))
	writeFile(t, dir, "httpx/go.mod", "module github.com/bds421/rho-kit/httpx/v2\n\ngo 1.26.2\n")
	writeFile(t, dir, "httpx/httpx.go", `package httpx

import "net/http"

func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h}
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "http-server-direct-construction"),
		"file in github.com/bds421/rho-kit/httpx/v2 must be exempt, got %+v", findings)
}

func TestScan_FactoryExemptionsDoNotCoverSiblingPackages(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "httpx", "middleware", "unsafe"), 0o700))
	writeFile(t, dir, "httpx/go.mod", "module github.com/bds421/rho-kit/httpx/v2\n\ngo 1.26.2\n")
	writeFile(t, dir, "httpx/middleware/unsafe/unsafe.go", `package unsafe

import "net/http"

func New() *http.Server {
	return &http.Server{Addr: ":8080"}
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "http-server-direct-construction"),
		"factory exemptions must be exact package paths, got %+v", findings)
}

func TestScan_ExemptsDefaultTransportInJWTUtilPackage(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "security", "jwtutil"), 0o700))
	writeFile(t, dir, "security/go.mod", "module github.com/bds421/rho-kit/security/v2\n\ngo 1.26.2\n")
	writeFile(t, dir, "security/jwtutil/jwtutil.go", `package jwtutil

import "net/http"

var base = http.DefaultTransport
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "default-http-client"),
		"jwtutil factory package must be exempt, got %+v", findings)
}

func TestScan_DefaultTransportExemptionDoesNotCoverWholeModule(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "security", "other"), 0o700))
	writeFile(t, dir, "security/go.mod", "module github.com/bds421/rho-kit/security/v2\n\ngo 1.26.2\n")
	writeFile(t, dir, "security/other/other.go", `package other

import "net/http"

var base = http.DefaultTransport
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "default-http-client"),
		"default transport exemption must not cover sibling packages, got %+v", findings)
}

// TestScan_FlagsHTTPServerOutsideKitFactory verifies that the same
// pattern in any other module is still flagged.
func TestScan_FlagsHTTPServerOutsideKitFactory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "mypkg"), 0o700))
	writeFile(t, dir, "mypkg/go.mod", "module example.com/mypkg\n\ngo 1.26.2\n")
	writeFile(t, dir, "mypkg/foo.go", `package mypkg

import "net/http"

func New() *http.Server {
	return &http.Server{Addr: ":8080"}
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "http-server-direct-construction"),
		"non-kit module must still be flagged, got %+v", findings)
}

// TestScan_InlineSuppressionAllowsHTTPServer verifies the per-line
// suppression marker. Service repos use this to mark exceptional
// adapter code.
func TestScan_InlineSuppressionAllowsHTTPServer(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "mypkg"), 0o700))
	writeFile(t, dir, "mypkg/go.mod", "module example.com/mypkg\n\ngo 1.26.2\n")
	writeFile(t, dir, "mypkg/foo.go", `package mypkg

import "net/http"

func New() *http.Server {
	return &http.Server{Addr: ":8080"} // kit-doctor:allow http-server-direct-construction reason="legacy adapter"
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "http-server-direct-construction"),
		"inline suppression must skip finding, got %+v", findings)
}

// TestScan_InlineSuppressionAboveLineAllowsHTTPServer verifies the
// suppression marker placed on the line above the offending code.
func TestScan_InlineSuppressionAboveLineAllowsHTTPServer(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "mypkg"), 0o700))
	writeFile(t, dir, "mypkg/go.mod", "module example.com/mypkg\n\ngo 1.26.2\n")
	writeFile(t, dir, "mypkg/foo.go", `package mypkg

import "net/http"

func New() *http.Server {
	// kit-doctor:allow http-server-direct-construction reason="legacy adapter"
	return &http.Server{Addr: ":8080"}
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "http-server-direct-construction"),
		"suppression on the line above must skip finding, got %+v", findings)
}

// TestScan_InlineSuppressionForWrongRuleStillFlags verifies that
// suppression names a single rule and does not silence other findings.
func TestScan_InlineSuppressionForWrongRuleStillFlags(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "mypkg"), 0o700))
	writeFile(t, dir, "mypkg/go.mod", "module example.com/mypkg\n\ngo 1.26.2\n")
	writeFile(t, dir, "mypkg/foo.go", `package mypkg

import "net/http"

func New() *http.Server {
	return &http.Server{Addr: ":8080"} // kit-doctor:allow some-other-rule
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "http-server-direct-construction"),
		"unrelated suppression must not silence the rule, got %+v", findings)
}

// TestScan_DefaultHTTPClientSkipsTestFiles verifies the rule no
// longer flags _test.go usage of http.DefaultTransport (tests swap
// it deliberately to assert helpers stay panic-free).
func TestScan_DefaultHTTPClientSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "client_test.go", `package svc

import "net/http"

func wire() {
	c := http.DefaultClient
	_ = c
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "default-http-client"),
		"_test.go must not be flagged for default-http-client, got %+v", findings)
}

func TestScan_FlagsHTTPServerMissingErrorLog(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2"

func wire() {
	httpx.NewServer(handler, httpx.WithReadTimeout(10))
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "http-server-error-log"))
}

func TestScan_FlagsHTTPServerMissingErrorLog_AliasedImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

import kithttpx "github.com/bds421/rho-kit/httpx/v2"

func wire() {
	kithttpx.NewServer(handler)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "http-server-error-log"),
		"aliased httpx import must still trigger rule, got %+v", findings)
}

func TestScan_DoesNotFlagLocalVarNamedHTTPx(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.go", `package svc

func wire() {
	httpx := struct {
		NewServer func(any)
	}{}
	httpx.NewServer(handler)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "http-server-error-log"),
		"local variable named httpx must not trigger package rule, got %+v", findings)
}

func TestScan_HTTPServerMissingErrorLogSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server_test.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2"

func wire() {
	httpx.NewServer(handler)
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "http-server-error-log"),
		"_test.go must not be flagged for http-server-error-log, got %+v", findings)
}

func TestScan_SkipsVendor(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "vendor", "x"), 0o700))
	writeFile(t, dir, "vendor/x/x.go", `package x

import "net/http"

var c = http.DefaultClient
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	for _, f := range findings {
		assert.NotContains(t, f.File, "vendor", "vendor must be skipped")
	}
}

// TestScan_FlagsRateLimitOmission pins Lens F A.15: a fluent
// `app.New(...).Run()` chain with no rate-limit declaration must be
// surfaced by kit-doctor before the Builder fails closed at runtime.
func TestScan_FlagsRateLimitOmission(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/v2"

func wire() {
	_ = app.New("svc", "v1", app.BaseConfig{}).
		Run()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "rate-limit-omission"),
		"Builder.Run() without a rate-limit declaration must be flagged, got %+v", findings)
	for _, f := range findings {
		if f.Rule == "rate-limit-omission" {
			assert.Equal(t, rules.High, f.Severity,
				"rate-limit-omission must be HIGH severity")
		}
	}
}

// TestScan_FlagsRateLimitOmission_AliasedImport ensures the rule
// resolves through an import alias.
func TestScan_FlagsRateLimitOmission_AliasedImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import kitapp "github.com/bds421/rho-kit/app/v2"

func wire() {
	_ = kitapp.New("svc", "v1", kitapp.BaseConfig{}).
		Run()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "rate-limit-omission"),
		"aliased app import must still trigger rule, got %+v", findings)
}

// TestScan_AcceptsRateLimitIP verifies registering ratelimit.IP via
// Builder.With suppresses the rule — the canonical declaration shape.
func TestScan_AcceptsRateLimitIP(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import (
	"time"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/app/ratelimit/v2"
)

func wire() {
	_ = app.New("svc", "v1", app.BaseConfig{}).
		With(ratelimit.IP(100, time.Minute)).
		Run()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "rate-limit-omission"),
		"ratelimit.IP must satisfy the rule, got %+v", findings)
}

// TestScan_AcceptsRateLimitKeyed verifies a keyed limiter alone is
// sufficient declaration.
func TestScan_AcceptsRateLimitKeyed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import (
	"time"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/app/ratelimit/v2"
)

func wire() {
	_ = app.New("svc", "v1", app.BaseConfig{}).
		With(ratelimit.Keyed("api", 10, time.Minute)).
		Run()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "rate-limit-omission"),
		"ratelimit.Keyed must satisfy the rule, got %+v", findings)
}

// TestScan_AcceptsRateLimitWithoutRateLimit verifies the explicit
// opt-out suppresses the rule. The Builder's own panic mirrors this.
func TestScan_AcceptsRateLimitWithoutRateLimit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/v2"

func wire() {
	_ = app.New("svc", "v1", app.BaseConfig{}).
		WithoutRateLimit().
		Run()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "rate-limit-omission"),
		"WithoutRateLimit must satisfy the rule, got %+v", findings)
}

// TestScan_RateLimitOmissionSkipsTestFiles confirms tests are not
// flagged — they routinely build Builders without ever reaching Run.
func TestScan_RateLimitOmissionSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire_test.go", `package svc

import "github.com/bds421/rho-kit/app/v2"

func wire() {
	_ = app.New("svc", "v1", app.BaseConfig{}).
		Run()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "rate-limit-omission"),
		"_test.go must not be flagged for rate-limit-omission, got %+v", findings)
}

// TestScan_RateLimitOmissionRespectsInlineSuppression confirms the
// standard `kit-doctor:allow` marker silences the rule.
func TestScan_RateLimitOmissionRespectsInlineSuppression(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/v2"

func wire() {
	_ = app.New("svc", "v1", app.BaseConfig{}).
		Run() // kit-doctor:allow rate-limit-omission reason="upstream gateway enforces rate limits"
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "rate-limit-omission"),
		"inline suppression must skip finding, got %+v", findings)
}

// TestScan_RateLimitOmissionIgnoresUnrelatedRun ensures the rule does
// not match `.Run()` calls on receivers that did not originate at
// `<app>.New(...)`. Other libraries use the same name.
func TestScan_RateLimitOmissionIgnoresUnrelatedRun(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

type cli struct{}

func (cli) Run() error { return nil }

func wire() {
	_ = cli{}.Run()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "rate-limit-omission"),
		"unrelated Run() calls must not be flagged, got %+v", findings)
}

func TestExitCode_FloorRespected(t *testing.T) {
	findings := []rules.Finding{
		{Severity: rules.Warning, Rule: "x"},
	}
	assert.Equal(t, 0, exitCode(findings, rules.High))
	assert.Equal(t, 1, exitCode(findings, rules.Warning))
}

func TestFormatFindings_RendersAtLeastOneLine(t *testing.T) {
	findings := []rules.Finding{
		{
			Rule: "x", Severity: rules.High, File: "a.go", Line: 7,
			Message: "boom", Suggestion: "fix it",
		},
	}
	out := formatFindings(findings)
	assert.Contains(t, out, "✗ HIGH [x]: boom")
	assert.Contains(t, out, "at a.go:7")
	assert.Contains(t, out, "fix: fix it")
}

func TestFormatFindings_EmptyShowsCheck(t *testing.T) {
	out := formatFindings(nil)
	assert.True(t, strings.HasPrefix(out, "✓"))
}

// Wave 173: websocket-any-origin-unsafe + websocket-missing-max-connections
// + centrifuge-missing-jwt-auth pin the kit's wave 157 / 164 hardening
// surface against consumer-side misconfiguration.

func TestScan_FlagsWebsocketAnyOriginUnsafe(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ws.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/websocket"

func wire() {
	websocket.Handle(websocket.WithAnyOriginUnsafe(), websocket.WithMaxConnections(100))
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "websocket-any-origin-unsafe"),
		"WithAnyOriginUnsafe in production code must flag, got %+v", findings)
}

func TestScan_WebsocketAnyOriginUnsafeSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ws_test.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/websocket"

func wire() {
	websocket.Handle(websocket.WithAnyOriginUnsafe())
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "websocket-any-origin-unsafe"),
		"_test.go must not flag, got %+v", findings)
}

func TestScan_FlagsWebsocketMissingMaxConnections(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ws.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/websocket"

func wire() {
	websocket.Handle(websocket.WithAllowedOrigins([]string{"https://example.com"}))
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "websocket-missing-max-connections"),
		"Handle without WithMaxConnections must flag, got %+v", findings)
}

func TestScan_AcceptsWebsocketWithMaxConnections(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ws.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/websocket"

func wire() {
	websocket.Handle(websocket.WithMaxConnections(1000))
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "websocket-missing-max-connections"),
		"WithMaxConnections must suppress finding, got %+v", findings)
}

func TestScan_FlagsCentrifugeMissingJWTAuth(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "realtime.go", `package svc

import "github.com/bds421/rho-kit/realtime/v2/centrifuge"

func wire() {
	_, _ = centrifuge.NewNode(centrifuge.WithChannelClassifier(classifier))
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "centrifuge-missing-jwt-auth"),
		"NewNode without WithJWTAuth must flag, got %+v", findings)
}

func TestScan_AcceptsCentrifugeWithJWTAuth(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "realtime.go", `package svc

import "github.com/bds421/rho-kit/realtime/v2/centrifuge"

func wire() {
	_, _ = centrifuge.NewNode(centrifuge.WithJWTAuth(provider))
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "centrifuge-missing-jwt-auth"),
		"WithJWTAuth must suppress finding, got %+v", findings)
}

func TestScan_CentrifugeMissingJWTAuthSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "realtime_test.go", `package svc

import "github.com/bds421/rho-kit/realtime/v2/centrifuge"

func wire() {
	_, _ = centrifuge.NewNode()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "centrifuge-missing-jwt-auth"),
		"_test.go must not flag, got %+v", findings)
}

func TestScan_CentrifugeJWTAuthAliasedImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "realtime.go", `package svc

import rt "github.com/bds421/rho-kit/realtime/v2/centrifuge"

func wire() {
	_, _ = rt.NewNode()
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "centrifuge-missing-jwt-auth"),
		"aliased centrifuge import must still trigger rule, got %+v", findings)
}

// F2 wave 179: apphttp.WithoutTLS rule.

func TestScan_FlagsApphttpWithoutTLS(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import apphttp "github.com/bds421/rho-kit/app/http/v2"

func wire() {
	_ = apphttp.Module(apphttp.WithoutTLS())
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.True(t, hasRule(findings, "apphttp-without-tls"),
		"apphttp.WithoutTLS in production code must flag, got %+v", findings)
}

func TestScan_ApphttpWithoutTLSSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire_test.go", `package svc

import apphttp "github.com/bds421/rho-kit/app/http/v2"

func wire() {
	_ = apphttp.Module(apphttp.WithoutTLS())
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "apphttp-without-tls"),
		"_test.go must not flag, got %+v", findings)
}

func TestScan_ApphttpWithoutTLSRespectsInlineSuppression(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "wire.go", `package svc

import apphttp "github.com/bds421/rho-kit/app/http/v2"

func wire() {
	// kit-doctor:allow apphttp-without-tls
	_ = apphttp.Module(apphttp.WithoutTLS())
}
`)
	findings, err := scan(dir, rules.Registered())
	require.NoError(t, err)
	assert.False(t, hasRule(findings, "apphttp-without-tls"),
		"inline suppression marker must silence the rule, got %+v", findings)
}

func hasRule(findings []rules.Finding, name string) bool {
	for _, f := range findings {
		if f.Rule == name {
			return true
		}
	}
	return false
}

func countRule(findings []rules.Finding, name string) int {
	count := 0
	for _, f := range findings {
		if f.Rule == name {
			count++
		}
	}
	return count
}
