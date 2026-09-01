.PHONY: build bpf run test vet fmt docker-build

GOCACHE ?= $(CURDIR)/.gocache
GOMODCACHE ?= $(CURDIR)/.gomodcache

build:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go build -o bin/agentrm ./cmd/agentrm

BPF_CLANG ?= clang
BPF_ARCH ?= $(if $(filter arm64 aarch64,$(shell uname -m)),arm64,x86)
BPF_INCLUDE ?= /usr/include/$(shell uname -m)-linux-gnu

bpf:
	$(BPF_CLANG) -O2 -g -target bpf -D__TARGET_ARCH_$(BPF_ARCH) -I$(BPF_INCLUDE) \
		-c bpf/agentrm.bpf.c -o bpf/agentrm.bpf.o

run:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run ./cmd/agentrm

test:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -race ./...

vet:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.gocache/*')

docker-build:
	docker build -t agentrm:dev .
