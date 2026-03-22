# ════════════════════════════════════════════════════════════════════════════
# Sports Stream Backend — Single container, pure Go
# Builds all 5 binaries (4 services + 1 Go gateway)
# No nginx — Go reverse proxy handles routing on :8080
# ════════════════════════════════════════════════════════════════════════════

# ── Stage 1: Build all binaries ───────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build all 4 services + gateway
RUN go build -o user-service         ./services/user/main.go
RUN go build -o stream-service       ./services/stream/main.go
RUN go build -o analytics-service    ./services/analytics/main.go
RUN go build -o notification-service ./services/notification/main.go
RUN go build -o gateway              ./gateway/main.go

# ── Stage 2: Runtime ─────────────────────────────────────────────────────
FROM alpine:latest

RUN apk --no-cache add ca-certificates ffmpeg

WORKDIR /root/

# Copy all binaries
COPY --from=builder /app/user-service         ./user-service
COPY --from=builder /app/stream-service       ./stream-service
COPY --from=builder /app/analytics-service    ./analytics-service
COPY --from=builder /app/notification-service ./notification-service
COPY --from=builder /app/gateway              ./gateway

# Copy startup script
COPY start.sh ./start.sh

# Make all binaries and script executable
RUN chmod +x ./user-service \
             ./stream-service \
             ./analytics-service \
             ./notification-service \
             ./gateway \
             ./start.sh

EXPOSE 8080

CMD ["./start.sh"]