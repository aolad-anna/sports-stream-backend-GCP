#!/bin/sh
# start.sh — starts all 4 services + nginx
# Runs inside the Docker container on Fly.io

echo "Starting Sports Stream Backend..."

# ── Start user-service ────────────────────────────────────────────────
PORT=8081 ./user-service &
echo "user-service started on :8081"

# ── Start stream-service ──────────────────────────────────────────────
PORT=8082 ./stream-service &
echo "stream-service started on :8082"

# ── Start analytics-service ───────────────────────────────────────────
PORT=8085 METRICS_PORT=9090 ./analytics-service &
echo "analytics-service started on :8085"

# ── Start notification-service (no HTTP) ─────────────────────────────
./notification-service &
echo "notification-service started"

# ── Wait for services to be ready ─────────────────────────────────────
sleep 2

# ── Start nginx (foreground — keeps container alive) ──────────────────
echo "Starting nginx on :8080..."
nginx -g "daemon off;"