#!/bin/bash
# Build the yt2srt Docker image.
# Run download-cache.sh first to download the model and create .build-config.
#
#   ./download-cache.sh               # one-time cache fill
#   ./docker-build.sh                 # Go build + Docker image
#   ./docker-build.sh --rebuild-base  # force rebuild of base image too
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CACHE_DIR="${SCRIPT_DIR}/.docker-cache"
BUILD_CONFIG="${CACHE_DIR}/.build-config"
BASE_IMAGE="yt2srt-base:latest"
SERVER_IMAGE="${IMAGE:-yt2srt:latest}"

# ── parse flags ──────────────────────────────────────────────────────────
REBUILD_BASE=false
SERVER_ARGS=()
while [ $# -gt 0 ]; do
    case "$1" in
        --rebuild-base) REBUILD_BASE=true; shift ;;
        --no-cache)     SERVER_ARGS+=("--no-cache"); shift ;;
        *)              shift ;;
    esac
done

# ── read build config ────────────────────────────────────────────────────
if [ ! -f "${BUILD_CONFIG}" ]; then
    echo "ERROR: ${BUILD_CONFIG} not found — run download-cache.sh first" >&2
    echo "  e.g.: ./download-cache.sh" >&2
    exit 1
fi
source "${BUILD_CONFIG}"

if [ -z "${ASR_ENGINE:-}" ] || [ -z "${ASR_MODEL:-}" ]; then
    echo "ERROR: .build-config must set ASR_ENGINE and ASR_MODEL" >&2
    exit 1
fi

echo "=== yt2srt: Docker Build ==="
echo "Engine:   ${ASR_ENGINE}"
echo "Model:    ${ASR_MODEL}"
echo "Base:     ${BASE_IMAGE}"
echo "Server:   ${SERVER_IMAGE}"
echo ""

# ── validate cache ───────────────────────────────────────────────────────
if [ ! -d "${CACHE_DIR}" ]; then
    echo "ERROR: ${CACHE_DIR} not found — run download-cache.sh first" >&2
    exit 1
fi

case "${ASR_MODEL}" in
    sense-voice-2024|sense-voice-v1)
        if [ ! -f "${CACHE_DIR}/sense-voice-2024/model.int8.onnx" ]; then
            echo "ERROR: model files missing in ${CACHE_DIR}/sense-voice-2024/" >&2
            echo "Run ./download-cache.sh" >&2
            exit 1
        fi
        ;;
    paraformer-zh)
        if [ ! -f "${CACHE_DIR}/paraformer-zh/model.int8.onnx" ]; then
            echo "ERROR: model files missing in ${CACHE_DIR}/paraformer-zh/" >&2
            echo "Run ./download-cache.sh paraformer-zh" >&2
            exit 1
        fi
        ;;
esac

# ── build base image (if needed) ─────────────────────────────────────────
BASE_EXISTS=$(docker images -q "$BASE_IMAGE" 2>/dev/null || true)

if [ -z "$BASE_EXISTS" ] || [ "$REBUILD_BASE" = true ]; then
    echo "==> Building base image (${ASR_ENGINE}/${ASR_MODEL})..."
    docker build \
        --pull \
        -f "$SCRIPT_DIR/Dockerfile.base" \
        -t "$BASE_IMAGE" \
        "$SCRIPT_DIR"
    echo "==> Base image built: $BASE_IMAGE"
else
    echo "==> Base image already exists: $BASE_IMAGE  (use --rebuild-base to force)"
fi

# ── build Go binary natively ─────────────────────────────────────────────
echo "==> Building Go binary (native linux/amd64)..."
cd "$SCRIPT_DIR"
GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o yt2srt ./cmd/server
echo "   binary: $(ls -lh yt2srt | awk '{print $5}')"

# ── build server image ───────────────────────────────────────────────────
echo "==> Building server image: $SERVER_IMAGE..."
docker build \
    -t "$SERVER_IMAGE" \
    --build-arg "CACHE_BUST=$(date +%s)" \
    ${SERVER_ARGS[@]+"${SERVER_ARGS[@]}"} \
    "$SCRIPT_DIR"

rm -f "$SCRIPT_DIR/yt2srt"

echo ""
echo "=== Done ==="
echo "docker run --rm -p 8080:8080 $SERVER_IMAGE"
echo ""
echo "# Or with docker compose:"
echo "docker compose up"
