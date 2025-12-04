# Build stage
FROM golang:1.25-alpine AS builder

ARG VERSION=dev

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o roar ./cmd/roar

# Runtime stage
FROM alpine:3.20

# Install FUSE runtime dependencies
RUN apk add --no-cache fuse

# Copy the binary from builder
COPY --from=builder /app/roar /usr/local/bin/roar

# Create a non-root user
RUN adduser -D -u 1000 roar

# Set up mount points
RUN mkdir -p /source /mount && chown roar:roar /source /mount

USER roar

ENTRYPOINT ["/usr/local/bin/roar"]
CMD ["--help"]
