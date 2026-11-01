# Build and test automation for the gateway, which is the whole repository: the Go
# service lives at the root and the canvas frontend is a self-contained subtree.
#
# CGO is disabled and the output is statically linked. That is not a micro
# optimisation: the whole point of this gateway is a single self-contained binary,
# and a cgo build would reintroduce a libc dependency that breaks when the host is
# upgraded. Nothing in this module needs cgo, so the constraint costs us nothing.

GO      ?= go
BIN_DIR := bin
BIN     := $(BIN_DIR)/comfyui-gateway
PKG     := .

# The canvas is a separate npm project under canvas/web. `build` stays
# Go-only on purpose (one static binary), so the bundle has its own entry points:
# `web-deps` installs, `web` builds, `web-check` typechecks.
WEB_DIR   := canvas/web
NPM       ?= npm
WEB_STAMP := $(WEB_DIR)/node_modules/.package-lock.json
# Every Go package lives in main.go or under internal/. Listing them keeps the gates
# off the vendored frontend: `./...` would walk canvas/web/node_modules, and a
# stray .go file in a future npm package would then break `make check`.
PKGS    := . ./internal/...
GO_DIRS := main.go internal
GOFLAGS := -trimpath
LDFLAGS := -s -w

# Force a static, dependency-free binary for every target including tests.
export CGO_ENABLED := 0

.DEFAULT_GOAL := build
.PHONY: build run test test-race test-v vet fmt fmt-check lint check check-full verify-static size clean tidy web web-deps web-check web-clean help

build: ## Compile the gateway binary into bin/
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)
	@echo "built $(BIN)"

run: ## Run the gateway from source
	$(GO) run $(PKG)

web-deps: ## Install canvas dependencies when node_modules is missing or stale
	@if [ ! -f $(WEB_STAMP) ] || [ $(WEB_DIR)/package-lock.json -nt $(WEB_STAMP) ]; then \
		echo "installing canvas dependencies ($(WEB_DIR))"; \
		cd $(WEB_DIR) && $(NPM) ci || exit 1; \
	else \
		echo "canvas dependencies up to date"; \
	fi

web: ## Build the canvas bundle into canvas/web/dist
	@$(MAKE) --no-print-directory web-deps
	cd $(WEB_DIR) && $(NPM) run build
	@test -f $(WEB_DIR)/dist/index.html || { echo "ERROR: canvas build produced no dist/index.html"; exit 1; }
	@echo "built $(WEB_DIR)/dist"

web-check: ## Typecheck the canvas sources (tsc --noEmit)
	@$(MAKE) --no-print-directory web-deps
	cd $(WEB_DIR) && $(NPM) run typecheck

web-clean: ## Remove the canvas build output
	rm -rf $(WEB_DIR)/dist

test: ## Run the test suite
	$(GO) test $(PKGS)

# The race detector needs cgo, so this target re-enables it for the test binary
# only. The shipped gateway stays CGO_ENABLED=0 and fully static.
test-race: ## Run the test suite with the race detector (needs a C toolchain)
	CGO_ENABLED=1 $(GO) test -race $(PKGS)

test-v: ## Run the test suite verbosely
	$(GO) test -v $(PKGS)

vet: ## Run go vet
	$(GO) vet $(PKGS)

fmt: ## Format all Go sources
	gofmt -w $(GO_DIRS)

fmt-check: ## Fail if any file needs formatting
	@needs_format=$$(gofmt -l $(GO_DIRS)); \
	if [ -n "$$needs_format" ]; then \
		echo "gofmt required for:"; echo "$$needs_format"; exit 1; \
	fi

lint: fmt-check vet ## Check formatting and run vet

check: lint test ## The default gate: no C toolchain required

verify-static: build ## Assert the binary has no dynamic dependencies
	@if ldd $(BIN) >/dev/null 2>&1; then \
		echo "ERROR: $(BIN) is dynamically linked"; ldd $(BIN); exit 1; \
	else \
		echo "static: $(BIN) has no dynamic dependencies"; \
	fi
	@file $(BIN)

check-full: lint test-race ## The strict gate, including the race detector

size: build ## Report the binary size
	@ls -lh $(BIN)

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

tidy: ## Prune and verify go.mod
	$(GO) mod tidy

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'
