#!/bin/bash
# Entrypoint: validates prerequisites, then launches server.
set -e

echo "=== yt2srt ==="
echo "Engine:    ${ENGINE:-sherpa-onnx}"
echo "Model:     ${ASR_MODEL:-sense-voice-2024}"
echo "Model Path: ${MODEL_PATH:-/opt/models}"
echo "Mode:      ${DEPLOY_MODE:-local}"
echo "Addr:      ${ADDR:-:8080}"
echo ""

# ── Validate sherpa-onnx ─────────────────────────────────────────────────
if ! command -v sherpa-onnx-offline >/dev/null 2>&1; then
    echo "ERROR: sherpa-onnx-offline not found in PATH" >&2
    exit 1
fi
echo "sherpa-onnx-offline: $(sherpa-onnx-offline --help 2>&1 | head -1 || echo 'installed')"

# ── Validate model path ──────────────────────────────────────────────────
MODEL_DIR="${MODEL_PATH}/${ASR_MODEL:-sense-voice-2024}"
if [ ! -d "${MODEL_DIR}" ]; then
    echo "ERROR: model directory not found at ${MODEL_DIR}" >&2
    exit 1
fi
echo "Model dir:  $(ls "${MODEL_DIR}" 2>/dev/null | tr '\n' ' ')"

# ── Validate ffmpeg ──────────────────────────────────────────────────────
if ! command -v ffmpeg >/dev/null 2>&1; then
    echo "ERROR: ffmpeg not found in PATH" >&2
    exit 1
fi

# ── Validate yt-dlp ──────────────────────────────────────────────────────
if ! command -v yt-dlp >/dev/null 2>&1; then
    echo "ERROR: yt-dlp not found in PATH" >&2
    exit 1
fi

echo ""
echo "Starting server on ${ADDR:-:8080}..."
exec /usr/local/bin/yt2srt
