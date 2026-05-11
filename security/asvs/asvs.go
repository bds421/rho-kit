// Package asvs maps kit middleware and helpers to OWASP Application
// Security Verification Standard (ASVS) 5.0 controls. The mapping is
// the kit's documented security contract: each middleware annotates
// which controls it satisfies, and [Catalog] returns the source of truth
// kit-doctor scans to report a service's ASVS posture.
//
// Why ASVS: it's the OWASP-published standard for "what defensive
// controls a webapp needs," with stable IDs that survive across
// the v4 → v5 transitions. Saying "this kit is OWASP-safe" means
// nothing without a control list; ASVS gives us that list.
//
// How it's used:
//
//   - Each kit middleware/helper carries a Go-comment annotation:
//     `// asvs: V2.1.5, V3.4.1, V13.2.3`. These annotations are kit-
//     internal *documentation* — they are not, and must not be,
//     trusted as compliance evidence by themselves (audit FR-007).
//   - The kit also maintains a hand-curated [PackageRegistry] mapping
//     each kit import path to the controls it satisfies. kit-doctor's
//     [ScanImports] resolves a service's non-blank imports against
//     this registry to produce package-capability evidence — stronger
//     than comments, but still not runtime proof.
//   - Controls carry an [Evidence] class — Capability, BuilderEnforced,
//     or RuntimeVerified — so kit-doctor reports can distinguish
//     "kit ships the helper" from "Builder.Validate refuses startup
//     without it" from "kit-verify probed a running service".
//   - kit-verify (the runtime conformance tool) probes a running
//     service to verify the annotated controls actually behave as
//     claimed (e.g., that secheaders truly emits CSP).
//
// Annotated chapters covered in v2:
//   - V2 Authentication
//   - V3 Session Management
//   - V4 Access Control
//   - V5 Validation, Sanitization, Encoding
//   - V6 Stored Cryptography
//   - V7 Error Handling and Logging
//   - V8 Data Protection (in-flight handled by V9)
//   - V9 Communications
//   - V11 Business Logic
//   - V12 Files and Resources
//   - V13 API and Web Service
//   - V14 Configuration
package asvs

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

// ID is an ASVS control identifier in the canonical "V<chapter>.<section>.<requirement>"
// form (e.g. "V2.1.5"). String parsing is deliberately strict — the ID is
// a contract, not a free-form tag.
type ID string

// Control is the kit's record of an ASVS control: its ID, the
// chapter title, and a short description suitable for kit-doctor
// output. The full ASVS text lives in the OWASP repository — we ship
// the IDs and headings, not the full requirements.
type Control struct {
	ID          ID
	Chapter     string // e.g. "Authentication"
	Section     string // e.g. "Password Security"
	Description string // one-line summary fit for terminal output
}

