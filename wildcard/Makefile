# Tailnet Community Bridge — wildcard-domain variant Makefile.
#
# Native targets operate on the Go orchestrator under ./orchestrator.
# Container targets drive docker compose at this directory.

ORCHESTRATOR_DIR := orchestrator
BIN_DIR          := bin
ORCHESTRATOR_BIN := $(BIN_DIR)/orchestrator

GO       ?= go
DOCKER   ?= docker
COMPOSE  ?= $(DOCKER) compose

GOFLAGS  ?= -trimpath
LDFLAGS  ?= -s -w

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available targets.
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} \
	     /^[a-zA-Z0-9_-]+:.*##/ {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' \
	     $(MAKEFILE_LIST)

# ─── Native Go ────────────────────────────────────────────────────────────

.PHONY: build
build: $(ORCHESTRATOR_BIN) ## Build the orchestrator binary into ./bin.

$(ORCHESTRATOR_BIN): $(shell find $(ORCHESTRATOR_DIR) -name '*.go' 2>/dev/null) $(ORCHESTRATOR_DIR)/go.mod
	@mkdir -p $(BIN_DIR)
	cd $(ORCHESTRATOR_DIR) && $(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o ../$(ORCHESTRATOR_BIN) ./cmd/orchestrator

.PHONY: test
test: ## Run unit tests for the orchestrator.
	cd $(ORCHESTRATOR_DIR) && $(GO) test ./...

.PHONY: test-race
test-race: ## Run unit tests with the race detector.
	cd $(ORCHESTRATOR_DIR) && $(GO) test -race ./...

.PHONY: vet
vet: ## Run go vet across the orchestrator.
	cd $(ORCHESTRATOR_DIR) && $(GO) vet ./...

.PHONY: tidy
tidy: ## Run go mod tidy.
	cd $(ORCHESTRATOR_DIR) && $(GO) mod tidy

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf $(BIN_DIR)

# ─── Docker ───────────────────────────────────────────────────────────────

.PHONY: image
image: ## Build the bridge container image via docker compose.
	$(COMPOSE) build

.PHONY: up
up: ## Start the bridge (builds if needed, detached).
	$(COMPOSE) up -d --build

.PHONY: down
down: ## Stop the bridge container (state volume preserved).
	$(COMPOSE) down

.PHONY: logs
logs: ## Follow container logs.
	$(COMPOSE) logs -f

.PHONY: reload
reload: ## SIGHUP to the bridge: re-poll directories AND re-check cert files.
	$(COMPOSE) kill -s HUP bridge

.PHONY: status
status: ## Fetch /__bridge_status from inside the container.
	$(COMPOSE) exec bridge wget -qO- http://127.0.0.1:8081/__bridge_status
