package rules

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseSrc parses src as a Go file named name and returns the fset and
// file. It fails the test on a parse error.
func parseSrc(t *testing.T, name, src string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	require.NoError(t, err)
	return fset, file
}

// resetExemptionCache clears the per-process package cache so a test's
// temp-dir go.mod files are resolved freshly rather than from a sibling
// test's lookup.
func resetExemptionCache() {
	packageCacheMu.Lock()
	packageCache = map[string]string{}
	packageCacheMu.Unlock()
}

func ruleNames(findings []Finding) map[string]int {
	out := map[string]int{}
	for _, f := range findings {
		out[f.Rule]++
	}
	return out
}

// --- matchesSuppression: marker must be the first token, not a substring ---

func TestMatchesSuppression_LeadingMarkerMatches(t *testing.T) {
	assert.True(t, matchesSuppression("// kit-doctor:allow my-rule", "my-rule"),
		"a comment that starts with the marker and names the rule must match")
}

func TestMatchesSuppression_LeadingMarkerWithReasonMatches(t *testing.T) {
	assert.True(t,
		matchesSuppression(`// kit-doctor:allow my-rule reason="legacy"`, "my-rule"),
		"trailing reason must not prevent the match")
}

func TestMatchesSuppression_SubstringInProseDoesNotMatch(t *testing.T) {
	// This is the documented contract: the linter never matches by
	// substring. A TODO that merely mentions the marker must NOT
	// silence the rule.
	assert.False(t,
		matchesSuppression("// TODO: consider kit-doctor:allow my-rule here", "my-rule"),
		"marker buried inside prose must not suppress the finding")
}

func TestMatchesSuppression_WrongRuleDoesNotMatch(t *testing.T) {
	assert.False(t, matchesSuppression("// kit-doctor:allow other-rule", "my-rule"),
		"suppression for a different rule must not match")
}

func TestMatchesSuppression_LongerTokenDoesNotMatch(t *testing.T) {
	// `kit-doctor:allowance` must not be treated as the `kit-doctor:allow`
	// prefix followed by a rule name.
	assert.False(t, matchesSuppression("// kit-doctor:allowance my-rule", "my-rule"),
		"a longer accidental token must not match the marker prefix")
}

func TestMatchesSuppression_BlockCommentLeadingMarkerMatches(t *testing.T) {
	assert.True(t, matchesSuppression("/* kit-doctor:allow my-rule */", "my-rule"),
		"block-comment form with leading marker must match")
}

// --- callOptionsUnverifiable: ellipsis spread cannot be inspected ---

func firstCall(t *testing.T, file *ast.File) *ast.CallExpr {
	t.Helper()
	var found *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if c, ok := n.(*ast.CallExpr); ok {
			found = c
			return false
		}
		return true
	})
	require.NotNil(t, found, "expected at least one call expression")
	return found
}

func TestCallOptionsUnverifiable_EllipsisSpreadIsUnverifiable(t *testing.T) {
	_, file := parseSrc(t, "x.go", `package svc

func f(opts ...int) {}

func wire(opts []int) { f(opts...) }
`)
	call := firstCall(t, file)
	assert.True(t, callOptionsUnverifiable(call),
		"a spread call f(opts...) must be reported as unverifiable")
}

func TestCallOptionsUnverifiable_LiteralArgsAreVerifiable(t *testing.T) {
	_, file := parseSrc(t, "x.go", `package svc

func f(opts ...int) {}

func wire() { f(1, 2, 3) }
`)
	call := firstCall(t, file)
	assert.False(t, callOptionsUnverifiable(call),
		"literal args must be statically verifiable")
}

func TestCallOptionsUnverifiable_NilCall(t *testing.T) {
	assert.False(t, callOptionsUnverifiable(nil),
		"nil call must not be reported as unverifiable")
}

// --- option rules must not false-positive on a spread option slice ---

func runRule(t *testing.T, r Rule, name, src string) []Finding {
	t.Helper()
	fset, file := parseSrc(t, name, src)
	SetCurrentFile(file)
	return r.Run(fset, file)
}

func TestJWTMissingClaims_SpreadOptionsSuppressFinding(t *testing.T) {
	// jwt.Module(url, opts...) where opts may carry WithIssuer /
	// WithAudience must NOT be flagged Critical — the options cannot be
	// inspected statically.
	findings := runRule(t, jwtMissingClaimsRule{}, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/jwt/v2"

func wire(opts []jwt.Option) {
	_ = jwt.Module("https://issuer/.well-known/jwks.json", opts...)
}
`)
	assert.Empty(t, findings,
		"spread option slice must suppress jwt-missing-claims, got %+v", findings)
}

func TestJWTMissingClaims_LiteralMissingStillFlags(t *testing.T) {
	// Regression guard: the literal no-option call must STILL be flagged
	// (otherwise the unverifiable guard would be too broad).
	findings := runRule(t, jwtMissingClaimsRule{}, "wire.go", `package svc

import "github.com/bds421/rho-kit/app/jwt/v2"

func wire() {
	_ = jwt.Module("https://issuer/.well-known/jwks.json")
}
`)
	require.Len(t, findings, 2,
		"literal call with no options must still flag issuer + audience, got %+v", findings)
}

func TestIdempotencyUserExtractor_SpreadOptionsSuppressFinding(t *testing.T) {
	findings := runRule(t, idempotencyMissingUserExtractorRule{}, "mw.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/middleware/idempotency"

func wire(store any, opts []idempotency.Option) {
	idempotency.Middleware(store, opts...)
}
`)
	assert.Empty(t, findings,
		"spread option slice must suppress idempotency-user-extractor, got %+v", findings)
}

