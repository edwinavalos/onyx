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

.PHONY: sign
sign: build ## Ad-hoc sign ./bin/onyx with the virtualization entitlement (needed to launch VMs)
	codesign --force --sign - --entitlements onyx.entitlements $(BIN_DIR)/$(BINARY)
	ln -sf $(BINARY) $(BIN_DIR)/ossh && ln -sf $(BINARY) $(BIN_DIR)/oclaude && ln -sf $(BINARY) $(BIN_DIR)/ocodex && ln -sf $(BINARY) $(BIN_DIR)/opi

.PHONY: image
image: ## Build the Alpine guest image into images/out (needs Docker)
	./images/build-alpine.sh

.PHONY: guest-swap
guest-swap: ## Cross-compile onyx-guest and swap it into a running VM (VM=name); no image rebuild
	@test -n "$(VM)" || { echo "usage: make guest-swap VM=<name>"; exit 2; }
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -ldflags '-s -w' -o $(BIN_DIR)/onyx-guest ./cmd/onyx-guest
	$(BIN_DIR)/$(BINARY) cp $(BIN_DIR)/onyx-guest $(VM):/tmp
	$(BIN_DIR)/$(BINARY) vm exec $(VM) -- sh -c 'mv /tmp/onyx-guest /usr/local/bin/onyx-guest.new && chown root:root /usr/local/bin/onyx-guest.new && mv /usr/local/bin/onyx-guest.new /usr/local/bin/onyx-guest && (sleep 1; rc-service onyx-guest restart) >/dev/null 2>&1 </dev/null &'
	@sleep 3; $(BIN_DIR)/$(BINARY) vm exec $(VM) -- sh -c 'echo "guest agent pid $$(cat /run/onyx-guest.pid), binary $$(stat -c %y /usr/local/bin/onyx-guest)"'
	# The proxy bridges lived in the old agent process: deliver the VM's packs again.
	@packs=$$($(BIN_DIR)/$(BINARY) vm status $(VM) | sed -n '/"packs"/,/\]/p' | grep -o '"[^"]*"' | grep -v packs | tr -d '"'); \
	if [ -n "$$packs" ]; then $(BIN_DIR)/$(BINARY) pack deliver $(VM) $$packs && echo "packs redelivered: $$packs"; fi

.PHONY: spike
spike: sign ## Boot the spike VM non-interactively and exercise the guest agent
	$(BIN_DIR)/$(BINARY) spike -console-log $(CURDIR)/spike-console.log

## ---- macOS app ----------------------------------------------------------

APP_DIR    := $(CURDIR)/app
APP_BUNDLE := $(DIST_DIR)/Onyx.app

.PHONY: app
app: sign app-icon ## Build the SwiftUI app into dist/Onyx.app (bundles ./bin/onyx as the core)
	cd $(APP_DIR) && swift build -c release
	rm -rf $(APP_BUNDLE)
	mkdir -p $(APP_BUNDLE)/Contents/MacOS $(APP_BUNDLE)/Contents/Resources
	cp $(APP_DIR)/Resources/Info.plist $(APP_BUNDLE)/Contents/
	cp $(APP_DIR)/.build/AppIcon.icns $(APP_BUNDLE)/Contents/Resources/
	cp $(APP_DIR)/.build/release/Onyx $(APP_BUNDLE)/Contents/MacOS/Onyx
	# APFS is case-insensitive: the core cannot be "onyx" next to "Onyx".
	cp $(BIN_DIR)/$(BINARY) $(APP_BUNDLE)/Contents/MacOS/onyx-core
	codesign --force --sign - --entitlements onyx.entitlements $(APP_BUNDLE)/Contents/MacOS/onyx-core
	codesign --force --sign - --entitlements $(APP_DIR)/entitlements.plist $(APP_BUNDLE)
	@echo "built $(APP_BUNDLE)"

.PHONY: app-icon
app-icon: ## Render the app icon (black diamond on gray) into app/.build/AppIcon.icns
	rm -rf $(APP_DIR)/.build/AppIcon.iconset
	swift $(APP_DIR)/Resources/icon/draw-icon.swift $(APP_DIR)/.build/AppIcon.iconset
	iconutil -c icns $(APP_DIR)/.build/AppIcon.iconset -o $(APP_DIR)/.build/AppIcon.icns

.PHONY: app-build
app-build: ## Compile the SwiftUI app without bundling (CI)
	cd $(APP_DIR) && swift build -c release

.PHONY: app-test
app-test: ## Run the app's unit tests (models, pure helpers)
	cd $(APP_DIR) && swift test

.PHONY: app-run
app-run: app ## Build and (re)launch the app
	# A running instance would just be refocused (single-instance app).
	pkill -x Onyx || true; sleep 1
	open $(APP_BUNDLE)

.PHONY: app-clean
app-clean: ## Remove Swift build products
	rm -rf $(APP_DIR)/.build $(APP_BUNDLE)

.PHONY: mcp-install
mcp-install: sign ## Register the Onyx MCP server with Claude Code (user scope)
	claude mcp remove onyx -s user >/dev/null 2>&1 || true
	claude mcp add onyx -s user -- $(BIN_DIR)/$(BINARY) mcp
	@echo "registered: onyx → $(BIN_DIR)/$(BINARY) mcp"

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
install: ## Install onyx (plus ossh/oclaude/ocodex/opi shorthands) into GOPATH/bin
	$(GO) install -ldflags '$(LDFLAGS)' $(CMD)
	ln -sf $(BINARY) "$$($(GO) env GOPATH)/bin/ossh" && ln -sf $(BINARY) "$$($(GO) env GOPATH)/bin/oclaude" && ln -sf $(BINARY) "$$($(GO) env GOPATH)/bin/ocodex" && ln -sf $(BINARY) "$$($(GO) env GOPATH)/bin/opi"

.PHONY: clean
clean: ## Remove build artifacts (keeps installed tools)
	rm -rf $(DIST_DIR) $(BIN_DIR)/$(BINARY) coverage.out spike-console.log

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
vet: ## Run go vet (e2e tag too, so the harness compiles even when it is not run)
	$(GO) vet ./...
	$(GO) vet -tags e2e ./e2e/

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...
	$(GOLANGCI_LINT) run --build-tags e2e ./e2e/

.PHONY: staticcheck
staticcheck: $(STATICCHECK) ## Run staticcheck
	$(STATICCHECK) ./...

.PHONY: overlay-test
overlay-test: ## Host-side tests for the guest overlay scripts (needs node)
	./images/test-overlay.sh

.PHONY: test
test: ## Run tests with race detector
	$(GO) test -race -cover ./...

.PHONY: e2e
e2e: sign ## End-to-end: a real core on a temp state dir boots VMs from images/out (serial; not part of ci)
	$(GO) test -tags e2e -count=1 -p 1 -timeout 15m -v ./e2e/

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
	$(GOSEC) -quiet -exclude=G110,G301 ./...  # rationale in .golangci.yml

.PHONY: govulncheck
govulncheck: $(GOVULNCHECK) ## Check dependencies against the Go vulnerability DB
	$(GOVULNCHECK) ./...

## ---- Aggregates ---------------------------------------------------------

.PHONY: check
check: fmt-check vet lint staticcheck test overlay-test ## Run formatting, vet, lint, staticcheck, and tests

.PHONY: ci
ci: tidy-check check sec app-test ## Everything CI should run

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
