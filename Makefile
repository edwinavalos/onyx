# Onyx build, lint, and security tooling.
#
# Tools are installed into ./bin with pinned versions so the build is
# reproducible and does not depend on globally installed binaries.

MODULE      := github.com/edwinavalos/onyx
BINARY      := onyx
CMD         := ./cmd/onyx
BIN_DIR     := $(CURDIR)/bin
DIST_DIR    := $(CURDIR)/dist

GO          ?= go
GOFLAGS     ?=
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Pinned tool versions.
GOLANGCI_LINT_VERSION := v2.6.2
GOSEC_VERSION         := v2.22.9
GOVULNCHECK_VERSION   := v1.1.4
STATICCHECK_VERSION   := 2025.1.1

GOLANGCI_LINT := $(BIN_DIR)/golangci-lint
GOSEC         := $(BIN_DIR)/gosec
GOVULNCHECK   := $(BIN_DIR)/govulncheck
STATICCHECK   := $(BIN_DIR)/staticcheck

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## ---- Build --------------------------------------------------------------

.PHONY: build
build: ## Build the onyx binary into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) $(CMD)

.PHONY: build-all
build-all: ## Cross-compile for darwin/linux (amd64, arm64) into ./dist
	@mkdir -p $(DIST_DIR)
	@for os in darwin linux; do \
		for arch in amd64 arm64; do \
			echo "building $$os/$$arch"; \
			GOOS=$$os GOARCH=$$arch $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
				-o $(DIST_DIR)/$(BINARY)-$$os-$$arch $(CMD) || exit 1; \
		done; \
	done

.PHONY: run
run: ## Run onyx from source
	$(GO) run $(CMD)

.PHONY: install
install: ## Install onyx into GOPATH/bin
	$(GO) install -ldflags '$(LDFLAGS)' $(CMD)

.PHONY: clean
clean: ## Remove build artifacts (keeps installed tools)
	rm -rf $(DIST_DIR) $(BIN_DIR)/$(BINARY) coverage.out

.PHONY: distclean
distclean: clean ## Remove build artifacts and installed tools
	rm -rf $(BIN_DIR)

## ---- Quality ------------------------------------------------------------

.PHONY: fmt
fmt: ## Format source with gofmt
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-formatted
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then \
		echo "files need gofmt:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: staticcheck
staticcheck: $(STATICCHECK) ## Run staticcheck
	$(STATICCHECK) ./...

.PHONY: test
test: ## Run tests with race detector
	$(GO) test -race -cover ./...

.PHONY: test-coverage
test-coverage: ## Run tests and write coverage.out
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: tidy
tidy: ## Tidy go.mod/go.sum
	$(GO) mod tidy

.PHONY: tidy-check
tidy-check: ## Fail if go mod tidy would change anything
	@cp go.mod go.mod.bak; [ -f go.sum ] && cp go.sum go.sum.bak || true
	@$(GO) mod tidy
	@if ! cmp -s go.mod go.mod.bak || { [ -f go.sum.bak ] && ! cmp -s go.sum go.sum.bak; }; then \
		echo "go.mod/go.sum not tidy"; mv go.mod.bak go.mod; [ -f go.sum.bak ] && mv go.sum.bak go.sum; exit 1; fi
	@rm -f go.mod.bak go.sum.bak

## ---- Security -----------------------------------------------------------

.PHONY: sec
sec: gosec govulncheck ## Run all security scans

.PHONY: gosec
gosec: $(GOSEC) ## Static security analysis with gosec
	$(GOSEC) -quiet ./...

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK) ## Check dependencies against the Go vulnerability DB
	$(GOVULNCHECK) ./...

## ---- Aggregates ---------------------------------------------------------

.PHONY: check
check: fmt-check vet lint staticcheck test ## Run formatting, vet, lint, staticcheck, and tests

.PHONY: ci
ci: tidy-check check sec ## Everything CI should run

## ---- Tools --------------------------------------------------------------

.PHONY: tools
tools: $(GOLANGCI_LINT) $(GOSEC) $(GOVULNCHECK) $(STATICCHECK) ## Install all pinned tools into ./bin

$(GOLANGCI_LINT):
	GOBIN=$(BIN_DIR) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOSEC):
	GOBIN=$(BIN_DIR) $(GO) install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)

$(GOVULNCHECK):
	GOBIN=$(BIN_DIR) $(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(STATICCHECK):
	GOBIN=$(BIN_DIR) $(GO) install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
