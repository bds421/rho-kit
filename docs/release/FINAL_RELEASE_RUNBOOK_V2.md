# rho-kit v2 Final Release Runbook

This runbook makes the future v2.0.0 release mechanical. It must not be run
during release preparation unless the release owner explicitly starts the
tagging and publishing phase.

Snippet status: shell blocks are executable from the repository root. Blocks in
the "Tag" and "Publish" sections are future release commands and must not be
run during preparation.

## Release Inputs

- Release version: `v2.0.0`
- Repository: `git@github.com:bds421/rho-kit.git`
- Release branch: `main`
- Go/toolchain baseline: `go 1.26.2`, `toolchain go1.26.2`
- Public API freeze: [API_FREEZE_V2.md](API_FREEZE_V2.md)
- Migration guide: [MIGRATION_V2.md](MIGRATION_V2.md)
- RC evidence checklist: [RC_CHECKLIST_V2.md](RC_CHECKLIST_V2.md)
- Benchmark baselines: [benchmarks/v2.0.0/MANIFEST.md](benchmarks/v2.0.0/MANIFEST.md)
- Tagging plan: [TAGGING_PLAN_V2.md](TAGGING_PLAN_V2.md)
- GitHub release notes body: [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md)

## Do Not Proceed If

- The release owner has not explicitly approved tagging and publishing.
- `git status --short` prints anything.
- The current branch is not `main`.
- `origin` is not `git@github.com:bds421/rho-kit.git`.
- Any planned `*/v2.0.0` module tag or `release/v2.0.0` already exists locally
  or remotely.
- `API_FREEZE_V2.md` does not cover every `go.work` module.
- Any internal `github.com/bds421/rho-kit/.../v2` require points at a version
  other than `v2.0.0`.
- Any local internal `replace` directive remains on the release branch after
  the replace-dropping preparation step.
- `make check-release-team` fails (the `@bds421/security` team is missing,
  has zero members, or branch protection on `main` does not require
  CODEOWNERS reviews — CODEOWNERS is decorative until both are true).
- `MIGRATION_V2.md` has not been revalidated against the current API.
- `docs/RELEASE_NOTES_v2.md` is not the exact body to publish.
- Docker is unavailable; integration tests must run for this release.
- Any RC gate fails.
- The only remaining motivation is broad hardening rather than a concrete
  release-candidate blocker.

## 1. Final Preflight

```bash
git status --short
git rev-parse --abbrev-ref HEAD
git remote get-url origin
git tag --list '*v2.0.0'
git ls-remote --tags origin '*v2.0.0'
make check-release-team
```

Expected output:

- `git status --short`: no output.
- Branch: `main`.
- Origin: `git@github.com:bds421/rho-kit.git`.
- Local tag list: no output.
- Remote tag list: no output.
- `make check-release-team`: `OK: team @bds421/security exists ... CODEOWNERS
  enforced on bds421/rho-kit@main.` This requires a `gh`-authenticated shell;
  see `tools/check-release-team.sh` for environment overrides.

## 2. Verify Artifact Coverage

Public API freeze must cover exactly the current workspace modules:

```bash
comm -23 \
  <(go list -m | sort) \
  <(rg -o 'github.com/bds421/rho-kit/[A-Za-z0-9_./-]+/v2' docs/release/API_FREEZE_V2.md | sort)

comm -13 \
  <(go list -m | sort) \
  <(rg -o 'github.com/bds421/rho-kit/[A-Za-z0-9_./-]+/v2' docs/release/API_FREEZE_V2.md | sort)
```

Expected output: both commands print nothing.

Every fenced markdown document must have snippet-status coverage:

```bash
for f in $(rg -l '```' docs README.md AGENTS.md CHANGELOG.md); do
  rg -q 'Snippet status|snippet status' "$f" || echo "$f"
