# ════════════════════════════════════════════════════════════════════════════
# Sports Stream Backend — Single container with all 4 services + nginx
# One Fly.io app, one URL, nginx routes by path prefix
#
# Routes:
#   /health              → user-service   :8081
#   /api/v1/auth/*       → user-service   :8081
#   /api/v1/users/*      → user-service   :8081
#   /api/v1/streams*     → stream-service :8082
#   /api/v1/analytics/*  → analytics      :8085
# ════════════════════════════════════════════════════════════════════════════

# ── Stage 1: Build all Go binaries ───────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o user-service         ./services/user/main.go
RUN go build -o stream-service       ./services/stream/main.go
RUN go build -o analytics-service    ./services/analytics/main.go
RUN go build -o notification-service ./services/notification/main.go

# ── Stage 2: Runtime with nginx ──────────────────────────────────────────
FROM alpine:latest

# Install nginx + ffmpeg + ca-certificates
RUN apk --no-cache add nginx ffmpeg ca-certificates

WORKDIR /root/

# Copy all binaries
COPY --from=builder /app/user-service         ./user-service
COPY --from=builder /app/stream-service       ./stream-service
COPY --from=builder /app/analytics-service    ./analytics-service
COPY --from=builder /app/notification-service ./notification-service

# Copy nginx config and startup script
COPY nginx.conf /etc/nginx/nginx.conf
COPY start.sh   ./start.sh
RUN chmod +x ./start.sh

# Fly.io exposes port 8080 by default
EXPOSE 8080

CMD ["./start.sh"]