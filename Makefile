.DEFAULT_GOAL := help
.PHONY: setup-dev upgrade pre-commit test install uninstall reinstall clean help

# Identity derived from the cmd/<name> layout.
BINARY  := $(notdir $(CURDIR))
PKG     := ./cmd/$(BINARY)
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOBIN   := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)
EXE     := $(GOBIN)/$(BINARY)$(shell go env GOEXE)

# Where the macOS .pkg, plus the receipt it registers
PKGID   := com.lucuma13.$(BINARY)
PKGEXE  := /usr/local/bin/$(BINARY)

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
	  Darwin|MINGW*|MSYS*|CYGWIN*) "$(EXE)" integrate on ;; \
	  *) echo "integrate: skipped (only macOS and Windows are supported)" ;; \
	esac

uninstall: ## Remove the installed binaries ($GOBIN and .pkg) and the right-click integration
	@case "$$(uname -s)" in \
	  Darwin|MINGW*|MSYS*|CYGWIN*) \
	    for b in "$(EXE)" "$(PKGEXE)"; do \
	      if [ -x "$$b" ]; then "$$b" integrate off || true; break; fi; \
	    done ;; \
	esac
	rm -f "$(EXE)"
	@case "$$(uname -s)" in \
	  Darwin) \
	    if [ -e "$(PKGEXE)" ] || pkgutil --pkg-info $(PKGID) >/dev/null 2>&1; then \
	      echo "Removing the .pkg-installed copy at $(PKGEXE) (needs sudo)."; \
	      sudo rm -f "$(PKGEXE)" "/usr/local/bin/._$(BINARY)"; \
	      sudo pkgutil --forget $(PKGID) >/dev/null 2>&1 || true; \
	    fi ;; \
	esac

reinstall: ## Uninstall any previous copy, then install fresh
	$(MAKE) uninstall
	$(MAKE) install

clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out
	rm -rf dist

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-11s\033[0m %s\n", $$1, $$2}'
