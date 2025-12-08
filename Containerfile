# =============================================================================
# Status Page - Multi-Stage Container Build
# Produces a minimal scratch-based image (~15MB)
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: Build
# -----------------------------------------------------------------------------
FROM docker.io/library/golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build static binary
# CGO_ENABLED=0 for static linking (required for scratch)
# -ldflags for smaller binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s -extldflags '-static'" \
    -a -installsuffix cgo \
    -o status .

# -----------------------------------------------------------------------------
# Stage 2: Production (distroless for proper non-root support)
# -----------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

# Labels
LABEL org.opencontainers.image.title="Status Page"
LABEL org.opencontainers.image.description="Enterprise-ready status page with multi-protocol health monitoring"
LABEL org.opencontainers.image.source="https://github.com/anubhavg-icpl/status"
LABEL org.opencontainers.image.licenses="MIT"

# Copy binary (distroless includes CA certs and tzdata)
COPY --from=builder /build/status /status

# Copy templates
COPY --from=builder /build/web/templates /web/templates

# Copy default config
COPY --from=builder /build/config.yaml /config.yaml

# Expose port
EXPOSE 8080

# distroless:nonroot runs as UID 65532 by default
# Data directory will be created at runtime in /tmp or use mounted volume

# Default command
ENTRYPOINT ["/status"]
CMD ["-config", "/config.yaml"]
