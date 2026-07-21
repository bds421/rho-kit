package asvs

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Evidence classifies how strongly a service can claim an ASVS control
// based on how the kit observes it (audit FR-007 / FR-008).
//
// The three classes are:
//
//   - [EvidenceCapability]: the kit *ships* the implementation. Importing
//     the package gives the service the *option* of using it. The
//     service still has to wire it correctly — capability evidence is
//     necessary but not sufficient.
//
//   - [EvidenceBuilderEnforced]: a scanner or verifier observed actual
//     Builder usage on the startup path. The Builder validator refuses
//     to construct a service without this control configured (e.g. TLS,
//     JWT audience, internal-host loopback). A plain import alone is
//     not enough for this class.
//
//   - [EvidenceRuntimeVerified]: kit-verify (the runtime conformance
//     tool) probes a running service and asserts the behaviour. This
//     is the only class that proves the control works as claimed in
//     production.
type Evidence string

const (
	EvidenceCapability      Evidence = "capability"
	EvidenceBuilderEnforced Evidence = "builder"
	EvidenceRuntimeVerified Evidence = "runtime"
)

// PackageClaim records that importing a kit package gives the importing
// service evidence for a set of ASVS controls. The registry is the
// kit's source of truth — adding an [Evidence] hook in source code is
// not enough; the package and its controls MUST land here so
// import-based scanning can see them.
type PackageClaim struct {
	// ImportPath is the canonical Go import path including the /v2
	// suffix (semantic import versioning).
	ImportPath string
	// Controls are the ASVS IDs the package satisfies.
	Controls []ID
	// Evidence classifies the strength of the claim.
	Evidence Evidence
	// Note is a one-line summary kit-doctor renders next to the claim.
	Note string
}

