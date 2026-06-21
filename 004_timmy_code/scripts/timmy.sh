#!/bin/sh
set -e

# --- TIMKY_HOME (substituted by make install) ---
TIMKY_HOME="__TIMKY_HOME__"

# Check required environment variable
if [ -z "$DEEPSEEK_API_KEY" ]; then
    echo "Error: DEEPSEEK_API_KEY is not set." >&2
    echo "Please set it: export DEEPSEEK_API_KEY='your-api-key'" >&2
    exit 1
fi

# --- Smart rebuild logic ---
need_rebuild() {
    # 1. Image doesn't exist
    if ! docker image inspect timmy-code:latest >/dev/null 2>&1; then
        return 0
    fi
    # 2. Uncommitted changes in source dirs
    if [ -n "$(git -C "$TIMKY_HOME" status --porcelain -- cmd/ internal/ go.mod go.sum 2>/dev/null)" ]; then
        return 0
    fi
    # 3. Commit hash mismatch
    IMAGE_HASH=$(docker image inspect --format '{{index .Config.Labels "com.timmy.commit-hash"}}' timmy-code:latest 2>/dev/null || echo "none")
    REPO_HASH=$(git -C "$TIMKY_HOME" rev-parse HEAD 2>/dev/null || echo "none")
    if [ "$IMAGE_HASH" != "$REPO_HASH" ]; then
        return 0
    fi
    return 1
}

build_if_needed() {
    if need_rebuild; then
        echo "[timmy] Rebuilding timmy-code..."
        # Ensure base image exists
        if ! docker image inspect timmy-code-base:latest >/dev/null 2>&1; then
            echo "[timmy] Building base image..."
            docker build -f "$TIMKY_HOME/Dockerfile.base" -t timmy-code-base "$TIMKY_HOME"
        fi
        HASH=$(git -C "$TIMKY_HOME" rev-parse HEAD 2>/dev/null || echo "unknown")
        docker build --build-arg COMMIT_HASH="$HASH" -t timmy-code "$TIMKY_HOME"
        echo "[timmy] Rebuild complete."
    fi
}

build_if_needed

# Pick a free port on host for the raw log viewer web UI.
get_free_port() {
    python3 -c "import socket; s=socket.socket(); s.bind(('',0)); print(s.getsockname()[1]); s.close()" 2>/dev/null \
    || echo 9876
}

HOST_PORT=$(get_free_port)
INTERNAL_PORT=9876
VIEWER_URL="http://localhost:${HOST_PORT}"

# Common Docker flags
DOCKER_FLAGS="--rm -v \"$PWD:/work\" \
    -e DEEPSEEK_API_KEY \
    -e TIMKY_VIEW_PORT=${INTERNAL_PORT} \
    -e TIMKY_VIEWER_URL=${VIEWER_URL} \
    -p ${HOST_PORT}:${INTERNAL_PORT}"

if [ $# -gt 0 ]; then
    printf '%s\n' "$*" | eval exec docker run ${DOCKER_FLAGS} -i timmy-code
elif [ -t 0 ]; then
    eval exec docker run ${DOCKER_FLAGS} -it timmy-code
else
    eval exec docker run ${DOCKER_FLAGS} -i timmy-code
fi