func TestCentrifugeMissingJWTAuth_SpreadOptionsSuppressFinding(t *testing.T) {
	findings := runRule(t, centrifugeMissingJWTAuthRule{}, "rt.go", `package svc

import "github.com/bds421/rho-kit/realtime/v2/centrifuge"

func wire(opts []centrifuge.Option) {
	_, _ = centrifuge.NewNode(opts...)
}
`)
	assert.Empty(t, findings,
		"spread option slice must suppress centrifuge-missing-jwt-auth, got %+v", findings)
}

func TestWebsocketMissingMaxConnections_SpreadOptionsSuppressFinding(t *testing.T) {
	findings := runRule(t, websocketMissingMaxConnectionsRule{}, "ws.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2/websocket"

func wire(opts []websocket.Option) {
	websocket.Handle(opts...)
}
`)
	assert.Empty(t, findings,
		"spread option slice must suppress websocket-missing-max-connections, got %+v", findings)
}

func TestHTTPServerErrorLog_SpreadOptionsSuppressFinding(t *testing.T) {
	findings := runRule(t, httpServerMissingErrorLogRule{}, "srv.go", `package svc

import "github.com/bds421/rho-kit/httpx/v2"

func wire(handler any, opts []httpx.Option) {
	httpx.NewServer(handler, opts...)
}
`)
	assert.Empty(t, findings,
		"spread option slice must suppress http-server-error-log, got %+v", findings)
}

// --- kit-primitive-collision: detect the kit by module path, not by the
// "/rho-kit/" filesystem-path substring ---

func TestKitPrimitiveCollision_ExemptsKitByModulePath(t *testing.T) {
	// A rho-kit checkout under a directory NOT named "rho-kit" must
	// still be recognised as the kit's own code (via its go.mod module
	// path) and therefore exempt.
	resetExemptionCache()
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "clock")
	require.NoError(t, os.MkdirAll(pkgDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module github.com/bds421/rho-kit/core/v2\n\ngo 1.26.2\n"), 0o600))
	src := filepath.Join(pkgDir, "clock.go")
	require.NoError(t, os.WriteFile(src, []byte("package clock\n"), 0o600))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
	require.NoError(t, err)

	findings := kitPrimitiveCollisionRule{}.Run(fset, file)
	assert.Empty(t, findings,
		"kit's own package (by module path) must be exempt even when the checkout dir is not named rho-kit, got %+v", findings)
}

func TestKitPrimitiveCollision_FlagsConsumerByModulePath(t *testing.T) {
	// A consumer repo whose path happens to contain a "rho-kit" segment
	// but whose module is NOT the kit must still be flagged.
	resetExemptionCache()
	dir := filepath.Join(t.TempDir(), "rho-kit")
	pkgDir := filepath.Join(dir, "clock")
	require.NoError(t, os.MkdirAll(pkgDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/myservice\n\ngo 1.26.2\n"), 0o600))
	src := filepath.Join(pkgDir, "clock.go")
	require.NoError(t, os.WriteFile(src, []byte("package clock\n"), 0o600))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
	require.NoError(t, err)

	findings := kitPrimitiveCollisionRule{}.Run(fset, file)
	names := ruleNames(findings)
	assert.Equal(t, 1, names["kit-primitive-collision"],
		"consumer repo under a path with a rho-kit segment must still be flagged, got %+v", findings)
}

func TestCallOptionsUnverifiable_VariableOptionArg(t *testing.T) {
	_, file := parseSrc(t, "x.go", `package svc

func Middleware(store any, opts ...any) {}
func WithUserExtractor(fn any) any { return fn }

func wire(store any, fn any) {
	opt := WithUserExtractor(fn)
	Middleware(store, opt)
}
`)
	// Find Middleware call (second CallExpr is WithUserExtractor; third is Middleware).
	var calls []*ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			calls = append(calls, c)
		}
		return true
	})
	require.GreaterOrEqual(t, len(calls), 2)
	// Last call should be Middleware(store, opt)
	assert.True(t, callOptionsUnverifiable(calls[len(calls)-1]),
		"Middleware(store, opt) with a scalar option var must be unverifiable")
}
