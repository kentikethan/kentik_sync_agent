# syntax=docker/dockerfile:1
FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/kentik-sync-agent ./cmd/kentik-sync-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/kentik-sync-agent /usr/local/bin/kentik-sync-agent
COPY config/example.yaml /etc/kentik-sync-agent/example.yaml

USER nonroot:nonroot
EXPOSE 8080 9090
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD ["/usr/local/bin/kentik-sync-agent", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/kentik-sync-agent", "run", "--config", "/etc/kentik-sync-agent/config.yaml"]