// packageRegistry is the kit's manifest of which import paths satisfy
// which ASVS controls. The registry is hand-maintained — adding a new
// package-level `// asvs: ...` annotation does NOT automatically grant
// import-evidence to the controls; a matching entry must be added here
// so kit-doctor's import scanner can resolve the package against the
// catalog.
//
// The hand-maintained shape is intentional: it forces a kit reviewer
// to consciously decide that "yes, importing this package
// genuinely gives a service [Evidence] for these controls". A pure
// generator from package comments would let any contributor claim
// kit-level coverage by typing the annotation, which is the trust
// failure FR-007 reported.
var packageRegistry = []PackageClaim{
	// Crypto — capability-level: importing the package only proves
	// the service *can* call the API, not that it does.
	{"github.com/bds421/rho-kit/crypto/v2/passhash", []ID{"V6.2.1"}, EvidenceCapability,
		"Argon2id password hashing"},
	{"github.com/bds421/rho-kit/crypto/v2/encrypt", []ID{"V6.2.1", "V6.4.1"}, EvidenceCapability,
		"AES-GCM at-rest encryption"},
	{"github.com/bds421/rho-kit/crypto/v2/envelope/gcpkms", []ID{"V6.2.1", "V6.4.1"}, EvidenceCapability,
		"GCP KMS envelope encryption"},
	{"github.com/bds421/rho-kit/crypto/v2/envelope/awskms", []ID{"V6.2.1", "V6.4.1"}, EvidenceCapability,
		"AWS KMS envelope encryption"},
	{"github.com/bds421/rho-kit/crypto/v2/envelope/azurekeyvault", []ID{"V6.2.1", "V6.4.1"}, EvidenceCapability,
		"Azure Key Vault envelope encryption"},
	{"github.com/bds421/rho-kit/crypto/v2/envelope/vaulttransit", []ID{"V6.2.1", "V6.4.1"}, EvidenceCapability,
		"HashiCorp Vault Transit envelope encryption"},
	{"github.com/bds421/rho-kit/crypto/v2/paseto", []ID{"V2.1.5", "V3.2.1"}, EvidenceCapability,
		"PASETO v4 public-key signing/verification with key-rotation Provider"},

	// Validation, storage, infra — capability-level.
	{"github.com/bds421/rho-kit/core/v2/validate", []ID{"V5.1.3"}, EvidenceCapability,
		"Schema-based input validation"},
	{"github.com/bds421/rho-kit/infra/v2/storage", []ID{"V12.1.1", "V12.3.1"}, EvidenceCapability,
		"MIME-sniff + size-limited uploads with server-side keys"},
	{"github.com/bds421/rho-kit/infra/v2/storage/storagehttp", []ID{"V12.1.1", "V12.3.1", "V13.4.1"}, EvidenceCapability,
		"HTTP upload handler with size cap"},

	// Auth / session — capability-level.
	{"github.com/bds421/rho-kit/security/v2/jwtutil", []ID{"V2.1.5", "V2.3.1", "V3.2.1"}, EvidenceCapability,
		"JWT verification + JWKS rotation"},
	{"github.com/bds421/rho-kit/security/v2/csrf", []ID{"V13.2.3", "V3.4.1"}, EvidenceCapability,
		"CSRF issuer with overlapping-secret rotation"},
	{"github.com/bds421/rho-kit/security/v2/netutil", []ID{"V9.1.1", "V14.4.1"}, EvidenceCapability,
		"Reloading TLS certificate source with TLS 1.2 floor and SNI enforcement"},
	{"github.com/bds421/rho-kit/infra/v2/sqldb/pgx", []ID{"V9.1.1"}, EvidenceCapability,
		"pgx-native Postgres pool with sslmode-aware preflight (TLS server communications)"},

	// Observability — capability-level.
	{"github.com/bds421/rho-kit/observability/v2/auditlog", []ID{"V7.1.1", "V7.4.1", "V4.1.5"}, EvidenceCapability,
		"Append-only audit log"},
	{"github.com/bds421/rho-kit/observability/v2/health", []ID{"V8.2.2", "V14.1.1"}, EvidenceCapability,
		"Health endpoints with no-store cache"},

	// HTTP middleware — capability when imported. Runtime verification
	// or startup-path analysis is required before treating it as
	// enforced.
	{"github.com/bds421/rho-kit/httpx/v2/middleware/auth", []ID{"V2.1.5", "V2.3.1", "V3.2.1", "V3.3.1", "V4.1.1", "V4.1.5"}, EvidenceCapability,
		"Authentication + revocation middleware"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/csrf", []ID{"V13.2.3", "V3.4.1"}, EvidenceCapability,
		"Anti-CSRF tokens"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/cors", []ID{"V13.2.1"}, EvidenceCapability,
		"CORS / OPTIONS handling"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/idempotency", []ID{"V11.1.2"}, EvidenceCapability,
		"Idempotency-key deduplication"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/maxbody", []ID{"V13.4.1"}, EvidenceCapability,
		"Request-body size limit"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit", []ID{"V2.2.1", "V11.1.1"}, EvidenceCapability,
		"Per-IP rate limiting"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/secheaders", []ID{"V9.2.1", "V14.4.1"}, EvidenceCapability,
		"X-Content-Type-Options, HSTS, X-Frame-Options headers"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/cspnonce", []ID{"V9.2.1", "V14.4.1"}, EvidenceCapability,
		"CSP nonce per request"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/signedrequest", []ID{"V13.1.1", "V13.2.3", "V11.1.2"}, EvidenceCapability,
		"HMAC-signed inter-service requests with nonce store"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/tenant", []ID{"V4.1.1"}, EvidenceCapability,
		"Per-tenant scoping"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/approval", []ID{"V4.2.1", "V13.4.1"}, EvidenceCapability,
		"Approval workflow for state-changing ops"},
	{"github.com/bds421/rho-kit/httpx/v2/middleware/recover", []ID{"V7.1.1", "V14.4.1"}, EvidenceCapability,
		"Panic recovery + structured logging"},

	// Builder — importing app proves the service can use the canonical
	// Builder API. It is still capability-level evidence here because
	// an import scan cannot prove the startup path actually calls
	// Builder.Run or Builder.Validate.
	{"github.com/bds421/rho-kit/app/v2", []ID{"V14.1.1", "V14.4.1", "V9.1.1"}, EvidenceCapability,
		"Builder API with production-safety validator (TLS / loopback / JWT audience required when used)"},
}

// ImportClaim is one resolved entry from a directory scan: the import
// path the source file used, the package's registry entry, and the
// file/line where the import appeared.
type ImportClaim struct {
	Claim PackageClaim
	File  string
	Line  int
}

// ImportReport is the import-based companion to [ScanReport]. It is
// the package-capability view of a service's ASVS posture: every entry
// is derived from an actual non-blank import statement and resolved
// against the kit's [PackageRegistry], not from comments the service
// author may have copied.
type ImportReport struct {
	// Imports lists every kit-namespace import found, with its claim.
	Imports []ImportClaim
	// Claimed is the deduplicated set of control IDs across all
	// resolved imports.
	Claimed []ID
	// Missing is the catalog ⧹ Claimed difference.
	Missing []ID
	// EvidenceByControl maps each claimed control to the strongest
	// [Evidence] class observed across the importing packages.
	// "Strongest" is ranked Runtime > Builder > Capability.
	EvidenceByControl map[ID]Evidence
	// SkippedFiles lists .go paths that could not be parsed (syntax
	// errors under the kit-doctor toolchain). Empty when every file
	// parsed cleanly; operators should treat non-empty as "report may
	// understate import evidence".
	SkippedFiles []string
}

