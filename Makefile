.PHONY: lint vulncheck test test-race test-integration test-cover bench build tidy fmt vet clean help ci release-plan check-publishable check-no-binaries check-dependency-allowlist check-dependency-boundaries

GOLANGCI_LINT_VERSION := v2.10.1
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
		(cd $$dir && go run golang.org/x/vuln/cmd/govulncheck@latest ./...) || exit 1; \
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

## bench: Run benchmarks (all workspace modules)
bench:
	@for dir in $(WORKSPACE_MODULES); do \
		echo "==> Benchmarking $$dir"; \
		(cd $$dir && go test -bench=. -benchmem ./...) || exit 1; \
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
ci: check-no-binaries check-dependency-allowlist check-dependency-boundaries check-publishable lint test-race build

## release-plan: Compute dependency-aware module release levels
release-plan:
	@RELEASE_VERSION=$(RELEASE_VERSION) RELEASE_BASE_REF=$(RELEASE_BASE_REF) RELEASE_MODE=$(RELEASE_MODE) RELEASE_FORMAT=$(RELEASE_FORMAT) RELEASE_GLOBAL_CHANGES=$(RELEASE_GLOBAL_CHANGES) bash tools/plan-module-release.sh

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
