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

# Default mode
CLI_MODE="client-server"

# Parse --mode flag if present.
# The first argument to timmy can be --mode <mode> or the prompt.
if [ "$1" = "--mode" ]; then
    CLI_MODE="$2"
    shift 2
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

# Pick free ports on host.
get_free_port() {
    python3 -c "import socket; s=socket.socket(); s.bind(('',0)); print(s.getsockname()[1]); s.close()" 2>/dev/null \
    || echo 9876
}

HOST_VIEWER_PORT=$(get_free_port)
HOST_CONTROL_PORT=0

INTERNAL_VIEWER_PORT=9876
INTERNAL_CONTROL_PORT=9877

VIEWER_URL="http://localhost:${HOST_VIEWER_PORT}"

# Common Docker flags.
DOCKER_FLAGS="--rm -v \"$PWD:/work\" \
    -e DEEPSEEK_API_KEY \
    -e TIMKY_VIEW_PORT=${INTERNAL_VIEWER_PORT} \
    -e TIMKY_VIEWER_URL=${VIEWER_URL}"

if [ "$CLI_MODE" = "client-server" ]; then
    # Client-server mode: map both viewer and control ports.
    HOST_CONTROL_PORT=$(get_free_port)
    DOCKER_FLAGS="$DOCKER_FLAGS \
        -e TIMKY_CONTROL_PORT=${INTERNAL_CONTROL_PORT} \
        -p ${HOST_VIEWER_PORT}:${INTERNAL_VIEWER_PORT} \
        -p ${HOST_CONTROL_PORT}:${INTERNAL_CONTROL_PORT}"

    echo "[timmy] Starting server in client-server mode..."
    echo "[timmy] Control port: ${HOST_CONTROL_PORT}  |  Log viewer: ${VIEWER_URL}"

    # Launch Docker in background.
    if [ $# -gt 0 ]; then
        printf '%s\n' "$*" | eval exec docker run ${DOCKER_FLAGS} -i --name timmy-server timmy-code --cli-mode client-server &
    elif [ -t 0 ]; then
        eval exec docker run ${DOCKER_FLAGS} -it --name timmy-server timmy-code --cli-mode client-server &
    else
        eval exec docker run ${DOCKER_FLAGS} -i --name timmy-server timmy-code --cli-mode client-server &
    fi

    DOCKER_PID=$!

    # Wait for control port to be ready.
    echo "[timmy] Waiting for server to be ready..."
    for i in $(seq 1 30); do
        if nc -z localhost ${HOST_CONTROL_PORT} 2>/dev/null; then
            break
        fi
        sleep 0.5
    done

    # Launch the rich UI client.
    TIMKY_CLIENT_BIN="${TIMKY_HOME}/bin/timmy-client"
    if [ -x "$TIMKY_CLIENT_BIN" ]; then
        exec "$TIMKY_CLIENT_BIN" --connect "localhost:${HOST_CONTROL_PORT}"
    elif command -v timmy-client >/dev/null 2>&1; then
        exec timmy-client --connect "localhost:${HOST_CONTROL_PORT}"
    else
        echo "[timmy] timmy-client binary not found." >&2
        echo "[timmy] Server is running. Connect with: nc localhost ${HOST_CONTROL_PORT}" >&2
        echo "[timmy] Or build the client: cd ${TIMKY_HOME} && make build-client" >&2
        wait $DOCKER_PID
    fi
else
    # Pipes mode: current behavior.
    DOCKER_FLAGS="$DOCKER_FLAGS -p ${HOST_VIEWER_PORT}:${INTERNAL_VIEWER_PORT}"

    if [ $# -gt 0 ]; then
        printf '%s\n' "$*" | eval exec docker run ${DOCKER_FLAGS} -i timmy-code --cli-mode pipes
    elif [ -t 0 ]; then
        eval exec docker run ${DOCKER_FLAGS} -it timmy-code --cli-mode pipes
    else
        eval exec docker run ${DOCKER_FLAGS} -i timmy-code --cli-mode pipes
    fi
fi
