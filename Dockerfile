FROM debian:bookworm-slim AS bpf-builder
RUN apt-get update \
    && apt-get install -y --no-install-recommends clang libbpf-dev linux-libc-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
ARG TARGETARCH
COPY bpf/agentrm.bpf.c ./bpf/agentrm.bpf.c
RUN case "${TARGETARCH}" in \
      amd64) bpf_arch=x86; include_arch=x86_64-linux-gnu ;; \
      arm64) bpf_arch=arm64; include_arch=aarch64-linux-gnu ;; \
      *) echo "unsupported eBPF architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && clang -O2 -g -target bpf -D__TARGET_ARCH_${bpf_arch} \
      -I/usr/include/${include_arch} \
      -c bpf/agentrm.bpf.c -o /agentrm.bpf.o

FROM golang:1.26-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agentrm ./cmd/agentrm

FROM gcr.io/distroless/static-debian12
COPY --from=go-builder /out/agentrm /agentrm
COPY --from=bpf-builder /agentrm.bpf.o /usr/lib/agentrm/agentrm.bpf.o
EXPOSE 8080
ENTRYPOINT ["/agentrm"]
