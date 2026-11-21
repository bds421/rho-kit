package rules

import (
	"go/ast"
	"go/token"
	"slices"
	"strings"
)

// rateLimitOmissionRule flags a fluent `app.New(...).Run()` chain in
// non-test code that fails to declare a rate-limit policy. Every other
// always-on security control on the Builder (TLS, JWT issuer / audience,
// internal-host loopback) fails-loud at Build time when unconfigured;
// rate limiting joined that contract in v2.0.0 (Lens F A.5) so a service
// must affirmatively choose one of:
//
//   - register `ratelimit.IP(...)`   via `Builder.With(...)`
//   - register `ratelimit.Keyed(...)` via `Builder.With(...)`
//   - call `Builder.WithoutRateLimit()` to acknowledge the un-throttled
//     posture
//
// The Builder will refuse to Build / Run if none of these appear in the
// chain. kit-doctor flags the same shape statically so editor
// integrations and CI catch the omission before the binary is started.
//
// Severity is HIGH rather than CRITICAL because the runtime gate is
// fail-closed — kit-doctor exists to surface the wiring bug pre-build,
// not to prevent a leak that would otherwise ship.
//
// The check is intentionally narrow:
//
//   - It matches only fluent chains that originate from `app.New(...)`
//     and terminate at `.Run()` so unrelated `New(...).Run()` chains in
//     other packages are not flagged.
//   - It skips `_test.go` files because tests routinely build Builders
//     that never reach Run.
//   - It honours `// kit-doctor:allow rate-limit-omission` for tooling
//     that deliberately constructs a Builder without a rate-limit
//     option (e.g. inline scaffold validation).
type rateLimitOmissionRule struct{}

func (rateLimitOmissionRule) Name() string { return "rate-limit-omission" }

// rateLimitOmissionImports lists the import paths that expose the
// Builder constructor. Aliased re-exports inside the kit would surface
// as new entries here.
var rateLimitOmissionImports = []string{
	"github.com/bds421/rho-kit/app/v2",
}

// rateLimitBridgeImports lists the import paths whose `IP` and `Keyed`
// constructors satisfy the rate-limit gate when passed to
// `Builder.With(...)`.
var rateLimitBridgeImports = []string{
	"github.com/bds421/rho-kit/app/ratelimit/v2",
}

// rateLimitBridgeConstructors names the functions in the ratelimit
// bridge package whose registration satisfies the gate.
var rateLimitBridgeConstructors = map[string]struct{}{
	"IP":    {},
	"Keyed": {},
}

func (r rateLimitOmissionRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	if strings.HasSuffix(fset.Position(file.Pos()).Filename, "_test.go") {
		return nil
	}
	appAliases := importAliasesForAny(file, rateLimitOmissionImports)
	if len(appAliases) == 0 {
		return nil
	}
	rlAliases := importAliasesForAny(file, rateLimitBridgeImports)
	return highFindings(fset, file, r.Name(), func(n ast.Node) (flaggedUse, bool) {
		return unlimitedBuilderRun(n, appAliases, rlAliases)
	})
}

// importAliasesForAny returns the identifiers that resolve to any of
// importPaths inside file.
func importAliasesForAny(file *ast.File, importPaths []string) map[string]struct{} {
	aliases := map[string]struct{}{}
	for _, imp := range importPaths {
		for name := range importAliasesFor(file, imp) {
			aliases[name] = struct{}{}
		}
	}
	return aliases
}

