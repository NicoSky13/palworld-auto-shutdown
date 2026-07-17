# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install build dependencies if any
RUN apk add --no-cache git

# Copy dependency files
COPY go.mod ./
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o palworld-manager ./cmd/manager/main.go

# Production stage
FROM alpine:3.18

RUN apk add --no-cache docker-cli

WORKDIR /app

# Copy the binary and UI assets
COPY --from=builder /app/palworld-manager .
COPY ui/ ./ui/

# Expose ports:
# 8211/UDP (Proxy listener)
# 8213/TCP (Web UI and API)
EXPOSE 8211/udp
EXPOSE 8213/tcp

ENTRYPOINT ["./palworld-manager"]
