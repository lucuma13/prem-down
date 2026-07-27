.DEFAULT_GOAL := help
.PHONY: setup-dev upgrade pre-commit test install uninstall reinstall clean help

# Identity derived from the cmd/<name> layout.
BINARY  := $(notdir $(CURDIR))
PKG     := ./cmd/$(BINARY)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBIN   := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)
EXE     := $(GOBIN)/$(BINARY)$(shell go env GOEXE)

setup-dev: ## Install dev pre-requisites (Go, pre-commit)
ifeq ($(OS),Windows_NT)
	powershell -NoProfile -ExecutionPolicy Bypass -Command "winget install -e --silent --accept-package-agreements --accept-source-agreements GoLang.Go astral-sh.uv; $$env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User'); uv tool install pre-commit; uv tool update-shell; uv tool run pre-commit install"
else ifeq ($(shell uname -s),Darwin)
	brew install go pre-commit
	pre-commit install
else
	sudo apt-get install -y golang-go pre-commit
	pre-commit install
endif

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
	  Darwin|MINGW*|MSYS*|CYGWIN*) "$(EXE)" integrate ;; \
	  *) echo "integrate: skipped (only macOS and Windows are supported)" ;; \
	esac

uninstall: ## Remove the installed binary and its right-click integration
	@case "$$(uname -s)" in \
	  Darwin|MINGW*|MSYS*|CYGWIN*) [ -x "$(EXE)" ] && "$(EXE)" integrate --remove || true ;; \
	esac
	rm -f "$(EXE)"

reinstall: ## Uninstall any previous copy, then install fresh
	$(MAKE) uninstall
	$(MAKE) install

clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out
	rm -rf dist

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-11s\033[0m %s\n", $$1, $$2}'
