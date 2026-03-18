.PHONY: lint vulncheck test test-race test-cover bench build tidy fmt vet clean help ci

GOLANGCI_LINT_VERSION := v2.10.1
COVERAGE_FILE        := coverage.out

# Extract workspace submodules from go.work, excluding the root module (".").
WORKSPACE_MODULES := $(shell sed -n '/^use (/,/^)/{ s/^[[:space:]]*\.\/\(.*\)/\1/p; }' go.work | grep -v '^\.')

## help: Show this help message
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s ':'

## lint: Run golangci-lint (all workspace modules, parallel)
lint:
	@echo "$(WORKSPACE_MODULES)" | tr ' ' '\n' | xargs -P$$(nproc 2>/dev/null || sysctl -n hw.ncpu) -I{} sh -c \
		'echo "==> Linting {}" && cd {} && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --timeout=5m' \
		|| exit 1

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

## ci: Run the full CI pipeline locally (lint + test + build)
ci: lint test-race build
