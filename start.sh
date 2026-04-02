#!/bin/bash
# start.sh — runs all services in one container
# Works on Scalingo, Fly.io, Render, Railway
# Scalingo injects $PORT — gateway must listen on it

echo "Starting Sports Stream Backend..."

resolve_bin() {
	local short_name="$1"
	local long_name="$2"
	if [ -x "./bin/${short_name}" ]; then
		echo "./bin/${short_name}"
		return
	fi
	if [ -x "/usr/local/bin/${long_name}" ]; then
		echo "/usr/local/bin/${long_name}"
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
	echo "Failed to locate one or more service binaries"
	exit 1
fi

# Start internal services on fixed ports
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

# Wait for services to be ready
sleep 3

# Gateway listens on $PORT (injected by Scalingo/Fly.io/Render)
# Falls back to 8080 locally
GATEWAY_PORT=${PORT:-8080} "$GATEWAY_BIN"