// unlimitedBuilderRun reports n when it is the zero-argument `.Run()` of a
// Builder chain that neither registers a rate-limit module nor opts out
// with WithoutRateLimit.
func unlimitedBuilderRun(n ast.Node, appAliases, rlAliases map[string]struct{}) (flaggedUse, bool) {
	call, ok := n.(*ast.CallExpr)
	// Match the outer `.Run()` call so each chain is flagged once. Only
	// the zero-arg form belongs to the Builder — keep this rule narrow so
	// unrelated `.Run(ctx)` chains in other libraries do not match.
	if !ok || !isMethodCall(call, "Run") || len(call.Args) != 0 {
		return flaggedUse{}, false
	}
	if !chainOriginatesFromBuilderNew(call, appAliases) {
		return flaggedUse{}, false
	}
	if chainHas(call, "WithoutRateLimit") || chainRegistersRateLimitModule(call, rlAliases) {
		return flaggedUse{}, false
	}
	// Report at the `.Run()` selector line rather than the
	// outer-call position so suppression markers placed next to
	// `.Run()` work as written. CallExpr.Pos returns the start of
	// the receiver expression, which for a multi-line fluent chain
	// is the `app.New(...)` line several lines above the offending
	// `.Run()` call.
	reportPos := call.Pos()
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		reportPos = sel.Sel.Pos()
	}
	return flaggedUse{
		pos:     reportPos,
		message: "app.Builder.Run() without an explicit rate-limit declaration",
		suggestion: "chain .With(ratelimit.IP(n, window)) or .With(ratelimit.Keyed(name, n, window)); " +
			"use .WithoutRateLimit() only for services whose traffic is bounded by another control (mTLS peer set, upstream gateway limit, internal cron worker). " +
			"Suppress with `// kit-doctor:allow rate-limit-omission` only when the omission is reviewed.",
	}, true
}

// chainOriginatesFromBuilderNew reports whether the fluent chain
// containing call traces back to a `<app>.New(...)` constructor call,
// where <app> is one of the registered aliases for the app/v2 import.
// Walking the chain rather than relying on type information keeps the
// rule lightweight (no go/types pass) and matches the other Builder
// rules in this package.
func chainOriginatesFromBuilderNew(call *ast.CallExpr, aliases map[string]struct{}) bool {
	cur := ast.Expr(call)
	for {
		c, ok := cur.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		// Step inside the next selector receiver.
		next := sel.X
		// If the receiver is a direct `<alias>.New(...)` call, the
		// chain is rooted at the Builder constructor.
		if inner, ok := next.(*ast.CallExpr); ok {
			if innerSel, ok := inner.Fun.(*ast.SelectorExpr); ok && innerSel.Sel.Name == "New" {
				if ident, ok := innerSel.X.(*ast.Ident); ok && isPackageAlias(ident, aliases) {
					return true
				}
			}
		}
		cur = next
	}
}

// chainRegistersRateLimitModule reports whether any `.With(...)` call
// in the fluent chain rooted at call passes an argument that is a
// direct call to `<ratelimit>.IP(...)` or `<ratelimit>.Keyed(...)`,
// where `<ratelimit>` is a registered import alias for the
// app/ratelimit bridge. When the alias set is empty (no import) the
// scan short-circuits to false — the file cannot register a rate-
// limit module without importing the bridge.
func chainRegistersRateLimitModule(call *ast.CallExpr, rlAliases map[string]struct{}) bool {
	if len(rlAliases) == 0 {
		return false
	}
	cur := ast.Expr(call)
	for {
		c, ok := cur.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		if sel.Sel.Name == "With" && slices.ContainsFunc(c.Args, func(arg ast.Expr) bool {
			return isRateLimitBridgeCall(arg, rlAliases)
		}) {
			return true
		}
		cur = sel.X
	}
}

// isRateLimitBridgeCall reports whether expr is a direct call to
// `<ratelimit>.IP(...)` or `<ratelimit>.Keyed(...)`.
func isRateLimitBridgeCall(expr ast.Expr, rlAliases map[string]struct{}) bool {
	inner, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	innerSel, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if _, named := rateLimitBridgeConstructors[innerSel.Sel.Name]; !named {
		return false
	}
	ident, ok := innerSel.X.(*ast.Ident)
	return ok && isPackageAlias(ident, rlAliases)
}
