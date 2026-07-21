# Code review: CLI tools (stage 1 — unverified findings)

## Scope

- **Directories**: cmd/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 6 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 7 |
| **Total (deduplicated)** | **7** |

**Reviewer impressions:**

> This CLI-tools scope is exceptionally well-hardened against the security concerns in my lens. Every user-controlled input that reaches a filesystem path, module/import string, or generated shell/YAML is validated through strict allow-list regexes, and both kit-new and kit-migrate layer contained-path checks, symlink-ancestor/target rejection, and O_EXCL creation to defend against path-traversal, symlink, and overwrite attacks. There is no command injection (the lone exec call uses fixed arguments), no SQL/crypto surface, and no secret/PII leakage into output; the only notable item is the intentional, documented -insecure TLS bypass in kit-verify.

> The CLI tools are unusually careful for their genre: path handling in kit-new and kit-migrate is defense-in-depth (containment checks plus symlink-ancestor rejection plus O_EXCL creates), input to templates is validated against tight regexes so go.mod/import injection is prevented, and error/exit-code semantics are thoughtfully documented (kit-verify's four-state model, kit-doctor's severity floor). There is no goroutine, channel, or shared-state concurrency anywhere in scope, so the concurrency surface is limited to a couple of unsynchronized package globals that are safe only because scanning is sequential. The most substantive correctness risk is the auth-identity auto-fix, which can match and rewrite an unrelated local `Identity` type; the remaining issues are edge-case robustness gaps.

> This CLI family is high quality: input validation in kit-new (service name, module path, versions) and path/symlink containment in kit-new and kit-migrate are careful and well-documented, and the kit-doctor rule set is consistent, defensively narrow, and heavily commented. The most material issue is a wiring/documentation gap where kit-doctor's -interactive mode never applies AST-rule fixes despite advertising that it does, leaving a whole rule's Fix path dead. The remaining findings are lower-severity: a regex-based import scanner in kit-catalog that over-matches quoted paths, some dead/duplicated code, and a hidden global dependency in the rules package.

> This CLI-tools scope is unusually well-hardened for its security-sensitive surface: kit-new's scaffolder tightly validates ServiceName/ModulePath/RhoVersion/GoVersion against strict regexes before rendering them into go.mod and Go source, and enforces path containment plus symlink-ancestor rejection before every write; kit-migrate applies the same containment and symlink defenses with O_EXCL creates; and kit-verify floors TLS to 1.2, blocks redirects, and keeps probes pinned to the operator-supplied host, so there is no realistic template-injection, path-traversal, or SSRF exposure. The only issues I found are minor verification-completeness gaps in kit-verify's header assertions (presence-only X-Frame-Options and substring-based value checks), which weaken the assurance the tool reports rather than introduce an exploitable flaw. Overall code quality, error handling, and defensive posture are high.

> This CLI-tools scope is high quality for its lens: path handling is defensively coded (repeated containment + symlink-ancestor checks, O_EXCL creates that never overwrite user files), template inputs are tightly validated so template injection into go.mod/imports is well guarded, and errors are generally surfaced rather than swallowed. Real correctness/concurrency bugs are scarce — the code is single-threaded with no goroutines, all response bodies and files are closed, and check-then-act windows are backstopped by O_EXCL. The notable defects are the static option-detection asymmetry that can emit false Critical kit-doctor findings on valid code, and the rule engine's reliance on package-global mutable maps that would race if the scan were ever parallelized.

> The CLI family is generally high-quality: kit-new, kit-migrate, and the kit-doctor engine show careful, defense-in-depth path/symlink handling and thoughtful TOCTOU-resistant file creation, and the rule set is remarkably consistent (uniform alias resolution, test-file skipping, variadic-spread bail-outs, and suppression handling). The weakest spots are kit-catalog, which uses fragile regex scraping where its sibling uses go/parser (producing genuinely wrong manifests), a fixable-finding wiring gap in kit-doctor's interactive mode that orphans the only AST-rule Fix, and some hidden global state in the exported rules package. None of the issues are exploitable, and most are LOW-severity polish or robustness gaps.

## Findings

### [LOW] Package-global mutable scan state (`parents`, `packageCache`) is unsynchronized

- **Where**: `cmd/kit-doctor/rules/helpers.go:173`
- **Dimension**: concurrency
- **Detail**: The `parents` map is a package-level variable rebuilt by SetCurrentFile before each file's rules run, and exemptions.go's packageCache is a package-level map mutated during scanning. Both are read/written with no synchronization. This is safe today only because scan() (engine.go) processes files strictly sequentially in one goroutine. It is a latent hazard: any future change that parallelizes rule execution or file walking (a natural optimization for large trees) would introduce a data race and cross-file corruption of the parent map, since a rule for file A could observe parents built for file B. Flagging per the review lens on shared mutable map access; no bug fires under the current single-threaded caller.
- **Suggestion**: Thread the parent map and package cache through the Rule.Run call (e.g. via a per-scan context struct) instead of package globals, so the state is naturally per-scan and parallelization-safe.

### [LOW] Two components sharing an identical migration filename escape duplicate detection and fail mid-publish, leaving partial state

