.PHONY: build run test clean web all

build:
	go build -o bin/mcpguard cmd/mcpguard/main.go

run:
	go run cmd/mcpguard/main.go

test:
	go test ./...

clean:
	rm -rf bin/

web:
	cd web && npm install && npm run build

all: web build
