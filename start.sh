#!/bin/bash
# start.sh — runs all services in one container
# Works on Scalingo, Fly.io, Render, Railway
# Scalingo injects $PORT — gateway must listen on it

echo "Starting Sports Stream Backend..."

# Start internal services on fixed ports
PORT=8081 ./bin/user &
echo "user-service started on :8081"

PORT=8082 ./bin/stream &
echo "stream-service started on :8082"

PORT=8083 ./bin/notification &
echo "notification-service started on :8083"

PORT=8085 METRICS_PORT=9090 ./bin/analytics &
echo "analytics-service started on :8085"

# Wait for services to be ready
sleep 3

# Gateway listens on $PORT (injected by Scalingo/Fly.io/Render)
# Falls back to 8080 locally
GATEWAY_PORT=${PORT:-8080} ./bin/gateway