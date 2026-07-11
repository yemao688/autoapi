.PHONY: all install dev build build-all build-macos build-windows build-linux test test-go test-frontend vet fmt generate clean help

# App version injected via ldflags. Override with: make build VERSION=1.0.0
VERSION ?= $(shell grep productVersion wails.json | sed 's/.*: *"//;s/".*//')
BUILD ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X main.appVersion=$(VERSION) -X main.appBuild=$(BUILD)

# Default target: run everything a CI-like check would run.
all: fmt vet test build

## Setup
install:
	cd frontend && npm install

## Development
dev:
	wails dev

## Code generation
generate:
	wails generate module

## Production builds
build:
	wails build -ldflags "$(LDFLAGS)"

build-all: build-macos build-windows build-linux

build-macos:
	wails build -platform darwin/universal -clean -ldflags "$(LDFLAGS)"

build-windows:
	wails build -platform windows/amd64 -clean -ldflags "$(LDFLAGS)"

build-linux:
	wails build -platform linux/amd64 -clean -ldflags "$(LDFLAGS)"

## Testing
test: test-go test-frontend

test-go:
	go test ./internal/...

test-frontend:
	cd frontend && npm run build

## Go checks
vet:
	go vet ./internal/...

fmt:
	gofmt -w ./internal ./main.go ./app.go

## Maintenance
clean:
	rm -rf build/bin
	rm -rf frontend/dist
	rm -rf frontend/package.json.md5

help:
	@echo "Available targets:"
	@echo "  install        - install frontend dependencies"
	@echo "  dev            - run wails dev (hot reload)"
	@echo "  generate       - regenerate Wails TS bindings"
	@echo "  build          - build current platform"
	@echo "  build-all      - build macOS/Windows/Linux"
	@echo "  test           - run Go tests and frontend build"
	@echo "  test-go        - run Go tests only"
	@echo "  test-frontend  - run frontend typecheck + build only"
	@echo "  vet            - run go vet"
	@echo "  fmt            - run gofmt on Go files"
	@echo "  clean          - remove build artifacts"
	@echo "  all            - fmt + vet + test + build"
	@echo "  help           - show this help"
