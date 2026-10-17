# Aruma build and development tasks. Run `make help` for the list.

SHELL        := /bin/bash
GO           ?= go
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS      := -s -w -X main.version=$(VERSION)
SQLC_VERSION := v1.31.1
SQLC         := $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

# Local development defaults. Override in the environment or a .env file.
-include .env
export ARUMA_DATABASE_URL ?= postgres://postgres@127.0.0.1:5432/aruma?sslmode=disable
export ARUMA_PUBLIC_URL   ?= http://localhost:8080
export ARUMA_TEST_DATABASE_URL ?= postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binaries into bin/
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/aruma ./cmd/aruma

.PHONY: test
test: ## Run all Go tests (needs local Postgres)
	$(GO) test -race -count=1 ./...

.PHONY: test-short
test-short: ## Run Go tests that need no database
	$(GO) test -short -count=1 ./...

.PHONY: lint
lint: ## gofmt, go vet and the dependency rules
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(GO) test -count=1 ./internal/archtest/

.PHONY: sqlc
sqlc: ## Regenerate query code from SQL
	$(SQLC) generate

.PHONY: sqlc-check
sqlc-check: ## Fail if generated query code is stale
	$(SQLC) diff

.PHONY: db-create
db-create: ## Create the local development database "aruma"
	psql "$(ARUMA_TEST_DATABASE_URL)" -tc "SELECT 1 FROM pg_database WHERE datname = 'aruma'" | grep -q 1 \
		|| psql "$(ARUMA_TEST_DATABASE_URL)" -c 'CREATE DATABASE aruma'

.PHONY: migrate
migrate: ## Apply migrations to the local database
	$(GO) run ./cmd/aruma migrate up

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist
