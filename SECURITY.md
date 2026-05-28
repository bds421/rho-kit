# Security Policy

We take security seriously and welcome reports of vulnerabilities in `rho-kit`.
The project follows **coordinated disclosure** for any valid report.

## Reporting a Vulnerability

- **Preferred and only channel:** Open a private vulnerability report via
  GitHub Private Security Advisories at
  https://github.com/bds421/rho-kit/security/advisories/new.
  GHSA gives us authenticated reporting, end-to-end encryption, and an audit
  trail; we do not maintain a separate disclosure mailbox.
- **Bug bounty:** none at present.

We acknowledge receipt within 24 hours and provide a triage decision within the
SLO window listed below.

## Response SLA

| Severity (CVSS / GHSA) | Time to patch | Time to release | Consumer notification |
|---|---|---|---|
| CRITICAL (9.0+) | 48 hours from disclosure to merge | 24 hours from merge to tagged release | Public advisory + GHSA watchers notified within 24h of release |
| HIGH (7.0–8.9) | 7 days from disclosure to merge | 7 days from merge to tagged release | Public advisory at release time |
| MEDIUM (4.0–6.9) | next planned release window (≤ 30 days) | ≤ 30 days | release notes |
| LOW (< 4.0) | rolled into the next Dependabot cycle | rolled into the next Dependabot cycle | release notes |

The clock starts when the first of these happens: a CVE/GHSA is filed against
an imported dependency, a private report is received, or `govulncheck` flags
the issue on `main`.

## Disclosure Policy

- Fix lands → public GitHub Security Advisory → details published.
- Reporter credited unless they request otherwise.
- Public details include affected versions, CVSS score, reproducer (where safe),
  mitigation steps, and the fixing commit.

## Further Reading

- [`docs/audit/THREAT_MODEL.md`](docs/audit/THREAT_MODEL.md) — kit-level
  threat model and known gaps.
- [`docs/audit/dependency-allowlist.txt`](docs/audit/dependency-allowlist.txt)
  — direct external Go dependency allowlist (enforced by
  `make check-dependency-allowlist`).
