package rules

import (
	"go/ast"
	"go/token"
	"strings"
)

// kitPrimitiveCollisionRule flags consumer-service package declarations
// whose name matches a kit primitive's package basename. Catches the
// classic "I'll just write my own clock/retry/idempotency package"
// reinvention pattern.
//
// Background: services that import rho-kit sometimes reinvent
// primitives the kit already provides. The pattern is hard to catch
// in code review because the local package looks self-contained until
// you grep for `bds421/rho-kit/clock/v2` and notice it was never
// imported. This rule does the grep for you: any `package X` where X
// matches a kit primitive's basename is flagged with a pointer to the
// kit equivalent.
//
// The rule is INFO-level (not WARNING) because the collision is
// sometimes intentional — a service may have a domain-specific "lock"
// or "queue" concept that's deliberately different from the kit's
// distributed-lock or task-queue primitives. The finding's Suggestion
// is the concrete kit-package import path; reviewers decide whether
// to consolidate.
type kitPrimitiveCollisionRule struct{}

func (kitPrimitiveCollisionRule) Name() string { return "kit-primitive-collision" }

// kitModulePrefixTrimmed is the rho-kit module root import path with no
// trailing slash; kitModulePrefix matches any sub-package beneath it.
// A file whose resolved package import path is the root or lives under
// the prefix is the kit's own code and is exempt from this rule.
const (
	kitModulePrefixTrimmed = "github.com/bds421/rho-kit"
	kitModulePrefix        = kitModulePrefixTrimmed + "/"
)

// kitPrimitives maps a primitive's package basename to its
// fully-qualified kit import suggestion. Order doesn't matter; the
// rule does a single lookup per file.
//
// Curated to genuinely-reusable primitives. Things only the kit's
// internal wiring would use (app/, observability/, internal helpers)
// aren't here — a consumer service is never likely to write a
// "package app" that needs redirecting.
var kitPrimitives = map[string]string{
	// Foundational primitives.
	"clock":       "github.com/bds421/rho-kit/core/v2/clock",
	"contextutil": "github.com/bds421/rho-kit/core/v2/contextutil",
	"apperror":    "github.com/bds421/rho-kit/core/v2/apperror",
	"redact":      "github.com/bds421/rho-kit/core/v2/redact",
	"secret":      "github.com/bds421/rho-kit/core/v2/secret",
	"randstr":     "github.com/bds421/rho-kit/core/v2/randstr",
	"safecast":    "github.com/bds421/rho-kit/core/v2/safecast",
	"validate":    "github.com/bds421/rho-kit/core/v2/validate",
	"tenant":      "github.com/bds421/rho-kit/core/v2/tenant",
	"tlsclone":    "github.com/bds421/rho-kit/core/v2/tlsclone",
	"config":      "github.com/bds421/rho-kit/core/v2/config",

	// Resilience + concurrency.
	"retry":          "github.com/bds421/rho-kit/resilience/v2/retry",
	"circuitbreaker": "github.com/bds421/rho-kit/resilience/v2/circuitbreaker",
	"bulkhead":       "github.com/bds421/rho-kit/resilience/v2/bulkhead",
	"timeoutbudget":  "github.com/bds421/rho-kit/resilience/v2/timeoutbudget",
	"concurrency":    "github.com/bds421/rho-kit/runtime/v2/concurrency",
	"lifecycle":      "github.com/bds421/rho-kit/runtime/v2/lifecycle",
	"eventbus":       "github.com/bds421/rho-kit/runtime/v2/eventbus",
	"batchworker":    "github.com/bds421/rho-kit/runtime/v2/batchworker",
	"saga":           "github.com/bds421/rho-kit/runtime/v2/saga",
	"cron":           "github.com/bds421/rho-kit/runtime/v2/cron",

	// Data primitives.
	"idempotency": "github.com/bds421/rho-kit/data/v2/idempotency",
	"cache":       "github.com/bds421/rho-kit/data/v2/cache",
	"queue":       "github.com/bds421/rho-kit/data/v2/queue",
	"stream":      "github.com/bds421/rho-kit/data/v2/stream",
	"lock":        "github.com/bds421/rho-kit/data/v2/lock",
	"ratelimit":   "github.com/bds421/rho-kit/data/v2/ratelimit",
	"budget":      "github.com/bds421/rho-kit/data/v2/budget",
	"approval":    "github.com/bds421/rho-kit/data/v2/approval",
	"actionlog":   "github.com/bds421/rho-kit/data/v2/actionlog",

	// Crypto + security.
	"signing":  "github.com/bds421/rho-kit/crypto/v2/signing",
	"passhash": "github.com/bds421/rho-kit/crypto/v2/passhash",
	"paseto":   "github.com/bds421/rho-kit/crypto/v2/paseto",
	"encrypt":  "github.com/bds421/rho-kit/crypto/v2/encrypt",
	"envelope": "github.com/bds421/rho-kit/crypto/v2/envelope",
	"masking":  "github.com/bds421/rho-kit/crypto/v2/masking",
	"jwtutil":  "github.com/bds421/rho-kit/security/v2/jwtutil",
	"csrf":     "github.com/bds421/rho-kit/security/v2/csrf",
	"netutil":  "github.com/bds421/rho-kit/security/v2/netutil",
	"secrets":  "github.com/bds421/rho-kit/infra/secrets/v2",

	// Observability.
	"logattr":        "github.com/bds421/rho-kit/observability/v2/logattr",
	"redmetrics":     "github.com/bds421/rho-kit/observability/v2/redmetrics",
	"runtimemetrics": "github.com/bds421/rho-kit/observability/v2/runtimemetrics",
	"slo":            "github.com/bds421/rho-kit/observability/v2/slo",
	"auditlog":       "github.com/bds421/rho-kit/observability/v2/auditlog",
	"tracing":        "github.com/bds421/rho-kit/observability/v2/tracing",
	"health":         "github.com/bds421/rho-kit/observability/v2/health",

	// I/O.
	"atomicfile": "github.com/bds421/rho-kit/io/v2/atomicfile",
	"progress":   "github.com/bds421/rho-kit/io/v2/progress",
}

