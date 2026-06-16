package rules

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// parseRuleFixture parses src as a Go file named filename and returns
// the fileset and ast.File so a rule's Run can be exercised directly.
func parseRuleFixture(t *testing.T, filename, src string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse fixture %q: %v", filename, err)
	}
	return fset, file
}

func hasRuleFinding(findings []Finding, rule string) bool {
	for _, f := range findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// TestWebsocketRulesMatchRealImportPath pins both websocket rules to
// the real module path consumers import
// (github.com/bds421/rho-kit/httpx/websocket/v2). Before the fix the
// rules only listed github.com/bds421/rho-kit/httpx/v2/websocket — a
// path no module exposes — so importAliasesFor never matched real
// consumer code and the rules silently never fired.
func TestWebsocketRulesMatchRealImportPath(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		src  string
		want bool
	}{
		{
			name: "any-origin-unsafe fires on real import path",
			rule: websocketAnyOriginUnsafeRule{},
			src: `package svc

import "github.com/bds421/rho-kit/httpx/websocket/v2"

func wire() {
	websocket.Handle(websocket.WithAnyOriginUnsafe(), websocket.WithMaxConnections(100))
}
`,
			want: true,
		},
		{
			name: "missing-max-connections fires on real import path",
			rule: websocketMissingMaxConnectionsRule{},
			src: `package svc

import "github.com/bds421/rho-kit/httpx/websocket/v2"

func wire() {
	websocket.Handle(websocket.WithAllowedOrigins([]string{"https://example.com"}))
}
`,
			want: true,
		},
		{
			name: "missing-max-connections suppressed by WithMaxConnections on real path",
			rule: websocketMissingMaxConnectionsRule{},
			src: `package svc

import "github.com/bds421/rho-kit/httpx/websocket/v2"

func wire() {
	websocket.Handle(websocket.WithMaxConnections(1000))
}
`,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fset, file := parseRuleFixture(t, "ws.go", tc.src)
			findings := tc.rule.Run(fset, file)
			got := hasRuleFinding(findings, tc.rule.Name())
			if got != tc.want {
				t.Fatalf("%s: got fired=%v, want %v (findings=%+v)", tc.rule.Name(), got, tc.want, findings)
			}
		})
	}
}

// TestCentrifugeRuleMatchesRealImportPath pins the critical
// centrifuge-missing-jwt-auth rule to the real module path
// (github.com/bds421/rho-kit/realtime/centrifuge/v2). Before the fix
// the rule listed github.com/bds421/rho-kit/realtime/v2/centrifuge —
// an unresolvable path — so an unauthenticated centrifuge.NewNode in
// real consumer code was never flagged.
func TestCentrifugeRuleMatchesRealImportPath(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{
			name: "NewNode without WithJWTAuth fires on real import path",
			src: `package svc

import "github.com/bds421/rho-kit/realtime/centrifuge/v2"

func wire() {
	_, _ = centrifuge.NewNode(centrifuge.WithChannelClassifier(classifier))
}
`,
			want: true,
		},
		{
			name: "WithJWTAuth suppresses finding on real import path",
			src: `package svc

import "github.com/bds421/rho-kit/realtime/centrifuge/v2"

func wire() {
	_, _ = centrifuge.NewNode(centrifuge.WithJWTAuth(provider))
}
`,
			want: false,
		},
		{
			name: "aliased real import path still fires",
			src: `package svc

import rt "github.com/bds421/rho-kit/realtime/centrifuge/v2"

func wire() {
	_, _ = rt.NewNode()
}
`,
			want: true,
		},
	}

	rule := centrifugeMissingJWTAuthRule{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fset, file := parseRuleFixture(t, "realtime.go", tc.src)
			findings := rule.Run(fset, file)
			got := hasRuleFinding(findings, rule.Name())
			if got != tc.want {
				t.Fatalf("%s: got fired=%v, want %v (findings=%+v)", rule.Name(), got, tc.want, findings)
			}
		})
	}
}
