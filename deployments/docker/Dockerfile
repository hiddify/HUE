# Build stage
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git make protobuf

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /hue ./cmd/hue

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /hue /app/hue

# Create data directory
RUN mkdir -p /data

# Set environment defaults
ENV HUE_DB_URL=sqlite:///data/hue.db
ENV HUE_PORT=50051
ENV HUE_LOG_LEVEL=info

# Expose ports
EXPOSE 50051 50052

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q --spider http://localhost:50052/health || exit 1

# Run the binary
ENTRYPOINT ["/app/hue"]
