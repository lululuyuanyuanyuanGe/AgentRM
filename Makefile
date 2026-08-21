.PHONY: build run test test-go test-python fmt

GOCACHE ?= $(CURDIR)/.gocache
PYTHONPYCACHEPREFIX ?= $(CURDIR)/.python-cache

build:
	GOCACHE=$(GOCACHE) go build -o bin/agentrm ./cmd/agentrm

run:
	GOCACHE=$(GOCACHE) go run ./cmd/agentrm

test: test-go test-python

test-go:
	GOCACHE=$(GOCACHE) go test -race ./...

test-python:
	cd runtime/python && PYTHONPYCACHEPREFIX=$(PYTHONPYCACHEPREFIX) PYTHONPATH=. python3 -m unittest discover -s tests -v

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.gocache/*')