done
```

Expected output: no output.

The migration guide must still name APIs that exist in the current tree:

```bash
rg -n 'func \(.*\) With(PASETO|NATS|Postgres|LeaderElection|SignedRequests|MultiTenant|TenantBudget|ActionLogger|ApprovalStore)' app
rg -n 'func (Catalog|PackageRegistry|HTTPLatencyBuckets|BatchDurationBuckets|DefaultPolicy|WorkerPolicy)\(' security observability resilience
rg -n 'func \(.*\) Run\(ctx context.Context\) error' security httpx infra data
rg -n 'func (HealthCheck|NonCriticalHealthCheck)\(' infra/redis
```

Expected output: at least one matching definition for every API named in the
migration guide.

## 3. Prepare Dependency-Ordered Release Branch

Before touching the real remote, run the local rehearsal from the release-prep
tree:

```bash
tools/rehearse-v2-release.sh
```

Expected output: `Rehearsal passed.` and a log path under
`docs/release/rehearsals/`. Do not continue to real tagging until this passes.

Drop local internal replaces once the release owner has entered the tagging
phase:

```bash
tools/drop-internal-replaces.sh
FORBID_INTERNAL_REPLACES=1 make check-publishable
```

Review and commit that change before creating any module tags. From this point,
normal workspace commands still use `go.work`; level-specific checksum updates
use `GOWORK=off go mod tidy` as described in
[TAGGING_PLAN_V2.md](TAGGING_PLAN_V2.md).

Compute the full dependency plan:

```bash
RELEASE_MODE=all make release-plan
RELEASE_MODE=all RELEASE_FORMAT=tsv make release-plan > /tmp/rho-kit-v2-plan.tsv
```

Expected output: all 67 modules are assigned to dependency levels. Modules in
the same level can be prepared and tagged together.

## 4. Run RC Gates

```bash
git diff --check
make test
make lint
make check-dependency-boundaries
make check-dependency-allowlist
make check-operational-readiness
FORBID_INTERNAL_REPLACES=1 EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
make vulncheck
make test-race
make bench
make bench-baseline
make test-integration
RELEASE_MODE=all make release-plan
```

Expected output summary:

- `git diff --check`: no output.
- `make check-dependency-boundaries`: OK; current evidence is `348 direct
  module edges checked`.
- `make check-dependency-allowlist`: OK; current evidence is `59 direct
  external deps approved`.
- `make check-operational-readiness`: OK; current evidence is `67 modules
  covered`.
- `FORBID_INTERNAL_REPLACES=1 EXPECTED_INTERNAL_VERSION=v2.0.0 make
  check-publishable`: OK for internal pins, internal require versions, no
  local internal replaces, and Go directives.
- `cmd/kit-doctor -strict=critical`: `null`.
- `make vulncheck`: `No vulnerabilities found.` for every module.
- `make test`, `make lint`, `make test-race`, `make bench`, and
  `make test-integration`: no `FAIL`; integration tests require Docker.
- `make bench-baseline`: refreshes `docs/release/benchmarks/v2.0.0/` on the
  release-candidate machine.
- `RELEASE_MODE=all make release-plan`: prints the dependency levels used for
  the release loop.

## 5. Prove Golden Path

```bash
go test ./examples/agentic-service/...
GOCACHE=/private/tmp/rho-kit-gocache go build ./examples/agentic-service/...
GOCACHE=/private/tmp/rho-kit-gocache go test ./cmd/kit-new/...
```

Expected output:

- `examples/agentic-service` tests pass.
- `examples/agentic-service` builds.
- `cmd/kit-new` scaffold tests pass.

Optional live smoke:

```bash
cd examples/agentic-service
export AGENTIC_SERVICE_DEMO_TOKEN="$(openssl rand -base64 32)"
go run ./cmd/agentic-service
```

In another shell:

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
  -H 'X-Tenant-Id: acme' \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'

curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
  -H 'X-Tenant-Id: acme' \
  -d '{"jsonrpc":"2.0","method":"echo","params":{"message":"hi"},"id":2}'

curl -s -H "Authorization: Bearer $AGENTIC_SERVICE_DEMO_TOKEN" \
  -H 'X-Tenant-Id: acme' \
  http://localhost:8080/admin/budget
```

Expected output includes:

```json
{"echoed":"hi"}
```

and:

```json
{"remaining":1000,"tenant":"acme"}
```

Stop the local service before tagging.

## 6. Tag

Run the dependency-ordered level loop from
[TAGGING_PLAN_V2.md](TAGGING_PLAN_V2.md). The future release must create 67
module-prefixed tags plus the coordination tag `release/v2.0.0`.

Do not tag all modules at one commit. Dependency modules must be tagged first;
dependent modules are tidied with `GOWORK=off`, committed with real internal
`go.sum` checksums, and then tagged at their later level commit.

Do not create a root `v2.0.0` tag as the Go module release signal.

## 7. Publish

After all module levels and the coordination tag are pushed and visible on
`origin`, verify module resolution from a clean downstream module. This is the
checksum proof for consumers.

```bash
set -euo pipefail

VERSION=v2.0.0
tmpdir="$(mktemp -d)"
cd "$tmpdir"
go mod init rho-kit-v2-verify

GOPRIVATE=github.com/bds421/* \
GONOSUMDB=github.com/bds421/* \
go get \
  github.com/bds421/rho-kit/app/v2@${VERSION} \
  github.com/bds421/rho-kit/httpx/v2@${VERSION} \
  github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2@${VERSION}

cat > main.go <<'EOF'
package main

import (
	_ "github.com/bds421/rho-kit/app/v2"
	_ "github.com/bds421/rho-kit/httpx/v2"
	_ "github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
)

func main() {}
EOF

go mod tidy
go list -deps ./... >/dev/null
go list -m all | rg 'github.com/bds421/rho-kit/.+/v2 v2\.0\.0'
rg 'github.com/bds421/rho-kit/.+ v2\.0\.0' go.sum
```

Expected output: selected modules and their internal rho-kit dependencies
resolve at `v2.0.0`, and the clean consumer `go.sum` contains internal module
checksums.

Then create a draft GitHub release using the release notes file as the exact
body:

```bash
VERSION=v2.0.0
gh release create "release/$VERSION" \
  --repo bds421/rho-kit \
  --title "rho-kit $VERSION" \
  --notes-file docs/RELEASE_NOTES_v2.md \
  --draft
```

Review the draft. Publish only after the module tags resolve from the clean
consumer check above. Spot-check direct module lookup as well:

```bash
VERSION=v2.0.0
go list -m github.com/bds421/rho-kit/app/v2@${VERSION}
go list -m github.com/bds421/rho-kit/httpx/v2@${VERSION}
go list -m github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2@${VERSION}
```

Expected output: each command prints the module path and `v2.0.0`.

Publish:

```bash
VERSION=v2.0.0
gh release edit "release/$VERSION" --repo bds421/rho-kit --draft=false
```

## 8. Rollback

Before a level's tags are pushed:

```bash
git tag -d $(cat /tmp/rho-kit-v2-level-tags.txt)
```

After tags are pushed but before consumers adopt them, rollback requires release
owner approval:

```bash
git push --delete origin $(cat /tmp/rho-kit-v2-level-tags.txt)
git tag -d $(cat /tmp/rho-kit-v2-level-tags.txt)
```

If a draft GitHub release exists and must be removed:

```bash
VERSION=v2.0.0
gh release delete "release/$VERSION" --repo bds421/rho-kit --yes
```

After public consumers may have resolved `v2.0.0`, do not delete or retag.
Cut a follow-up patch release instead.