// catalog is the kit's per-chapter index of controls referenced by
// any kit middleware/helper annotation. It is intentionally NOT
// exhaustive — it lists only controls the kit actually addresses.
// Adding a new annotation requires adding the matching entry here so
// kit-doctor can resolve the ID.
var catalog = []Control{
	// V2 Authentication
	{"V2.1.5", "Authentication", "Password Security", "Password helpers support minimum-entropy verification."},
	{"V2.2.1", "Authentication", "General Authentication", "Rate-limit helpers support anti-automation controls."},
	{"V2.3.1", "Authentication", "Authenticator Lifecycle", "JWT and PASETO helpers support credential/key rotation."},

	// V3 Session Management
	{"V3.2.1", "Session", "Session Binding", "Auth helpers support subject-bound session/token handling."},
	{"V3.3.1", "Session", "Session Termination", "Auth helpers support token revocation paths."},
	{"V3.4.1", "Session", "Cookie Security", "CSRF helpers support Secure, HttpOnly, SameSite cookies."},

	// V4 Access Control
	{"V4.1.1", "Access Control", "General Access Control", "Tenant and authorization helpers support handler-boundary checks."},
	{"V4.1.5", "Access Control", "General Access Control", "Authorization helpers support server-side decision logging."},
	{"V4.2.1", "Access Control", "Operation Authorization", "Approval workflow helpers support state-changing operation gates."},

	// V5 Validation, Sanitization, Encoding
	{"V5.1.3", "Validation", "Input Validation", "Validation helpers support schema checks before handler logic."},
	{"V5.2.5", "Validation", "Sanitization & Sandboxing", "URL helpers support rejecting credentials in URL userinfo."},
	{"V5.3.1", "Validation", "Output Encoding", "Problem-details helpers support RFC 7807 encoding."},

	// V6 Stored Cryptography
	{"V6.2.1", "Cryptography", "Algorithms", "Crypto helpers provide Argon2id password hashing and AES-GCM encryption."},
	{"V6.4.1", "Cryptography", "Key Management", "Envelope encryption helpers support KMS-backed KEKs."},

	// V7 Error Handling and Logging
	{"V7.1.1", "Logging", "Log Content", "Logging helpers support structured request, correlation, and tenant attributes."},
	{"V7.4.1", "Logging", "Log Protection", "Secret helpers support redaction in slog output."},

	// V8 Data Protection
	{"V8.2.2", "Data Protection", "Client-Side Data Protection", "Health handlers support no-store cache headers."},

	// V9 Communications
	{"V9.1.1", "Communications", "Server Communications", "Builder validation and Postgres helpers support TLS / sslmode enforcement."},
	{"V9.2.1", "Communications", "Server Communications", "Security-header middleware supports X-Content-Type-Options, X-Frame-Options, and HSTS."},

	// V11 Business Logic
	{"V11.1.1", "Business Logic", "Business Logic Security", "Budget helpers support per-tenant spend caps."},
	{"V11.1.2", "Business Logic", "Business Logic Security", "Idempotency helpers support retry deduplication."},

	// V12 Files and Resources
	{"V12.1.1", "Files & Resources", "File Upload", "Storage helpers support MIME sniffing and size-limited uploads."},
	{"V12.3.1", "Files & Resources", "File Storage", "Storage helpers support server-side object keys instead of raw filenames."},

	// V13 API and Web Service
	{"V13.1.1", "API", "Generic Web Service", "HTTP helpers support JSON Content-Type validation."},
	{"V13.2.1", "API", "RESTful Web Service", "CORS helpers support method and OPTIONS handling."},
	{"V13.2.3", "API", "RESTful Web Service", "CSRF and signed-request helpers support replay/cross-site request defenses."},
	{"V13.4.1", "API", "GraphQL & Web Service", "Middleware supports bounded request bodies."},

	// V14 Configuration
	{"V14.1.1", "Configuration", "Build & Deploy", "Builder production-safety validator checks deployment defaults."},
	{"V14.4.1", "Configuration", "HTTP Security Headers", "Default stack can install security headers and recovery middleware."},
}

// Catalog returns a detached copy of the kit's per-chapter index of controls
// referenced by kit middleware/helper annotations.
func Catalog() []Control {
	out := make([]Control, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup returns the catalog entry for id, or an error when the ID
// is unknown. kit-doctor calls this when surfacing an annotation;
// unknown IDs indicate a typo in the source annotation.
func Lookup(id ID) (Control, error) {
	for _, c := range catalog {
		if c.ID == id {
			return c, nil
		}
	}
	return Control{}, errors.New("asvs: unknown control (add to security/asvs/Catalog)")
}

// IDs returns the catalog's control IDs sorted lexically. Used by
// kit-doctor to render a stable column ordering.
func IDs() []ID {
	out := make([]ID, 0, len(catalog))
	for _, c := range catalog {
		out = append(out, c.ID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// idPattern matches a strict ASVS ID form: "V" + chapter +
// "." + section + "." + requirement. Anchored so trailing
// punctuation (like a closing quote, paren, or backtick) is
// rejected — the source-of-truth is whichever annotation line
// formats clean, and a fuzzy parser would silently accept docstring
// text that happens to mention "asvs:".
var idPattern = regexp.MustCompile(`^V\d+(?:\.\d+){1,2}$`)

// ParseAnnotation extracts ASVS IDs from a "// asvs: V2.1.5, V3.4.1"
// comment line. Returns nil for non-annotation lines or when the
// extracted text contains no valid IDs.
//
// Strict matching rules — relaxing any of these caused false
// positives in v2-asvs-coverage:
//
//   - The line MUST start with "//" or "/*" (possibly indented).
//     Lines containing "asvs:" inside string literals are rejected.
//   - Each comma-separated token MUST match [idPattern]. Tokens with
//     trailing punctuation (quotes, parens, backticks) are silently
//     dropped — the kit's documentation references like
//     `// asvs: V2.1.5` work, but plain English mentions of "asvs:"
//     in package docstrings won't be misparsed as annotations.
func ParseAnnotation(line string) []ID {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "//") && !strings.HasPrefix(trimmed, "/*") {
		return nil
	}

	const marker = "asvs:"
	idx := strings.Index(trimmed, marker)
	if idx < 0 {
		return nil
	}
	rest := strings.TrimSpace(trimmed[idx+len(marker):])
	rest = strings.TrimSuffix(rest, "*/")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil
	}

	parts := strings.Split(rest, ",")
	out := make([]ID, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !idPattern.MatchString(p) {
			continue
		}
		out = append(out, ID(p))
	}
	return out
}
