#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

MODE="${1:-build}"
GATEWAY_URL="${GATEWAY_URL:-http://127.0.0.1:8080}"

echo "[deploy] mode=$MODE"

build_binaries() {
	echo "[deploy] building all services"
	mkdir -p bin
	go build -o bin/user ./services/user/main.go
	go build -o bin/stream ./services/stream/main.go
	go build -o bin/notification ./services/notification/main.go
	go build -o bin/admin ./services/admin/main.go
	go build -o bin/analytics ./services/analytics/main.go
	go build -o bin/gateway ./gateway/main.go
}

health_check() {
	echo "[deploy] waiting for gateway health at $GATEWAY_URL/health"
	for i in {1..20}; do
		if curl -fsS "$GATEWAY_URL/health" >/dev/null; then
			echo "[deploy] health check passed"
			return 0
		fi
		sleep 2
	done
	echo "[deploy] health check failed"
	return 1
}

case "$MODE" in
	build)
		build_binaries
		;;
	up)
		docker compose up -d --build
		health_check
		;;
	restart)
		docker compose down
		docker compose up -d --build
		health_check
		;;
	down)
		docker compose down
		;;
	*)
		echo "Usage: $0 [build|up|restart|down]"
		exit 1
		;;
esac

echo "[deploy] done"
