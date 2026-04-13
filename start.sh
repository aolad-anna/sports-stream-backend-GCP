#!/bin/sh
# start.sh — runs all services in one container
# Cloud Run injects $PORT=8080 — save it FIRST before overwriting

echo "Starting Sports Stream Backend..."

# ── Save Cloud Run's PORT before we overwrite it ──────────────────────────
# Cloud Run injects PORT=8080. We must save it now because the lines below
# set PORT=8081, 8082 etc. for each service — overwriting the original value.
GATEWAY_PORT=${PORT:-8080}
echo "Gateway will listen on :${GATEWAY_PORT}"

resolve_bin() {
  local short_name="$1"
  local long_name="$2"
  if [ -x "/usr/local/bin/${long_name}" ]; then
    echo "/usr/local/bin/${long_name}"
    return
  fi
  if [ -x "./bin/${short_name}" ]; then
    echo "./bin/${short_name}"
    return
  fi
  echo ""
}

USER_BIN=$(resolve_bin "user" "user-service")
STREAM_BIN=$(resolve_bin "stream" "stream-service")
NOTIFICATION_BIN=$(resolve_bin "notification" "notification-service")
ADMIN_BIN=$(resolve_bin "admin" "admin-service")
ANALYTICS_BIN=$(resolve_bin "analytics" "analytics-service")
GATEWAY_BIN=$(resolve_bin "gateway" "gateway")

if [ -z "$USER_BIN" ] || [ -z "$STREAM_BIN" ] || [ -z "$NOTIFICATION_BIN" ] || [ -z "$ADMIN_BIN" ] || [ -z "$ANALYTICS_BIN" ] || [ -z "$GATEWAY_BIN" ]; then
  echo "ERROR: Failed to locate one or more service binaries"
  exit 1
fi

# ── Start internal services on fixed ports ────────────────────────────────
PORT=8081 "$USER_BIN" &
echo "user-service started on :8081"

PORT=8082 "$STREAM_BIN" &
echo "stream-service started on :8082"

PORT=8083 "$NOTIFICATION_BIN" &
echo "notification-service started on :8083"

PORT=8084 "$ADMIN_BIN" &
echo "admin-service started on :8084"

PORT=8085 METRICS_PORT=9090 "$ANALYTICS_BIN" &
echo "analytics-service started on :8085"

# ── Wait for internal services to be ready ────────────────────────────────
sleep 3

# ── Start gateway on saved Cloud Run port ─────────────────────────────────
# Use GATEWAY_PORT (saved before any PORT overrides above)
# exec replaces this shell process — Cloud Run health check hits this
echo "Starting gateway on :${GATEWAY_PORT}"
export PORT="${GATEWAY_PORT}"
export USER_SERVICE_URL="http://127.0.0.1:8081"
export STREAM_SERVICE_URL="http://127.0.0.1:8082"
export ANALYTICS_SERVICE_URL="http://127.0.0.1:8085"
export NOTIFICATION_SERVICE_URL="http://127.0.0.1:8083"
export ADMIN_SERVICE_URL="http://127.0.0.1:8084"
exec "$GATEWAY_BIN"