# Multi-stage build for Go application
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

# Runtime stage
FROM alpine:latest

# Install Docker CLI and other dependencies
RUN apk --no-cache add docker-cli ca-certificates tzdata

# Create app user
RUN addgroup -g 1001 appgroup && \
    adduser -u 1001 -D -s /bin/sh -G appgroup appuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/main .

# Create temp directories with proper permissions
RUN mkdir -p /tmp/app && \
    chown -R appuser:appgroup /app /tmp/app

# Switch to non-root user
USER appuser

# Command to run
CMD ["./main"]