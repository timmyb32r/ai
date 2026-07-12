#!/bin/bash
# Download sense_voice model into .docker-cache/ for offline Docker build.
# Run ONCE before docker-build.sh.
#
# Usage:
#   ./download-cache.sh                  # default: sense-voice-2024
#   ./download-cache.sh paraformer-zh    # alternative model
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CACHE_DIR="${SCRIPT_DIR}/.docker-cache"
MODEL="${1:-sense-voice-2024}"

case "${MODEL}" in
    sense-voice-2024|sense-voice-v1)
        ENGINE="sherpa-onnx"
        DIR="sense-voice-2024"
        URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2"
        ;;
    paraformer-zh)
        ENGINE="sherpa-onnx"
        DIR="paraformer-zh"
        URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-paraformer-zh-2023-09-14.tar.bz2"
        ;;
    *)
        echo "ERROR: unknown model '${MODEL}'" >&2
        echo "Valid models: sense-voice-2024, sense-voice-v1, paraformer-zh" >&2
        exit 1
        ;;
esac

echo "=== yt2srt: Download Cache ==="
echo "Model:  ${MODEL}"
echo "Engine: ${ENGINE}"
echo "Cache:  ${CACHE_DIR}"
echo ""

mkdir -p "${CACHE_DIR}"

# ── Download model ────────────────────────────────────────────────────────
if [ "${DIR}" = "sense-voice-2024" ]; then
    if [ -f "${CACHE_DIR}/${DIR}/model.int8.onnx" ] && [ -f "${CACHE_DIR}/${DIR}/tokens.txt" ]; then
        echo "==> SenseVoice int8 already cached"
    else
        echo "==> Downloading SenseVoice int8 (~140MB)..."
        rm -rf "${CACHE_DIR}/${DIR}"
        mkdir -p "${CACHE_DIR}/${DIR}"
        curl -L --progress-bar "${URL}" -o /tmp/sense-voice.tar.bz2
        tar xjf /tmp/sense-voice.tar.bz2 -C "${CACHE_DIR}/${DIR}" --strip-components=1
        rm /tmp/sense-voice.tar.bz2
        echo "   ✓ model.int8.onnx ($(du -h "${CACHE_DIR}/${DIR}/model.int8.onnx" | cut -f1))"
    fi
elif [ "${DIR}" = "paraformer-zh" ]; then
    if [ -f "${CACHE_DIR}/${DIR}/model.int8.onnx" ]; then
        echo "==> Paraformer already cached"
    else
        echo "==> Downloading Paraformer..."
        rm -rf "${CACHE_DIR}/${DIR}"
        mkdir -p "${CACHE_DIR}/${DIR}"
        curl -L --progress-bar "${URL}" -o /tmp/paraformer.tar.bz2
        tar xjf /tmp/paraformer.tar.bz2 -C "${CACHE_DIR}/${DIR}" --strip-components=1
        rm /tmp/paraformer.tar.bz2
        echo "   ✓ model.int8.onnx ($(du -h "${CACHE_DIR}/${DIR}/model.int8.onnx" | cut -f1))"
    fi
fi

# ── Write build config for docker-build.sh ────────────────────────────────
cat > "${CACHE_DIR}/.build-config" <<EOF
ASR_ENGINE=${ENGINE}
ASR_MODEL=${MODEL}
EOF

echo ""
echo "=== Done! ${ENGINE} / ${MODEL} ==="
echo "Next: ./docker-build.sh"
