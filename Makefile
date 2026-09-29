GO ?= mise exec -- go
NPM ?= npm

.PHONY: all setup tools-update fmt check vet lint build run test test-unit test-race coverage bench \
	security security-vuln security-static e2e e2e-install clean help

all: fmt check test-race build

## Install the pinned Go toolchain and JavaScript E2E dependencies.
setup:
	mise install
	$(GO) mod download
	$(GO) tool golangci-lint --version
	$(GO) tool govulncheck -version
	$(GO) tool gosec -version
	$(NPM) ci
	$(NPM) exec playwright install chromium

## Update pinned Go development tools; review and commit go.mod/go.sum afterwards.
tools-update:
	$(GO) get -tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest \
		github.com/securego/gosec/v2/cmd/gosec@latest \
		golang.org/x/vuln/cmd/govulncheck@latest

## Format Go source files.
fmt:
	$(GO) fmt ./...

## Check formatting and static Go analysis.
check:
	@test -z "$$(gofmt -l $$(rg --files -g '*.go'))" || { echo "Go files need formatting; run make fmt"; exit 1; }
	$(GO) vet ./...

vet:
	$(GO) vet ./...

lint:
	$(GO) tool golangci-lint run ./...

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
	$(GO) tool govulncheck ./...

security-static:
	$(GO) tool gosec ./...

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
