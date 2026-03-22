#!/bin/sh
# start.sh — starts all 4 Go services + Go gateway

echo "Starting Sports Stream Backend..."

# ── Fix permissions at runtime ────────────────────────────────────────
chmod +x /root/user-service
chmod +x /root/stream-service
chmod +x /root/analytics-service
chmod +x /root/notification-service
chmod +x /root/gateway

# ── Start user-service ────────────────────────────────────────────────
PORT=8081 /root/user-service &
echo "user-service started on :8081"

# ── Start stream-service ──────────────────────────────────────────────
PORT=8082 /root/stream-service &
echo "stream-service started on :8082"

# ── Start analytics-service ───────────────────────────────────────────
PORT=8085 METRICS_PORT=9090 /root/analytics-service &
echo "analytics-service started on :8085"

# ── Start notification-service (no HTTP) ─────────────────────────────
/root/notification-service &
echo "notification-service started"

# ── Wait for services to be ready ────────────────────────────────────
sleep 3

# ── Start Go gateway on :8080 (foreground) ───────────────────────────
echo "Starting Go API Gateway on :8080..."
GATEWAY_PORT=8080 /root/gateway