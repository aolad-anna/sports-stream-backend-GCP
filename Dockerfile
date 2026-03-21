# ════════════════════════════════════════════════════════════════════════════
# Sports Stream Backend — Multi-service Dockerfile
# Builds all 4 service binaries in one image
# Fly.io process groups select which binary to run
# ════════════════════════════════════════════════════════════════════════════

# ── Stage 1: Build all services ───────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Download dependencies first (cached layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy all source code
COPY . .

# Build all 4 service binaries
RUN go build -o user-service         ./services/user/main.go
RUN go build -o stream-service       ./services/stream/main.go
RUN go build -o analytics-service    ./services/analytics/main.go
RUN go build -o notification-service ./services/notification/main.go

# ── Stage 2: Run ─────────────────────────────────────────────────────────
FROM alpine:latest

# Install certificates + ffmpeg (needed by stream-service)
RUN apk --no-cache add ca-certificates ffmpeg

WORKDIR /root/

# Copy all 4 binaries
COPY --from=builder /app/user-service         ./user-service
COPY --from=builder /app/stream-service       ./stream-service
COPY --from=builder /app/analytics-service    ./analytics-service
COPY --from=builder /app/notification-service ./notification-service

# Default cmd — Fly.io process groups override this
CMD ["./user-service"]