# JanusFS Makefile.
#
# Every target is prefixed with a short "##" comment; the default `help` target
# parses this file to print them. Add new targets with a `## <description>`
# comment on the same line as `.PHONY: <name>` so they show up automatically.

MODULE  := github.com/sarathsp06/janusfs
GOOS    := darwin
ARCH    := $(shell go env GOARCH)
BIN_DIR := build
BIN     := $(BIN_DIR)/janusfs-$(GOOS)-$(ARCH)

# The `build/` directory has the same name as the `build` .PHONY target, so
# make it an order-only prerequisite via a distinct alias rather than
# depending on it directly (which triggers "circular dependency" warnings).

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Injected into cmd/janusfs at build time via -ldflags so `janusfs --version`
# and the dashboard show a real version, not "dev".
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

# GO ?= go lets a caller override with a specific toolchain, e.g. GO=go1.26rc2.
GO ?= go

.DEFAULT_GOAL := help

## --- meta ---------------------------------------------------------------

.PHONY: help ## Show this help (default target)
help:
	@awk 'BEGIN { \
	  FS = "[:#]"; \
	  printf "\nJanusFS build targets\n=====================\n\n"; \
	} \
	/^\.PHONY: [a-zA-Z0-9_-]+ ##/ { \
	  target = $$2; \
	  sub(/^ */, "", target); \
	  desc = $$0; \
	  sub(/^.*## /, "", desc); \
	  printf "  \033[36m%-22s\033[0m %s\n", target, desc; \
	}' $(MAKEFILE_LIST)
	@echo ""

## --- build --------------------------------------------------------------

.PHONY: build ## Build the janusfs binary into build/ for the host arch
build: | $(BIN_DIR)/.keep
	GOOS=$(GOOS) $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/janusfs

$(BIN_DIR)/.keep:
	@mkdir -p $(BIN_DIR) && touch $@

.PHONY: install ## Install janusfs into $$GOPATH/bin (or $$GOBIN)
install:
	$(GO) install -ldflags="$(LDFLAGS)" ./cmd/janusfs

.PHONY: run ## Build then run: make run ARGS="mount <src> <mnt>"
run: build
	$(BIN) $(ARGS)

## --- test ---------------------------------------------------------------

.PHONY: test ## Run unit tests
test:
	$(GO) test ./...

.PHONY: test-race ## Run unit tests with the race detector
test-race:
	$(GO) test -race ./...

.PHONY: test-short ## Run only short tests (skip conformance-vs-git etc.)
test-short:
	$(GO) test -short ./...

.PHONY: coverage ## Run tests with coverage; writes coverage.out + coverage.html
coverage:
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo ""
	@echo "Per-package coverage:"
	@$(GO) tool cover -func=coverage.out | tail -20
	@echo ""
	@echo "Full report: coverage.html"

.PHONY: integration ## Run FUSE-T mounted integration tests (needs FUSE-T)
integration:
	$(GO) test -tags fuseintegration ./...

.PHONY: leak-oracle ## Run the leak-oracle sentinel scan through a real mount
leak-oracle:
	$(GO) test -tags fuseintegration -run TestLeakOracle ./...

.PHONY: bench ## Run benchmarks and compare against bench/BASELINE.md
bench:
	$(GO) test -run '^$$' -bench . -benchmem ./bench/...

## --- quality ------------------------------------------------------------

.PHONY: fmt ## Format code (gofmt + goimports)
fmt:
	gofmt -w .
	@command -v goimports >/dev/null 2>&1 && goimports -local $(MODULE) -w . || \
	  echo "goimports not installed; skipping import ordering (go install golang.org/x/tools/cmd/goimports@latest)"

.PHONY: fmt-check ## Check gofmt cleanliness without modifying files
fmt-check:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
	  echo "gofmt found unformatted files:"; \
	  echo "$$out"; \
	  exit 1; \
	fi

.PHONY: vet ## Run go vet
vet:
	$(GO) vet ./...

.PHONY: lint ## Run golangci-lint if installed
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint not installed. Install with:"; \
	  echo "  brew install golangci-lint"; \
	  exit 1; \
	}
	golangci-lint run ./...

.PHONY: staticcheck ## Run staticcheck if installed
staticcheck:
	@command -v staticcheck >/dev/null 2>&1 || { \
	  echo "staticcheck not installed. Install with:"; \
	  echo "  go install honnef.co/go/tools/cmd/staticcheck@latest"; \
	  exit 1; \
	}
	staticcheck ./...

.PHONY: tidy ## Run go mod tidy
tidy:
	$(GO) mod tidy

.PHONY: verify ## Full pre-push check: fmt-check + vet + test-race
verify: fmt-check vet test-race

## --- release ------------------------------------------------------------

.PHONY: release-snapshot ## Build a local snapshot release (no publish)
release-snapshot:
	@command -v goreleaser >/dev/null 2>&1 || { \
	  echo "goreleaser not installed. Install with:"; \
	  echo "  brew install goreleaser"; \
	  exit 1; \
	}
	goreleaser release --snapshot --clean

.PHONY: release-check ## Validate .goreleaser.yml
release-check:
	@command -v goreleaser >/dev/null 2>&1 || { \
	  echo "goreleaser not installed."; \
	  exit 1; \
	}
	goreleaser check

## --- housekeeping -------------------------------------------------------

.PHONY: clean ## Remove build artifacts, coverage reports, and goreleaser output
clean:
	rm -rf build dist coverage.out coverage.html

.PHONY: version ## Print the version that would be embedded in the binary
version:
	@echo "VERSION=$(VERSION)"
	@echo "COMMIT=$(COMMIT)"
	@echo "DATE=$(DATE)"