- **Where**: `cmd/kit-migrate/main.go:401`
- **Dimension**: error-handling
- **Detail**: checkDuplicateVersions only flags a collision when two selected migrations share a goose version prefix AND have different filenames (`ok && prev != filename`). When two components ship a file with the exact same name (same version prefix, same filename), no error is raised. buildPublishPlan then reads the on-disk target for each; since nothing has been written yet, both are added to plan.publish for the same target path. cmdPublish (lines 161-168) writes them in sequence via writeNewFile, which uses O_WRONLY|O_CREATE|O_EXCL — the first write succeeds, the second fails with 'already exists', returning exit 1 AFTER a file was already created. The result is a confusing error plus a partially-published directory. Migration filenames are normally timestamp- and component-specific so this is unlikely, but the guard is meant to prevent exactly this class of collision before writing.
- **Suggestion**: Treat an identical-version, identical-filename pair across distinct components as a collision too (report it), or de-duplicate identical targets in the plan and verify byte-equality before dropping the second.

### [LOW] kit-new leaves a partially-generated tree when a template fails mid-plan

- **Where**: `cmd/kit-new/scaffold.go:245`
- **Dimension**: error-handling
- **Detail**: scaffold() preflights every destination, then in the write loop creates parent directories and writes each file. If tmpl.Execute fails on file N (scaffold.go:245), it removes only that one file and returns an error; the output directory and the N-1 files/directories already written in earlier iterations are left on disk. main.go prints the error and exits 1, leaving the user with a half-scaffolded, non-compiling tree that a subsequent `kit-new` re-run into the same -dir will then reject as 'destination already exists'. Template execution failure is unlikely for the shipped templates but possible if a template references a field not on Params.
- **Suggestion**: Track created files/directories and roll them back on failure (or write into a temp dir and rename into place once every file succeeds), so a failed generation leaves no partial tree.

### [LOW] gateActive has an unreachable MCP case and the gate mechanism is applied inconsistently

- **Where**: `cmd/kit-new/scaffold.go:331`
- **Dimension**: smell
- **Detail**: gateActive handles "MCP" (scaffold.go:331-332), but no row in templateFile carries gate:"MCP" — only the Postgres rows are gated (lines 140-143). MCP and Tenant are instead handled purely inside templates via {{if .MCP}}/{{if .Tenant}}, while Postgres is gated both as a row filter and inside templates. The result is an inconsistent design (three feature toggles, one of which uses the gate list, two of which don't) plus a dead switch case that reads as if MCP-gated files exist.
- **Suggestion**: Either remove the MCP case (and rely on in-template {{if .MCP}} as with Tenant) or, if MCP-specific files are intended, add the corresponding gated rows; document why Postgres is the only file-gated feature.

### [LOW] -insecure disables TLS verification for all probes, including the TLS/security-header assertions

- **Where**: `cmd/kit-verify/main.go:165`
- **Dimension**: security
- **Detail**: newProbeHTTPClient sets tlsConfig.InsecureSkipVerify = insecureTLS for the single shared client used by every probe. When an operator runs `kit-verify -url=https://... -insecure`, certificate/hostname verification is skipped for all requests, so the tool will happily report PASS on security-header, readiness, JWT, CSRF and rate-limit probes against a MITM'd or misconfigured TLS endpoint. It is default-false and documented as dev-only (with a //nolint:gosec annotation), so this is an intentional opt-in rather than a bug, but because kit-verify's purpose is to attest a service's security posture, a run that passed under -insecure could be mistaken for a genuine compliance signal in CI logs.
- **Suggestion**: Emit a prominent warning line (text and a JSON field) whenever insecureTLS is active, e.g. a synthetic Result{Probe:"tls-verification",Status:UNKNOWN,Detail:"cert verification disabled via -insecure"}, so downstream consumers cannot silently treat an -insecure run as authoritative.

### [LOW] -timeout-ms is documented as per-probe but is enforced per-request

- **Where**: `cmd/kit-verify/main.go:213`
- **Dimension**: api-design
- **Detail**: The flag help calls -timeout-ms a 'per-probe timeout', and the exit-code/probe-status docs treat each probe as one unit, but the value is assigned to http.Client.Timeout (newProbeHTTPClient), which bounds a single request/response. The ratelimit-emits-retry-after probe issues up to 30 sequential requests (burst const, line 506-521), so with -timeout-ms=5000 that one probe can take up to ~150s of wall clock before yielding UNKNOWN. Operators sizing a CI timeout around the flag's stated meaning will be surprised.
- **Suggestion**: Either rename/redocument the flag as a per-request timeout, or enforce a genuine per-probe deadline (e.g. a context.WithTimeout around each probe's Run) so the 30-request burst honors the configured bound.

### [LOW] runAll is dead production code that duplicates default paths and can drift from the real entrypoint

- **Where**: `cmd/kit-verify/main.go:286`
- **Dimension**: smell
- **Detail**: runAll(hc, base) is only referenced from main_test.go (4 call sites); the production path in run() uses runAllWithConfig with cfg values parsed from flags. runAll re-hardcodes the probe defaults (/ready, /, /api/v1/whoami, /api/v1/state, /) that also live in parseConfig's flag defaults (lines 214-218). Two copies of the same defaults can silently diverge, and tests then exercise a code path that production never takes.
- **Suggestion**: Move runAll into the _test.go file (it is a test convenience wrapper), or have tests build a probeConfig and call runAllWithConfig so the default paths have a single source of truth.

