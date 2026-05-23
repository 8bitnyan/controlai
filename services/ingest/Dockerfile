# Multi-stage build for controlai-ingest.
# Stage 1: build
FROM golang:1.22-bookworm AS builder

WORKDIR /src
# Copy module files first for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/controlai-ingest ./services/ingest

# Stage 2: minimal distroless runtime (no shell, no package manager).
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

LABEL org.opencontainers.image.source="https://github.com/your-org/controlai"
LABEL org.opencontainers.image.description="controlai IoT ingest service"

COPY --from=builder /out/controlai-ingest /usr/local/bin/controlai-ingest

# Cert mount points expected by the ingest binary (override via env).
VOLUME ["/certs", "/etc/controlai"]

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/controlai-ingest"]
