GO ?= mise exec -- go
GOFMT ?= mise exec -- gofmt
NPM ?= npm

.PHONY: all setup tools-update fmt check vet lint build run test test-unit test-race test-benchmark-tools coverage bench bench-http \
	security security-vuln security-static e2e e2e-sqlite e2e-install migrations-check deploy clean help

all: fmt check test-race test-benchmark-tools build

## Install the pinned Go toolchain and JavaScript E2E dependencies.
setup:
	mise install
	$(GO) mod download
	$(GO) tool golangci-lint --version
	$(GO) tool govulncheck -version
	$(GO) tool gosec -version
	$(GO) tool goose -version
	$(NPM) ci
	$(NPM) exec playwright install chromium

## Update pinned Go development tools; review and commit go.mod/go.sum afterwards.
tools-update:
	$(GO) get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest \
		github.com/securego/gosec/v2/cmd/gosec@latest \
		golang.org/x/vuln/cmd/govulncheck@latest \
		github.com/pressly/goose/v3/cmd/goose@latest

## Validate SQLite migration files without opening a database.
migrations-check:
	$(GO) tool goose -env none -dir migrations/sqlite validate

## Format Go source files.
fmt:
	$(GO) fmt ./...

## Check formatting and static Go analysis.
check:
	@formatted="$$(find . -type f -name '*.go' -not -path './.git/*' -exec $(GOFMT) -l {} +)" || { echo "Failed to check Go formatting"; exit 1; }; test -z "$$formatted" || { echo "Go files need formatting; run make fmt"; exit 1; }
	$(GO) vet ./...

vet:
	$(GO) vet ./...

lint:
	$(GO) tool golangci-lint run ./...

build:
	$(GO) build ./...

run:
	$(GO) run .

## Deploy to a VPS with the interactive blue-green CLI.
deploy:
	$(GO) run ./cmd/deploy

test: test-unit

test-unit:
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -race -count=1 ./...

test-benchmark-tools:
	python3 -m unittest discover -s scripts -p 'test_*.py'

coverage:
	$(GO) test -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

bench:
	$(GO) test -run '^$$' -bench=. -benchmem ./...

## Run the bounded local HTTP campaign (rootless Docker, roughly 20–30 minutes).
bench-http:
	python3 scripts/benchmark.py

security: security-vuln security-static

security-vuln:
	$(GO) tool govulncheck ./...

security-static:
	$(GO) tool gosec ./...

e2e:
	$(NPM) run test:e2e

## Exercise the real browser journeys with an isolated Goose-migrated SQLite DB.
e2e-sqlite:
	python3 scripts/e2e_sqlite.py

e2e-install:
	$(NPM) ci
	$(NPM) exec playwright install chromium

clean:
	rm -f share-secrets coverage.out
	rm -rf test-results playwright-report

help:
	@grep -E '^[a-zA-Z0-9_-]+:|^## ' Makefile | awk 'BEGIN { FS = ":" } /^## / { desc = substr($$0, 4); next } /^[a-zA-Z0-9_-]+:/ { printf "  make %-18s %s\n", $$1, desc; desc = "" }'
