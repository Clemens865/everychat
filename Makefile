.PHONY: dev test build lint clean caddy compose compose-down smoke help

BIN_DIR := bin
EVERYCHAT_BIN := $(BIN_DIR)/everychat
OPS_BIN := $(BIN_DIR)/everychat-ops
VERSION ?= 0.1.0-dev

GO ?= go
GOFLAGS ?=
LDFLAGS ?= -X main.Version=$(VERSION)

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

dev: ## Run the everychat HTTP server (no hot reload)
	$(GO) run ./cmd/everychat

test: ## Run all tests
	$(GO) test ./...

build: ## Build everychat and everychat-ops into ./bin
	@mkdir -p $(BIN_DIR)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(EVERYCHAT_BIN) ./cmd/everychat
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(OPS_BIN) ./cmd/everychat-ops

lint: ## Run golangci-lint
	golangci-lint run ./...

clean: ## Remove build artifacts and local data
	rm -rf $(BIN_DIR) tmp/
	$(GO) clean ./...

caddy: ## Run Caddy reverse proxy on top of everychat
	caddy run --config Caddyfile

compose: ## Bring up the docker-compose stack
	docker compose up --build -d

compose-down: ## Tear down the docker-compose stack
	docker compose down

smoke: ## Run the smoke test against a running compose stack
	./scripts/smoke.sh
