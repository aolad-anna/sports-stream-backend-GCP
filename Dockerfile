# ════════════════════════════════════════════════════════════════════════════
# Sports Stream Backend — Single Dockerfile
# Build any service by passing SERVICE build arg:
#
#   docker build --build-arg SERVICE=user        -t user-service .
#   docker build --build-arg SERVICE=stream      -t stream-service .
#   docker build --build-arg SERVICE=analytics   -t analytics-service .
#   docker build --build-arg SERVICE=notification -t notification-service .
# ════════════════════════════════════════════════════════════════════════════

ARG SERVICE=user

# ── Stage 1: Build ────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder
ARG SERVICE
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o service-binary ./services/${SERVICE}/main.go

# ── Stage 2: Run ─────────────────────────────────────────────────────────
FROM alpine:latest
ARG SERVICE
RUN apk --no-cache add ca-certificates bash && \
    if [ "$SERVICE" = "stream" ]; then apk --no-cache add ffmpeg; fi
WORKDIR /root/
COPY --from=builder /app/service-binary ./service-binary
EXPOSE 8081
CMD ["./service-binary"]