package rules

import (
	"go/ast"
	"go/token"
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
	appAliases := map[string]struct{}{}
	for _, imp := range rateLimitOmissionImports {
		for name := range importAliasesFor(file, imp) {
			appAliases[name] = struct{}{}
		}
	}
	if len(appAliases) == 0 {
		return nil
	}
	rlAliases := map[string]struct{}{}
	for _, imp := range rateLimitBridgeImports {
		for name := range importAliasesFor(file, imp) {
			rlAliases[name] = struct{}{}
		}
	}
	var findings []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Match the outer `.Run()` call so each chain is flagged once.
		if !isMethodCall(call, "Run") {
			return true
		}
		// Only the zero-arg form belongs to the Builder — keep this
		// rule narrow so unrelated `.Run(ctx)` chains in other
		// libraries do not match.
		if len(call.Args) != 0 {
			return true
		}
		if !chainOriginatesFromBuilderNew(call, appAliases) {
			return true
		}
		if chainHas(call, "WithoutRateLimit") {
			return true
		}
		if chainRegistersRateLimitModule(call, rlAliases) {
			return true
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
		pos := fset.Position(reportPos)
		if isExempt(fset, file, r.Name(), pos.Filename, pos.Line) {
			return true
		}
		findings = append(findings, Finding{
			Rule:     r.Name(),
			Severity: High,
			File:     pos.Filename,
			Line:     pos.Line,
			Message:  "app.Builder.Run() without an explicit rate-limit declaration",
			Suggestion: "chain .With(ratelimit.IP(n, window)) or .With(ratelimit.Keyed(name, n, window)); " +
				"use .WithoutRateLimit() only for services whose traffic is bounded by another control (mTLS peer set, upstream gateway limit, internal cron worker). " +
				"Suppress with `// kit-doctor:allow rate-limit-omission` only when the omission is reviewed.",
		})
		return true
	})
	return findings
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
		if sel.Sel.Name == "With" {
			for _, arg := range c.Args {
				inner, ok := arg.(*ast.CallExpr)
				if !ok {
					continue
				}
				innerSel, ok := inner.Fun.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				if _, named := rateLimitBridgeConstructors[innerSel.Sel.Name]; !named {
					continue
				}
				ident, ok := innerSel.X.(*ast.Ident)
				if !ok {
					continue
				}
				if isPackageAlias(ident, rlAliases) {
					return true
				}
			}
		}
		cur = sel.X
	}
}
