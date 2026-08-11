.DEFAULT_GOAL := help

GO ?= go
GOIMPORTS ?= goimports
HERDR ?= herdr

BINARY := herdr-plugin-k8s-context
PLUGIN_ID := herdr.k8s-context
GO_FILES := $(shell find cmd internal -type f -name '*.go' | sort)

.PHONY: help build fmt test test-race vet check link unlink open logs

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the plugin binary
	$(GO) build -o $(BINARY) ./cmd/herdr-plugin-k8s-context

fmt: ## Format all Go files with gofmt and goimports
	gofmt -w $(GO_FILES)
	$(GOIMPORTS) -w $(GO_FILES)

test: ## Run unit tests
	$(GO) test ./...

test-race: ## Run unit tests with the race detector
	$(GO) test -race ./...

vet: ## Run go vet
	$(GO) vet ./...

check: fmt test vet ## Format and run the standard checks

link: build ## Build and link the plugin into Herdr
	$(HERDR) plugin link .

unlink: ## Unlink the local plugin from Herdr
	$(HERDR) plugin unlink $(PLUGIN_ID)

open: ## Open the plugin popup in the active Herdr workspace
	$(HERDR) plugin action invoke open --plugin $(PLUGIN_ID)

logs: ## Show recent plugin logs
	$(HERDR) plugin log list --plugin $(PLUGIN_ID) --limit 20
