.DEFAULT_GOAL := help
.PHONY: upgrade pre-commit test install uninstall reinstall clean help

# Identity derived from the cmd/<name> layout.
BINARY  := $(notdir $(CURDIR))
PKG     := ./cmd/$(BINARY)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBIN   := $(shell go env GOPATH)/bin

upgrade: ## Upgrade dependencies
	go get -u ./...
	go mod tidy

pre-commit: ## Run all pre-commit hooks
	pre-commit run --all-files

test: ## Run the test suite
	go test -coverprofile=coverage.out -covermode=atomic ./...

install: ## Install to $GOBIN and integrate the right-click action (macOS/Windows)
	go install -trimpath -ldflags '$(LDFLAGS)' $(PKG)
	@case "$$(uname -s)" in \
	  Darwin|MINGW*|MSYS*|CYGWIN*) "$(GOBIN)/$(BINARY)" integrate ;; \
	  *) echo "integrate: skipped (only macOS and Windows are supported)" ;; \
	esac

uninstall: ## Remove the installed binary and its right-click integration
	@case "$$(uname -s)" in \
	  Darwin|MINGW*|MSYS*|CYGWIN*) [ -x "$(GOBIN)/$(BINARY)" ] && "$(GOBIN)/$(BINARY)" integrate --remove || true ;; \
	esac
	rm -f "$(GOBIN)/$(BINARY)"

reinstall: ## Uninstall any previous copy, then install fresh
	$(MAKE) uninstall
	$(MAKE) install

clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out
	rm -rf dist

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-11s\033[0m %s\n", $$1, $$2}'
