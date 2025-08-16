# Multi-stage build for Go application
FROM golang:1.24.4-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build the application with optimizations
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -a -installsuffix cgo \
    -ldflags='-w -s -extldflags "-static"' \
    -o main .

# Runtime stage
FROM alpine:3.19

# Install Docker CLI and other dependencies
RUN apk --no-cache add \
    docker-cli \
    ca-certificates \
    tzdata \
    && rm -rf /var/cache/apk/*

# Create app user for security
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -D -S -s /bin/sh -G appgroup appuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/main .

# Create temp directories with proper permissions
RUN mkdir -p /tmp/app && \
    chown -R appuser:appgroup /app /tmp/app

# Switch to non-root user
USER appuser

# Set environment variables
ENV TZ=UTC
ENV DOCKER_HOST=unix:///var/run/docker.sock

# Command to run
CMD ["./main"]