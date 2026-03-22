#!/bin/sh
echo "Starting Sports Stream Backend..."

PORT=8081 /usr/local/bin/user-service &
echo "user-service started on :8081"

PORT=8082 /usr/local/bin/stream-service &
echo "stream-service started on :8082"

PORT=8085 METRICS_PORT=9090 /usr/local/bin/analytics-service &
echo "analytics-service started on :8085"

/usr/local/bin/notification-service &
echo "notification-service started"

sleep 3

echo "Starting Go API Gateway on :8080..."
GATEWAY_PORT=8080 /usr/local/bin/gateway