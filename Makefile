.PHONY: build run test vet fmt

GOCACHE ?= $(CURDIR)/.gocache
GOMODCACHE ?= $(CURDIR)/.gomodcache

build:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go build -o bin/agentrm ./cmd/agentrm

run:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run ./cmd/agentrm

test:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -race ./...

vet:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.gocache/*')
