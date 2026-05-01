SHELL := /bin/bash

GO  ?= go
BUF ?= buf

# ----------------------------------------------------------------------------
# Code generation
# ----------------------------------------------------------------------------

.PHONY: proto
proto: ## Generate gRPC + gateway + OpenAPI from api/proto/v1/*.proto
	$(BUF) lint
	$(BUF) format --diff --exit-code
	$(BUF) generate
	$(GO) mod tidy

.PHONY: proto-fmt
proto-fmt: ## Auto-format proto files in place
	$(BUF) format -w

.PHONY: proto-breaking
proto-breaking: ## Check for breaking changes vs main
	$(BUF) breaking --against '.git#branch=main'

.PHONY: ent
ent: ## Regenerate ent client from internal/ent/schema/*.go
	$(GO) generate ./internal/ent/...
	$(GO) mod tidy

# ----------------------------------------------------------------------------
# Build / run
# ----------------------------------------------------------------------------

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w \
              -X main.version=$(VERSION) \
              -X main.commit=$(COMMIT) \
              -X main.date=$(BUILD_DATE)

.PHONY: build
build: ## Build the hue binary
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/hue ./cmd/hue

.PHONY: build-bench
build-bench: ## Build the benchmark tool
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/benchmark ./cmd/benchmark

.PHONY: run
run: ## Run hue with local dev settings
	HUE_ADDR=":8443" HUE_LOG_LEVEL=debug $(GO) run ./cmd/hue

# ----------------------------------------------------------------------------
# Quality gates
# ----------------------------------------------------------------------------

.PHONY: lint
lint: ## golangci-lint + go vet + gofmt
	golangci-lint run ./...
	$(GO) vet ./...
	test -z "$$(gofmt -l . | grep -v '^gen/')"

.PHONY: fmt
fmt: ## go fmt
	$(GO) fmt ./...

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: govulncheck
govulncheck: ## Vulnerability scan via govulncheck
	govulncheck ./...

# ----------------------------------------------------------------------------
# Tests
# ----------------------------------------------------------------------------

.PHONY: test
test: ## Unit tests with race detector + coverage
	$(GO) test -race -short -coverprofile=coverage.out ./...

.PHONY: test-integration
test-integration: ## Integration tests against testcontainers Postgres
	$(GO) test -race -tags=integration -coverprofile=coverage-integration.out ./internal/server/integration/...

.PHONY: test-e2e
test-e2e: ## End-to-end tests via docker compose
	docker compose -f deployments/docker/docker-compose.test.yml up --build --abort-on-container-exit --exit-code-from e2e

.PHONY: test-fuzz
test-fuzz: ## Short fuzz run (CI smoke); use FUZZTIME=4h for nightly
	@FUZZTIME=$${FUZZTIME:-20s}; \
	for target in $$($(GO) test -list '^Fuzz' ./... 2>/dev/null | grep '^Fuzz' | sort -u); do \
	  echo "==> fuzz $$target ($$FUZZTIME)"; \
	  $(GO) test -run='^$$' -fuzz="^$$target$$" -fuzztime=$$FUZZTIME ./... || exit 1; \
	done

.PHONY: test-load
test-load: ## k6 load test against the e2e stack
	k6 run test/load/k6.js

.PHONY: coverage
coverage: ## Render combined coverage report as HTML
	$(GO) test -race -coverprofile=coverage.out -coverpkg=./... ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

.PHONY: ci
ci: lint proto test test-integration test-e2e ## Run the full CI gate locally

# ----------------------------------------------------------------------------
# Tooling install (local dev)
# ----------------------------------------------------------------------------

.PHONY: tools
tools: ## Install developer tools to $$GOPATH/bin
	$(GO) install github.com/bufbuild/buf/cmd/buf@latest
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	$(GO) install entgo.io/ent/cmd/ent@latest
	$(GO) install github.com/air-verse/air@latest

# ----------------------------------------------------------------------------
# Misc
# ----------------------------------------------------------------------------

.PHONY: clean
clean:
	rm -rf bin/ gen/ coverage.out coverage*.out coverage.html

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'
