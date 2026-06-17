#!/bin/bash
# Build the CRI radio server Docker image using the local download cache.
#
# Run download-cache.sh first to populate ~/tmp/docker-cache/, then call this
# script instead of bare `docker build`. All extra args are forwarded to docker
# for the server build.
#
# Usage:
#   ./download-cache.sh          # one-time cache fill (re-run when model version changes)
#   ./docker-build.sh            # build (auto-builds base if missing)
#   ./docker-build.sh --no-cache # force full server rebuild
#   ./docker-build.sh --rebuild-base  # force rebuild of base image too
#
# Without the cache, the image pulls model/wheels/dict from the internet on
# every build (~200 MB model). With the cache those layers are instant.

set -eu

# Use BuildKit if docker buildx is available; fall back to classic builder otherwise.
if docker buildx version &>/dev/null; then
    export DOCKER_BUILDKIT=1
    USE_BUILDKIT=true
else
    echo "==> BuildKit/buildx not available — falling back to classic builder"
    export DOCKER_BUILDKIT=0
    USE_BUILDKIT=false
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CACHE_DIR="${CACHE_DIR:-$HOME/tmp/docker-cache}"
BASE_IMAGE="criradio-base:latest"
SERVER_IMAGE="${IMAGE:-criradio-server}"

# ── parse flags ──────────────────────────────────────────────────────────────
REBUILD_BASE=false
SERVER_ARGS=()
while [ $# -gt 0 ]; do
    case "$1" in
        --rebuild-base)
            REBUILD_BASE=true
            shift ;;
        *)
            SERVER_ARGS+=("$1")
            shift ;;
    esac
done

# ── stage cache ──────────────────────────────────────────────────────────────
if [ ! -d "$CACHE_DIR" ]; then
    echo "==> Cache dir $CACHE_DIR does not exist — running download-cache.sh first…"
    "$SCRIPT_DIR/download-cache.sh"
fi

echo "==> Staging cache files into .docker-cache/ …"
rm -rf "$SCRIPT_DIR/.docker-cache"
mkdir -p "$SCRIPT_DIR/.docker-cache"

# Model tarball
MODEL_FILE=$(echo "$CACHE_DIR"/*.tar.bz2 | head -1)
if [ -f "$MODEL_FILE" ]; then
    cp "$MODEL_FILE" "$SCRIPT_DIR/.docker-cache/"
else
    echo "ERROR: no model .tar.bz2 found in $CACHE_DIR" >&2
    exit 1
fi

# CC-CEDICT dictionary
if [ -f "$CACHE_DIR/cedict_1_0_ts_utf-8_mdbg.zip" ]; then
    cp "$CACHE_DIR/cedict_1_0_ts_utf-8_mdbg.zip" "$SCRIPT_DIR/.docker-cache/"
else
    echo "ERROR: cedict_1_0_ts_utf-8_mdbg.zip not found in $CACHE_DIR" >&2
    exit 1
fi

# Wheels directory
if [ -d "$CACHE_DIR/wheels" ]; then
    cp -r "$CACHE_DIR/wheels" "$SCRIPT_DIR/.docker-cache/wheels"
else
    echo "ERROR: wheels/ not found in $CACHE_DIR" >&2
    exit 1
fi

# gse dictionaries
if [ -d "$CACHE_DIR/gse-dict" ]; then
    cp -r "$CACHE_DIR/gse-dict" "$SCRIPT_DIR/.docker-cache/gse-dict"
else
    echo "ERROR: gse-dict/ not found in $CACHE_DIR (re-run download-cache.sh)" >&2
    exit 1
fi

# ── build base (if needed) ───────────────────────────────────────────────────
BASE_EXISTS=$(docker images -q "$BASE_IMAGE" 2>/dev/null || true)
if [ -z "$BASE_EXISTS" ] || [ "$REBUILD_BASE" = true ]; then
    if [ "$REBUILD_BASE" = true ]; then
        echo "==> Force-rebuilding base image: $BASE_IMAGE …"
    else
        echo "==> Base image not found — building $BASE_IMAGE …"
    fi
    docker build \
        -f "$SCRIPT_DIR/Dockerfile.base" \
        -t "$BASE_IMAGE" \
        "$SCRIPT_DIR"
    echo "==> Base image built: $BASE_IMAGE"
else
    echo "==> Base image already exists: $BASE_IMAGE  (use --rebuild-base to force)"
fi

# ── drop .docker-cache/ so the server build context stays lean ────────────────
# (the server Dockerfile does COPY . . in its build stage — we don't want the
# 200 MB model tarball to bloat the build context or the Go layer)
rm -rf "$SCRIPT_DIR/.docker-cache"

# ── build server ─────────────────────────────────────────────────────────────
echo "==> Building server image: $SERVER_IMAGE …"
docker build \
    -t "$SERVER_IMAGE" \
	${SERVER_ARGS[@]+"${SERVER_ARGS[@]}"} \
    "$SCRIPT_DIR"

echo ""
echo "Done. Run with:"
echo "  docker run --rm -p 8080:8080 $SERVER_IMAGE"
echo "  docker compose up"
