.PHONY: lint vulncheck test test-race test-integration test-cover build tidy fmt vet clean help ci release-candidate kit-doctor release-plan release-bin release-bin-all check-dashboards check-publishable check-no-binaries check-dependency-allowlist check-dependency-boundaries check-licenses check-operational-readiness check-api-freeze-coverage check-dashboard-metrics check-dashboard-labels check-fmt-errorf-wrap check-doc-rot bench check-bench-regression update-bench-baseline

GOLANGCI_LINT_VERSION := v2.10.1
GOVULNCHECK_VERSION  ?= v1.1.4
COVERAGE_FILE        := coverage.out
RELEASE_VERSION      ?= v2.0.0
RELEASE_BASE_REF     ?= HEAD~1
RELEASE_MODE         ?= changed
RELEASE_FORMAT       ?= text
RELEASE_GLOBAL_CHANGES ?= none

# Extract workspace submodules from go.work, excluding the root module (".").
WORKSPACE_MODULES := $(shell sed -n '/^use (/,/^)/{ s/^[[:space:]]*\.\/\(.*\)/\1/p; }' go.work | grep -v '^\.')

## help: Show this help message
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'

## lint: Run golangci-lint sequentially across workspace modules
# Sequential because golangci-lint v2 uses a shared cache lock that collides
# with parallel invocations across modules in the same workspace.
lint:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Linting $$dir"; \
		(cd $$dir && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --timeout=5m) || exit 1; \
	done

## vulncheck: Run Go vulnerability analysis (all workspace modules)
vulncheck:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Vulncheck $$dir"; \
		(cd $$dir && go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...) || exit 1; \
	done

## test: Run all tests (all workspace modules)
test:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Testing $$dir"; \
		(cd $$dir && go test ./...) || exit 1; \
	done

## test-race: Run tests with race detector (all workspace modules)
test-race:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Testing $$dir (race)"; \
		(cd $$dir && go test -race ./...) || exit 1; \
	done

## test-integration: Run Docker-backed integration tests (all workspace modules)
test-integration:
	@docker info >/dev/null 2>&1 || { echo "Docker is required for integration tests"; exit 1; }
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Integration testing $$dir"; \
		(cd $$dir && go test -tags integration ./...) || exit 1; \
	done

## test-cover: Run tests with coverage report (all workspace modules)
test-cover:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Coverage $$dir"; \
		(cd $$dir && go test -race -coverprofile=coverage.out ./...) || exit 1; \
	done

## build: Build all packages (all workspace modules)
build:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Building $$dir"; \
		(cd $$dir && go build ./...) || exit 1; \
	done

## vet: Run go vet (all workspace modules)
vet:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Vetting $$dir"; \
		(cd $$dir && go vet ./...) || exit 1; \
	done

## fmt: Format code
fmt:
	gofmt -s -w .

## tidy: Tidy and verify module dependencies (all workspace modules)
tidy:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Tidying $$dir"; \
		(cd $$dir && go mod tidy && go mod verify) || exit 1; \
	done

## clean: Remove build artifacts
clean:
	rm -f $(COVERAGE_FILE)
	go clean -cache -testcache

## ci: Run the full CI pipeline locally (lint + test + build + supply-chain checks)
ci: check-no-binaries check-dependency-allowlist check-dependency-boundaries check-publishable check-dashboards check-dashboard-metrics check-dashboard-labels check-operational-readiness check-api-freeze-coverage check-doc-rot lint test-race build

## kit-doctor: Run strict critical kit-doctor checks against this repository
kit-doctor:
	@go run ./cmd/kit-doctor -format=json -strict=critical .

## release-candidate: Run the full pre-release quality gate
release-candidate: ci vulncheck test-integration test-cover kit-doctor

## release-plan: Compute dependency-aware module release levels
release-plan:
	@RELEASE_VERSION=$(RELEASE_VERSION) RELEASE_BASE_REF=$(RELEASE_BASE_REF) RELEASE_MODE=$(RELEASE_MODE) RELEASE_FORMAT=$(RELEASE_FORMAT) RELEASE_GLOBAL_CHANGES=$(RELEASE_GLOBAL_CHANGES) bash tools/plan-module-release.sh

