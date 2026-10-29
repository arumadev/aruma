# Aruma build and development tasks. Run `make help` for the list.

SHELL        := /bin/bash
GO           ?= go
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS      := -s -w -X main.version=$(VERSION)
SQLC_VERSION := v1.31.1
SQLC         := $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
# PostgreSQL client tools: from ARUMA_PG_BIN when set (e.g. DBngin's bin directory), else PATH.
PSQL          = $(if $(ARUMA_PG_BIN),$(ARUMA_PG_BIN)/psql,psql)

# Local development defaults. Override in the environment or a .env file.
-include .env
export ARUMA_DATABASE_URL ?= postgres://postgres@127.0.0.1:5432/aruma?sslmode=disable
export ARUMA_PUBLIC_URL   ?= http://localhost:8080
export ARUMA_TEST_DATABASE_URL ?= postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

.PHONY: dashboard
dashboard: ## Build the dashboard and stage it for embedding
	cd dashboard && npm ci --no-audit --no-fund && npm run build
	find internal/admin/web/dist -mindepth 1 ! -name .gitkeep -delete
	cp -R dashboard/out/. internal/admin/web/dist/

.PHONY: dev
dev: db-create ## Run the server locally on http://localhost:8080 (creates .env on first run)
	@test -f .env || { printf 'ARUMA_DATABASE_URL=%s\nARUMA_PUBLIC_URL=%s\nARUMA_MASTER_KEY=%s\n' \
		'$(ARUMA_DATABASE_URL)' '$(ARUMA_PUBLIC_URL)' "$$(openssl rand -base64 32)" > .env; chmod 600 .env; echo "created .env"; }
	@test -f internal/admin/web/dist/index.html || echo "note: the dashboard is not built; run 'make dashboard' first"
	$(GO) build -o bin/aruma ./cmd/aruma
	set -a && . ./.env && set +a && bin/aruma migrate up && exec bin/aruma serve

.PHONY: build
build: ## Build the binaries into bin/
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/aruma ./cmd/aruma
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/arumactl ./cmd/arumactl

# Release archives: one per platform, named aruma_<version>_<os>_<arch> (.zip on Windows,
# .tar.gz elsewhere), plus checksums.txt. The binaries are static (no cgo), so every target
# builds on one machine. The release workflow runs this after `make dashboard`.
DIST_TARGETS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
DIST_VERSION  = $(patsubst v%,%,$(VERSION))

.PHONY: dist
dist: ## Build the release archives and checksums into dist/ (run 'make dashboard' first)
	@test -f internal/admin/web/dist/index.html || { echo "dist: the dashboard is not built; run 'make dashboard' first"; exit 1; }
	rm -rf dist && mkdir -p dist
	@set -e; for t in $(DIST_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		name=aruma_$(DIST_VERSION)_$${os}_$${arch}; dir=dist/$$name; \
		echo "dist: $$name"; mkdir -p "$$dir"; \
		for b in aruma arumactl; do \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o "$$dir/$$b$$ext" ./cmd/$$b; \
		done; \
		cp LICENSE NOTICE README.md aruma.example.yaml "$$dir/"; \
		if [ "$$os" = windows ]; then (cd dist && zip -qr "$$name.zip" "$$name"); \
		else tar -C dist -czf "dist/$$name.tar.gz" "$$name"; fi; \
		rm -rf "$$dir"; \
	done
	cd dist && { command -v sha256sum >/dev/null && sha256sum aruma_* || shasum -a 256 aruma_*; } > checksums.txt
	@cat dist/checksums.txt

.PHONY: test
test: ## Run all Go tests (needs local Postgres)
	$(GO) test -race -count=1 ./...

.PHONY: test-short
test-short: ## Run Go tests that need no database
	$(GO) test -short -count=1 ./...

# The default run of the Document Database's linearizability check takes a few seconds and is part
# of `make test`; this one runs the clients for LIN_DURATION, for a nightly job.
LIN_DURATION ?= 60s

.PHONY: linearizability
linearizability: ## Run the Document Database's Porcupine check longer (LIN_DURATION, default 60s)
	ARUMA_LINEARIZABILITY_DURATION=$(LIN_DURATION) $(GO) test -race -count=1 -timeout 60m -v \
		-run '^TestLinearizability$$' ./test/linearizability/documents/

.PHONY: lint
lint: ## gofmt, go vet and the dependency rules
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(GO) test -count=1 ./internal/archtest/

.PHONY: smoke
smoke: ## Run the SDK smoke tests (all targets this machine supports, or SMOKE="web ios ...")
	test/smoke/run.sh $(SMOKE)

.PHONY: sqlc
sqlc: ## Regenerate query code from SQL
	$(SQLC) generate

.PHONY: sqlc-check
sqlc-check: ## Fail if generated query code is stale
	$(SQLC) diff

# The Document Database's protobuf code (buf.gen.yaml), written as *.gen.go like sqlc's. buf and its
# plugins run at pinned versions through `go run`, so nothing is installed.
BUF_VERSION := v1.73.0
BUF         := $(GO) run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
PROTO_OUT   := internal/services/documents/wire/pb

# proto-gen DIR: the merge check, then the generated code under DIR/$(PROTO_OUT).
define proto-gen
	$(GO) run ./tools/protomerge
	rm -rf $(1) && mkdir -p $(1)
	$(BUF) generate --template buf.gen.yaml -o $(1)
	find $(1) -name '*.go' ! -name '*.gen.go' -exec sh -c 'mv "$$1" "$${1%.go}.gen.go"' _ {} \;
endef

.PHONY: proto
proto: ## Regenerate the Document Database's protobuf code from third_party
	$(call proto-gen,.cache/proto)
	rm -rf $(PROTO_OUT) && mkdir -p $(dir $(PROTO_OUT)) && mv .cache/proto/$(PROTO_OUT) $(PROTO_OUT)
	rm -rf .cache/proto

.PHONY: proto-check
proto-check: ## Fail if the generated protobuf code is stale
	$(call proto-gen,.cache/proto-check)
	diff -r $(PROTO_OUT) .cache/proto-check/$(PROTO_OUT)
	rm -rf .cache/proto-check

.PHONY: db-create
db-create: ## Create the local development database "aruma"
	$(PSQL) "$(ARUMA_TEST_DATABASE_URL)" -tc "SELECT 1 FROM pg_database WHERE datname = 'aruma'" | grep -q 1 \
		|| $(PSQL) "$(ARUMA_TEST_DATABASE_URL)" -c 'CREATE DATABASE aruma'

.PHONY: migrate
migrate: ## Apply migrations to the local database
	$(GO) run ./cmd/aruma migrate up

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist
