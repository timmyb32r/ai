#!/bin/bash
# Start yt2srt: proxy + docker compose. Ctrl+C to stop everything.
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

cleanup() {
    echo ""
    echo "=== Shutting down ==="
    [ -n "${PROXY_PID:-}" ] && kill "$PROXY_PID" 2>/dev/null
    $DOCKER_CMD down 2>/dev/null
    echo "=== Stopped ==="
    exit 0
}
trap cleanup INT TERM

# Detect docker compose command
if docker compose version >/dev/null 2>&1; then
    DOCKER_CMD="docker compose"
else
    DOCKER_CMD="docker-compose"
fi

# Kill old proxy if any
OLD_PID=$(lsof -ti :8889 2>/dev/null || true)
[ -n "$OLD_PID" ] && kill "$OLD_PID" 2>/dev/null && sleep 0.5

echo "=== yt2srt: starting ==="

# Start host proxy (bypasses Colima network issues)
"$SCRIPT_DIR/proxy.sh" &
PROXY_PID=$!
sleep 1
echo "Proxy:  PID=$PROXY_PID :8889"

# Start docker compose
$DOCKER_CMD up &
DOCKER_PID=$!
echo "Docker: PID=$DOCKER_PID"
echo "=== Ready: http://localhost:8080 ==="
echo "Press Ctrl+C to stop"

wait $DOCKER_PID 2>/dev/null
cleanup