## check-dashboards: Validate Grafana dashboard JSON and Prometheus rule YAML.
check-dashboards:
	@command -v promtool >/dev/null 2>&1 || { echo "promtool is required; install Prometheus first"; exit 1; }
	@for f in observability/dashboards/grafana/*.json; do \
		python3 -m json.tool "$$f" >/dev/null || exit 1; \
	done
	@promtool check rules observability/dashboards/prometheus/*.yaml

## check-publishable: Static pre-tag gate for internal module pins, replaces, and Go directives
check-publishable:
	@bash tools/check-publishable.sh

## check-no-binaries: Reject tracked binary artifacts outside fixture dirs.
# Audit FR-001: prevents Mach-O / ELF / Windows PE executables and >1MB binary
# blobs from being committed unless under explicitly allow-listed test fixture
# directories. Run as part of `make ci` and pre-commit.
check-no-binaries:
	@bash tools/check-no-binaries.sh

## check-dependency-allowlist: Reject unreviewed direct external Go dependencies.
check-dependency-allowlist:
	@bash tools/check-direct-dependency-allowlist.sh

## check-dependency-boundaries: Keep heavy optional SDKs in adapter/test modules.
check-dependency-boundaries:
	@bash tools/check-heavy-dependency-boundaries.sh

## check-fmt-errorf-wrap: Flag fmt.Errorf("...: %w", err) in data/ + infra/ — use redact.WrapError.
# Not yet a CI gate; the kit-wide sweep is staged as a follow-up wave.
# Wave 136 swept the cache/lock/stream/budget/idempotency/queue packages.
check-fmt-errorf-wrap:
	@bash tools/check-fmt-errorf-wrap.sh

## check-licenses: Reject transitive deps whose license is outside the SUPPLY_CHAIN.md §8.1 allowlist.
check-licenses:
	@bash tools/check-licenses.sh

## check-operational-readiness: Verify the v2 operational-readiness review covers every module
check-operational-readiness:
	@bash tools/check-operational-readiness.sh

## check-api-freeze-coverage: Verify the v2 API-freeze decision matrix has a row for every workspace module
check-api-freeze-coverage:
	@bash tools/check-api-freeze-coverage.sh

## check-dashboard-metrics: Verify every dashboard/rule metric name is emitted by Go code (catches metric-rename drift).
check-dashboard-metrics:
	@bash tools/check-dashboard-metrics.sh

## check-dashboard-labels: Verify every dashboard label selector references a label declared by the metric's Go NewXxxVec call.
check-dashboard-labels:
	@bash tools/check-dashboard-labels.sh

## check-doc-rot: Validate every "wave N" reference in docs/ has a matching commit; flag unanchored "future wave" claims.
check-doc-rot:
	@bash tools/check-doc-rot.sh

## bench: Run all kit hot-path benchmarks (used by check-bench-regression).
bench:
	@go test -run '^$$' -bench '.' -benchmem -benchtime 1s \
		./core/redact/... ./observability/promutil/... ./httpx/websocket/...

## check-bench-regression: Compare hot-path benchmarks to the checked-in baseline.
check-bench-regression:
	@bash tools/check-bench-regression.sh

## update-bench-baseline: Re-measure and overwrite tools/check-bench-regression/benchmarks-baseline.txt.
update-bench-baseline:
	@bash tools/check-bench-regression.sh -update

## check-release-team: Verify the @bds421/security team and branch protection exist before tagging.
check-release-team:
	@bash tools/check-release-team.sh

## release-bin: Build a single cmd binary with reproducibility flags (BIN=<name>).
# Produces dist/cmd/$(BIN)/$(BIN) using `-trimpath`, stripped symbol/debug tables,
# zeroed Go build-id, CGO disabled, and SOURCE_DATE_EPOCH pinned to the HEAD
# commit time (%ct — Unix seconds, timezone-agnostic). Two builds from the same
# commit produce byte-identical artefacts; verify with
# `tools/verify-reproducible-build.sh`.
BIN ?=
release-bin:
	@if [ -z "$(BIN)" ]; then echo "BIN= is required, e.g. make release-bin BIN=kit-doctor"; exit 1; fi
	@if [ ! -d "cmd/$(BIN)" ]; then echo "cmd/$(BIN) does not exist"; exit 1; fi
	@mkdir -p dist/cmd/$(BIN)
	@cd cmd/$(BIN) && \
		SOURCE_DATE_EPOCH=$$(git log -1 --format=%ct) \
		CGO_ENABLED=0 \
		go build \
			-trimpath \
			-ldflags="-s -w -buildid= -X main.commit=$$(git rev-parse HEAD) -X main.date=$$(git log -1 --format=%cI)" \
			-o ../../dist/cmd/$(BIN)/$(BIN) \
			.
	@echo "built dist/cmd/$(BIN)/$(BIN)"
	@sha256sum dist/cmd/$(BIN)/$(BIN) 2>/dev/null || shasum -a 256 dist/cmd/$(BIN)/$(BIN)

## release-bin-all: Build every cmd/<name> binary with reproducibility flags.
release-bin-all:
	@for d in cmd/*/; do \
		name=$$(basename $$d); \
		echo "==> Building $$name"; \
		$(MAKE) release-bin BIN=$$name || exit 1; \
	done
