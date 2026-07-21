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
| LOW | 0 |
| **Total (deduplicated)** | **0** |

**Reviewer impressions:**

> This CLI-tools scope is exceptionally well-hardened against the security concerns in my lens. Every user-controlled input that reaches a filesystem path, module/import string, or generated shell/YAML is validated through strict allow-list regexes, and both kit-new and kit-migrate layer contained-path checks, symlink-ancestor/target rejection, and O_EXCL creation to defend against path-traversal, symlink, and overwrite attacks. There is no command injection (the lone exec call uses fixed arguments), no SQL/crypto surface, and no secret/PII leakage into output; the only notable item is the intentional, documented -insecure TLS bypass in kit-verify.

> The CLI tools are unusually careful for their genre: path handling in kit-new and kit-migrate is defense-in-depth (containment checks plus symlink-ancestor rejection plus O_EXCL creates), input to templates is validated against tight regexes so go.mod/import injection is prevented, and error/exit-code semantics are thoughtfully documented (kit-verify's four-state model, kit-doctor's severity floor). There is no goroutine, channel, or shared-state concurrency anywhere in scope, so the concurrency surface is limited to a couple of unsynchronized package globals that are safe only because scanning is sequential. The most substantive correctness risk is the auth-identity auto-fix, which can match and rewrite an unrelated local `Identity` type; the remaining issues are edge-case robustness gaps.

> This CLI family is high quality: input validation in kit-new (service name, module path, versions) and path/symlink containment in kit-new and kit-migrate are careful and well-documented, and the kit-doctor rule set is consistent, defensively narrow, and heavily commented. The most material issue is a wiring/documentation gap where kit-doctor's -interactive mode never applies AST-rule fixes despite advertising that it does, leaving a whole rule's Fix path dead. The remaining findings are lower-severity: a regex-based import scanner in kit-catalog that over-matches quoted paths, some dead/duplicated code, and a hidden global dependency in the rules package.

> This CLI-tools scope is unusually well-hardened for its security-sensitive surface: kit-new's scaffolder tightly validates ServiceName/ModulePath/RhoVersion/GoVersion against strict regexes before rendering them into go.mod and Go source, and enforces path containment plus symlink-ancestor rejection before every write; kit-migrate applies the same containment and symlink defenses with O_EXCL creates; and kit-verify floors TLS to 1.2, blocks redirects, and keeps probes pinned to the operator-supplied host, so there is no realistic template-injection, path-traversal, or SSRF exposure. The only issues I found are minor verification-completeness gaps in kit-verify's header assertions (presence-only X-Frame-Options and substring-based value checks), which weaken the assurance the tool reports rather than introduce an exploitable flaw. Overall code quality, error handling, and defensive posture are high.

> This CLI-tools scope is high quality for its lens: path handling is defensively coded (repeated containment + symlink-ancestor checks, O_EXCL creates that never overwrite user files), template inputs are tightly validated so template injection into go.mod/imports is well guarded, and errors are generally surfaced rather than swallowed. Real correctness/concurrency bugs are scarce — the code is single-threaded with no goroutines, all response bodies and files are closed, and check-then-act windows are backstopped by O_EXCL. The notable defects are the static option-detection asymmetry that can emit false Critical kit-doctor findings on valid code, and the rule engine's reliance on package-global mutable maps that would race if the scan were ever parallelized.

> The CLI family is generally high-quality: kit-new, kit-migrate, and the kit-doctor engine show careful, defense-in-depth path/symlink handling and thoughtful TOCTOU-resistant file creation, and the rule set is remarkably consistent (uniform alias resolution, test-file skipping, variadic-spread bail-outs, and suppression handling). The weakest spots are kit-catalog, which uses fragile regex scraping where its sibling uses go/parser (producing genuinely wrong manifests), a fixable-finding wiring gap in kit-doctor's interactive mode that orphans the only AST-rule Fix, and some hidden global state in the exported rules package. None of the issues are exploitable, and most are LOW-severity polish or robustness gaps.

## Findings

