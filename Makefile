.PHONY: build run test clean web all dev-up dev-run dev-test dev-down

GO ?= go

build:
	$(GO) build -o bin/mcpguard ./cmd/mcpguard
	$(GO) build -o bin/mcpguard-connect ./cmd/mcpguard-connect

run:
	$(GO) run ./cmd/mcpguard

test:
	$(GO) test ./...

dev-up:
	python3 scripts/dev.py up

dev-run:
	python3 scripts/dev.py run

dev-test:
	python3 scripts/dev.py test

dev-down:
	python3 scripts/dev.py down

clean:
	rm -rf bin/

web:
	cd web && npm install && npm run build

all: build
