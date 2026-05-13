# ════════════════════════════════════════════════════════════════════════════
# Sports Stream Backend — Single container, pure Go + Go gateway
# ════════════════════════════════════════════════════════════════════════════

# ── Stage 1: Build all binaries ───────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build package directories (not single files) so multi-file packages compile
RUN go build -o /bin/user-service         ./services/user/         && chmod +x /bin/user-service
RUN go build -o /bin/stream-service       ./services/stream/       && chmod +x /bin/stream-service
RUN go build -o /bin/analytics-service    ./services/analytics/    && chmod +x /bin/analytics-service
RUN go build -o /bin/notification-service ./services/notification/ && chmod +x /bin/notification-service
RUN go build -o /bin/admin-service        ./services/admin/        && chmod +x /bin/admin-service
RUN go build -o /bin/video-service        ./services/video/        && chmod +x /bin/video-service
RUN go build -o /bin/gateway              ./gateway/               && chmod +x /bin/gateway

# ── Stage 2: Runtime ─────────────────────────────────────────────────────
FROM alpine:latest

RUN apk --no-cache add ca-certificates ffmpeg

# Copy binaries from builder
COPY --from=builder /bin/user-service         /usr/local/bin/user-service
COPY --from=builder /bin/stream-service       /usr/local/bin/stream-service
COPY --from=builder /bin/analytics-service    /usr/local/bin/analytics-service
COPY --from=builder /bin/notification-service /usr/local/bin/notification-service
COPY --from=builder /bin/admin-service        /usr/local/bin/admin-service
COPY --from=builder /bin/video-service        /usr/local/bin/video-service
COPY --from=builder /bin/gateway              /usr/local/bin/gateway

COPY --from=builder /app/public /public

COPY start.sh /usr/local/bin/start.sh
RUN chmod +x /usr/local/bin/start.sh

EXPOSE 8080

CMD ["/usr/local/bin/start.sh"]