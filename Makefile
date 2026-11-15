.PHONY: all install dev build package build-macos dmg dmg-universal build-windows build-linux test test-go test-frontend vet fmt generate clean help

# App version is owned by build/config.yml (Wails v3 project config).
VERSION ?= $(shell sed -n 's/^  version: "\(.*\)"/\1/p' build/config.yml | head -1)
BUILD ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
TASK_VARS := VERSION=$(VERSION) BUILD=$(BUILD)

# Default target: run everything a CI-like check would run.
all: fmt vet test build

## Setup
install:
	cd frontend && npm install

## Development
dev:
	wails3 dev

## Code generation
generate:
	wails3 generate bindings -ts -clean

## Production builds
build:
	wails3 task build $(TASK_VARS)

package:
	wails3 task package $(TASK_VARS)

build-macos:
	wails3 task package $(TASK_VARS)

dmg:
	wails3 task package:dmg $(TASK_VARS)

dmg-universal:
	wails3 task package:universal $(TASK_VARS)

build-windows:
	GOOS=windows GOARCH=amd64 wails3 task build $(TASK_VARS)

build-linux:
	GOOS=linux wails3 task build $(TASK_VARS)

## Testing
test: test-go test-frontend

test-go:
	go test ./internal/...

test-frontend:
	cd frontend && npm run build

## Go checks
vet:
	go vet ./...

fmt:
	gofmt -w ./internal ./main.go ./app.go

## Maintenance
clean:
	wails3 task clean

help:
	@echo "Available targets:"
	@echo "  install        - install frontend dependencies"
	@echo "  dev            - run wails3 dev (hot reload)"
	@echo "  generate       - regenerate Wails TS bindings"
	@echo "  build          - build current platform"
	@echo "  package        - package current platform (macOS .app)"
	@echo "  build-macos    - package the macOS .app"
	@echo "  dmg            - package the macOS .app and wrap it in a DMG"
	@echo "  dmg-universal  - build arm64 + amd64 and wrap them in one universal DMG"
	@echo "  build-windows  - cross-compile the Windows binary (CGO-free)"
	@echo "  build-linux    - build the Linux binary (run on Linux; CGO)"
	@echo "  test           - run Go tests and frontend build"
	@echo "  test-go        - run Go tests only"
	@echo "  test-frontend  - run frontend typecheck + build only"
	@echo "  vet            - run go vet"
	@echo "  fmt            - run gofmt on Go files"
	@echo "  clean          - remove build artifacts"
	@echo "  all            - fmt + vet + test + build"
	@echo "  help           - show this help"