// EvidenceEntry is one (control, evidence-class) pair from
// [ImportReport.EvidenceSummary].
type EvidenceEntry struct {
	ID       ID
	Evidence Evidence
}

// ScanImports walks root looking for kit-namespace imports in .go
// files (skipping vendor, hidden dirs, and _test.go files), resolves
// each against [PackageRegistry], and returns an [ImportReport].
//
// Audit FR-007: this is the import-derived scanner. Its claims rest on
// "the service's source code does a non-blank import of `kit/...`" —
// stronger than a comment but still separated by [Evidence] class so
// package availability is not mistaken for runtime verification.
func ScanImports(root string) (ImportReport, error) {
	registry := buildRegistryIndex()

	var imports []ImportClaim
	var skipped []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return asvsFileError("inspect source tree", walkErr)
		}
		if d.IsDir() {
			if filepath.Clean(path) == filepath.Clean(root) {
				return nil
			}
			name := d.Name()
			if name == "vendor" || strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileImports, skip, err := scanFileImports(path, registry)
		if err != nil {
			return err
		}
		if skip {
			skipped = append(skipped, path)
		}
		imports = append(imports, fileImports...)
		return nil
	})
	if err != nil {
		return ImportReport{}, asvsFileError("walk source tree", err)
	}
	return buildImportReport(imports, skipped), nil
}

// PackageRegistry returns a detached copy of the kit's hand-maintained import
// evidence registry.
func PackageRegistry() []PackageClaim {
	out := make([]PackageClaim, len(packageRegistry))
	for i, claim := range packageRegistry {
		out[i] = clonePackageClaim(claim)
	}
	return out
}

func clonePackageClaim(claim PackageClaim) PackageClaim {
	claim.Controls = append([]ID(nil), claim.Controls...)
	return claim
}

func buildRegistryIndex() map[string]PackageClaim {
	out := make(map[string]PackageClaim, len(packageRegistry))
	for _, c := range packageRegistry {
		out[c.ImportPath] = clonePackageClaim(c)
	}
	return out
}

func scanFileImports(path string, registry map[string]PackageClaim) (claims []ImportClaim, skipped bool, err error) {
	fset := token.NewFileSet()
	// ImportsOnly avoids the cost of parsing function bodies.
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		// A parse error in user source is not a kit-doctor failure —
		// record the skip so operators know evidence may be understated.
		return nil, true, nil
	}
	var out []ImportClaim
	for _, imp := range file.Imports {
		if imp.Name != nil && imp.Name.Name == "_" {
			continue
		}
		// imp.Path.Value is the literal "github.com/..." with quotes.
		raw := strings.Trim(imp.Path.Value, `"`)
		claim, ok := registry[raw]
		if !ok {
			continue
		}
		pos := fset.Position(imp.Pos())
		out = append(out, ImportClaim{
			Claim: claim,
			File:  path,
			Line:  pos.Line,
		})
	}
	return out, false, nil
}

func buildImportReport(imports []ImportClaim, skipped []string) ImportReport {
	claimedSet := map[ID]struct{}{}
	evidence := map[ID]Evidence{}
	for _, ic := range imports {
		for _, id := range ic.Claim.Controls {
			claimedSet[id] = struct{}{}
			evidence[id] = strongerEvidence(evidence[id], ic.Claim.Evidence)
		}
	}
	missing := map[ID]struct{}{}
	for _, c := range catalog {
		if _, ok := claimedSet[c.ID]; !ok {
			missing[c.ID] = struct{}{}
		}
	}
	return ImportReport{
		Imports:           imports,
		Claimed:           sortedIDs(claimedSet),
		Missing:           sortedIDs(missing),
		EvidenceByControl: evidence,
		SkippedFiles:      append([]string(nil), skipped...),
	}
}

// strongerEvidence returns the higher-confidence of two Evidence
// values. The ranking is Runtime > Builder > Capability — runtime
// verification trumps builder enforcement, which trumps mere
// availability.
func strongerEvidence(a, b Evidence) Evidence {
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

func rank(e Evidence) int {
	switch e {
	case EvidenceRuntimeVerified:
		return 3
	case EvidenceBuilderEnforced:
		return 2
	case EvidenceCapability:
		return 1
	default:
		return 0
	}
}

// EvidenceSummary returns a sorted list of (ID, Evidence) pairs from
// an [ImportReport]. kit-doctor uses this for stable rendering.
func (r ImportReport) EvidenceSummary() []EvidenceEntry {
	out := make([]EvidenceEntry, 0, len(r.EvidenceByControl))
	for id, ev := range r.EvidenceByControl {
		out = append(out, EvidenceEntry{ID: id, Evidence: ev})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
