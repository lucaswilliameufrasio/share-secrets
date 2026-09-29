GO ?= mise exec -- go
NPM ?= npm

.PHONY: all setup fmt check vet lint build run test test-unit test-race coverage bench \
	security security-vuln security-static e2e e2e-install clean help

all: fmt check test-race build

## Install the pinned Go toolchain and JavaScript E2E dependencies.
setup:
	mise install
	$(GO) mod download
	$(NPM) ci
	$(NPM) exec playwright install chromium

## Format Go source files.
fmt:
	$(GO) fmt ./...

## Check formatting and static Go analysis.
check:
	@test -z "$$(gofmt -l $$(rg --files -g '*.go'))" || { echo "Go files need formatting; run make fmt"; exit 1; }
	$(GO) vet ./...

vet:
	$(GO) vet ./...

## Optional golangci-lint (requires golangci-lint in PATH).
lint:
	golangci-lint run ./...

build:
	$(GO) build ./...

run:
	$(GO) run .

test: test-unit

test-unit:
	$(GO) test -count=1 ./...

test-race:
	$(GO) test -race -count=1 ./...

coverage:
	$(GO) test -count=1 -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

bench:
	$(GO) test -run '^$$' -bench=. -benchmem ./...

security: security-vuln security-static

security-vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

security-static:
	$(GO) run github.com/securego/gosec/v2/cmd/gosec@latest ./...

e2e:
	$(NPM) run test:e2e

e2e-install:
	$(NPM) ci
	$(NPM) exec playwright install chromium

clean:
	rm -f share-secrets coverage.out
	rm -rf test-results playwright-report

help:
	@grep -E '^[a-zA-Z0-9_-]+:|^## ' Makefile | awk 'BEGIN { FS = ":" } /^## / { desc = substr($$0, 4); next } /^[a-zA-Z0-9_-]+:/ { printf "  make %-18s %s\n", $$1, desc; desc = "" }'
