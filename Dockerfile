FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agentrm ./cmd/agentrm

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/agentrm /agentrm
EXPOSE 8080
ENTRYPOINT ["/agentrm"]