func (r kitPrimitiveCollisionRule) Run(fset *token.FileSet, file *ast.File) []Finding {
	if file == nil {
		return nil
	}
	// Skip the kit's own files — internal packages legitimately use
	// these names. The rule is meant for downstream consumers. Detect
	// the kit by the resolved module import path (read from the nearest
	// go.mod) rather than a filesystem-path substring: the latter both
	// false-negatives a rho-kit checkout under a different directory
	// name and false-positives a consumer repo that merely has a
	// "rho-kit" path segment.
	filename := fset.Position(file.Pos()).Filename
	if pkg := packageAtPath(filename); pkg != "" &&
		(pkg == kitModulePrefixTrimmed || strings.HasPrefix(pkg, kitModulePrefix)) {
		return nil
	}
	// Skip test files — `package foo_test` is a Go convention, not a
	// package-naming choice the rule should second-guess.
	if strings.HasSuffix(filename, "_test.go") {
		return nil
	}
	pkgName := file.Name.Name
	suggested, ok := kitPrimitives[pkgName]
	if !ok {
		return nil
	}
	// The engine dispatches one Run per file, so a package split across
	// N non-test files whose name collides yields N findings — one per
	// file. That is acceptable: each finding points at the package
	// clause of a distinct file the author may consolidate or rename,
	// and the surrounding text is identical so de-dup is cheap for the
	// operator. (No per-directory de-dup is performed here because the
	// rule is stateless across files.)
	return []Finding{{
		Rule:     r.Name(),
		Severity: Info,
		File:     filename,
		Line:     fset.Position(file.Package).Line,
		Message: "package name `" + pkgName + "` collides with kit primitive `" +
			suggested + "`. If you're solving the same problem, prefer the kit primitive — " +
			"that's why it's there. If this is a domain concept that happens to share the " +
			"name, rename the local package to make the distinction explicit (e.g. `" +
			"my" + pkgName + "` or `domain" + pkgName + "`).",
		Suggestion: "import " + suggested + " — or rename this package",
	}}
}
