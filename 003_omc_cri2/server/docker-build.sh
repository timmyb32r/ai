#!/bin/bash
# Build the CRI Radio server Docker image.
# Run download-cache.sh first to download models and create .build-config.
#
#   ./download-cache.sh sense-voice-2024   # one-time cache fill
#   ./docker-build.sh                       # Go build + Docker image
#   ./docker-build.sh --rebuild-base        # force rebuild of base image too
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CACHE_DIR="${SCRIPT_DIR}/.docker-cache"
BUILD_CONFIG="${CACHE_DIR}/.build-config"
BASE_IMAGE="criradio-base:latest"
SERVER_IMAGE="${IMAGE:-criradio-server}"

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
    echo "  e.g.: ./download-cache.sh sense-voice-2024" >&2
    exit 1
fi
source "${BUILD_CONFIG}"

if [ -z "${ASR_ENGINE:-}" ] || [ -z "${ASR_MODEL:-}" ]; then
    echo "ERROR: .build-config must set ASR_ENGINE and ASR_MODEL" >&2
    exit 1
fi

echo "=== CRI Radio: Docker Build ==="
echo "Engine:   ${ASR_ENGINE}"
echo "Model:    ${ASR_MODEL}"
echo "Dict:     ${DICT:-bkrs} (BKRS_PATH=${BKRS_PATH:-/opt/dabkrs.gz})"
echo "Base:     ${BASE_IMAGE}"
echo "Server:   ${SERVER_IMAGE}"
echo ""

# ── validate cache ───────────────────────────────────────────────────────
if [ ! -d "${CACHE_DIR}" ]; then
    echo "ERROR: ${CACHE_DIR} not found — run download-cache.sh first" >&2
    exit 1
fi

# Required data files — the build MUST NOT proceed without them, otherwise the
# image silently ships without a dictionary / Unihan readings and features
# (e.g. single-char pinyin fill) break at runtime.
REQUIRED_FILES=(
    "cedict_ts.u8"
    "Unihan_Readings.txt"
    "gse-dict/zh/s_1.txt"
    "gse-dict/zh/t_1.txt"
    "gse-dict/zh/stop_word.txt"
)
# BKRS dump is required only when running in bkrs mode (the default).
if [ "${DICT:-bkrs}" = "bkrs" ]; then
    REQUIRED_FILES+=("dabkrs.gz")
fi

MISSING=()
for f in "${REQUIRED_FILES[@]}"; do
    [ -s "${CACHE_DIR}/${f}" ] || MISSING+=("$f")
done
if [ ${#MISSING[@]} -gt 0 ]; then
    echo "ERROR: required cache files missing in ${CACHE_DIR}:" >&2
    for f in "${MISSING[@]}"; do echo "  - ${f}" >&2; done
    echo "Run ./download-cache.sh to fetch them, then re-run this script." >&2
    exit 1
fi

# ── build base image (if needed) ─────────────────────────────────────────
BASE_EXISTS=$(docker images -q "$BASE_IMAGE" 2>/dev/null || true)

# A cached base image may predate a newly-added required data file (e.g. the
# Unihan readings): the file lives in the base layer, so an old base silently
# ships without it and the feature no-ops at runtime. Probe the cached base for
# the required /opt files and force a rebuild if any are missing.
if [ -n "$BASE_EXISTS" ] && [ "$REBUILD_BASE" = false ]; then
    BASE_CHECK_PATHS="/opt/Unihan_Readings.txt /opt/cedict_ts.u8 /opt/gse-dict/zh/s_1.txt"
    if [ "${DICT:-bkrs}" = "bkrs" ]; then
        BASE_CHECK_PATHS="${BASE_CHECK_PATHS} /opt/dabkrs.gz"
    fi
    if ! docker run --rm --entrypoint sh "$BASE_IMAGE" -c \
        "for f in ${BASE_CHECK_PATHS}; do [ -s \"\$f\" ] || exit 1; done" >/dev/null 2>&1; then
        echo "==> Cached base image is missing required data files — forcing base rebuild"
        REBUILD_BASE=true
    fi
fi

if [ -z "$BASE_EXISTS" ] || [ "$REBUILD_BASE" = true ]; then
    echo "==> Building base image (${ASR_ENGINE}/${ASR_MODEL})..."
    docker build \
        --pull \
        -f "$SCRIPT_DIR/Dockerfile.base" \
        --build-arg "ASR_ENGINE=${ASR_ENGINE}" \
        --build-arg "ASR_MODEL=${ASR_MODEL}" \
        -t "$BASE_IMAGE" \
        "$SCRIPT_DIR"
    echo "==> Base image built: $BASE_IMAGE"
else
    echo "==> Base image already exists: $BASE_IMAGE  (use --rebuild-base to force)"
fi

# ── build Go binary natively ─────────────────────────────────────────────
echo "==> Building Go binary (native)..."
cd "$SCRIPT_DIR"
GOTOOLCHAIN=local GOOS=linux CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o criradio-server ./cmd/server
echo "   binary: $(ls -lh criradio-server | awk '{print $5}')"

# ── build server image ───────────────────────────────────────────────────
echo "==> Building server image: $SERVER_IMAGE..."
docker build \
    -t "$SERVER_IMAGE" \
    --build-arg "CACHE_BUST=$(date +%s)" \
    ${SERVER_ARGS[@]+"${SERVER_ARGS[@]}"} \
    "$SCRIPT_DIR"

rm -f "$SCRIPT_DIR/criradio-server"

echo ""
echo "=== Done ==="
echo "docker run --rm -p 8080:8080 -e ASR_ENGINE=${ASR_ENGINE} -e ASR_MODEL=${ASR_MODEL} $SERVER_IMAGE"
