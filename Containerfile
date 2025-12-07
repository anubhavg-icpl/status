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
# Stage 2: Production (scratch)
# -----------------------------------------------------------------------------
FROM scratch

# Labels
LABEL org.opencontainers.image.title="Status Page"
LABEL org.opencontainers.image.description="Enterprise-ready status page with multi-protocol health monitoring"
LABEL org.opencontainers.image.source="https://github.com/anubhavg-icpl/status"
LABEL org.opencontainers.image.licenses="MIT"

# Copy CA certificates for HTTPS
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy binary
COPY --from=builder /build/status /status

# Copy templates (embedded or external)
COPY --from=builder /build/web/templates /web/templates

# Copy default config (optional, can be mounted)
COPY --from=builder /build/config.yaml /config.yaml

# Create data directory marker (will be mounted as volume)
# Note: scratch doesn't have mkdir, directory will be created by volume mount

# Expose port
EXPOSE 8080

# Health check not available in scratch, use orchestrator probes instead
# HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
#   CMD ["/status", "-healthcheck"] || exit 1

# Run as non-root (UID 65534 = nobody)
USER 65534:65534

# Default command
ENTRYPOINT ["/status"]
CMD ["-config", "/config.yaml"]
