GO ?= $(shell command -v go 2>/dev/null || printf '%s' /opt/homebrew/opt/go/bin/go)
GOCACHE ?= $(CURDIR)/.cache/go-build
GOPATH ?= $(CURDIR)/.cache/go-path
GOENV ?= off

export GOCACHE GOPATH GOENV

.PHONY: run test build fmt

run:
	$(GO) run ./cmd/neondrop

test:
	$(GO) test ./...

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags="-s -w" -o bin/neondrop ./cmd/neondrop

fmt:
	$(GO) fmt ./...
