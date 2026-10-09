# Solaigo Companion — build and release helpers.
#
# The default target compiles the binary for the host OS and architecture. The
# cross-compile targets produce one binary per supported OS in ./dist/, which is
# what the GitHub release workflow uploads.

MODULE   := github.com/IPConvergence/solaigo-companion
BINARY   := solaigo-companion
PKG      := $(MODULE)/cmd/companion
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
DIST     := dist

.PHONY: help build test lint vet tidy run cross release clean

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build for the host platform into ./$(BINARY)
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

test: ## Run all tests
	go test ./...

vet: ## Run go vet across the module
	go vet ./...

tidy: ## Tidy the module file
	go mod tidy

run: build ## Build and run with --help
	./$(BINARY) --help

# Cross-compile matrix. Four target triples cover 99% of desktops.
cross: ## Build release binaries for linux, macos (intel + arm), windows
	mkdir -p $(DIST)
	GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64        $(PKG)
	GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-amd64       $(PKG)
	GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-darwin-arm64       $(PKG)
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-windows-amd64.exe  $(PKG)
	@echo
	@ls -lh $(DIST)/

release: test vet cross ## Everything CI runs for a tagged release

clean: ## Remove build artifacts
	rm -rf $(BINARY) $(DIST)
