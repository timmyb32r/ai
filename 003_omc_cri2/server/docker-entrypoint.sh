#!/bin/bash
# Entrypoint: validates prerequisites, creates output directories, launches server.
set -e

echo "=== CRI Radio Server ==="
echo "ASR Engine: ${ASR_ENGINE:-whisper}"
echo "ASR Model:  ${ASR_MODEL:-ggml-small}"
echo "Model Path: ${MODEL_PATH:-/opt/models}"
echo "Dictionary: ${CEDICT_PATH} ($(wc -l < "${CEDICT_PATH}" 2>/dev/null || echo 'N/A') entries)"
echo "Channel:    ${CHANNEL_URL}"
echo "Output:     ${OUTPUT_DIR}"
echo "Addr:       ${ADDR}"
echo ""

# ── Validate ASR engine binary ────────────────────────────────────────────
ASR_ENGINE="${ASR_ENGINE:-whisper}"
case "${ASR_ENGINE}" in
    whisper)
        if ! command -v whisper-cli >/dev/null 2>&1; then
            echo "ERROR: whisper-cli not found in PATH. Set ASR_ENGINE=sherpa-onnx or install whisper.cpp." >&2
            exit 1
        fi
        echo "whisper-cli: $(whisper-cli --version 2>&1 | head -1 || echo 'installed')"
        ;;
    sherpa-onnx)
        if ! command -v sherpa-onnx-offline >/dev/null 2>&1; then
            echo "ERROR: sherpa-onnx-offline not found in PATH. Set ASR_ENGINE=whisper or install sherpa-onnx." >&2
            exit 1
        fi
        echo "sherpa-onnx-offline: $(sherpa-onnx-offline --help 2>&1 | head -1 || echo 'installed')"
        ;;
    *)
        echo "ERROR: Unknown ASR_ENGINE '${ASR_ENGINE}'. Valid: whisper, sherpa-onnx" >&2
        exit 1
        ;;
esac

# ── Validate model path ──────────────────────────────────────────────────
if [ ! -d "${MODEL_PATH}" ] && [ ! -f "${MODEL_PATH}" ]; then
    echo "ERROR: MODEL_PATH ${MODEL_PATH} is neither a file nor a directory" >&2
    exit 1
fi
if [ -d "${MODEL_PATH}" ]; then
    echo "Model dir:  $(ls "${MODEL_PATH}" 2>/dev/null | tr '\n' ' ')"
else
    echo "Model file: ${MODEL_PATH} ($(du -h "${MODEL_PATH}" 2>/dev/null | cut -f1 || echo 'N/A'))"
fi

# ── Validate dictionary ───────────────────────────────────────────────────
if [ ! -f "${CEDICT_PATH}" ]; then
    echo "ERROR: CC-CEDICT dictionary not found at ${CEDICT_PATH}" >&2
    exit 1
fi

# ── Validate ffmpeg ──────────────────────────────────────────────────────
if ! command -v ffmpeg >/dev/null 2>&1; then
    echo "ERROR: ffmpeg not found in PATH" >&2
    exit 1
fi

# ── Create output directories ─────────────────────────────────────────────
mkdir -p "${OUTPUT_DIR}/hls" "${OUTPUT_DIR}/metadata"

echo ""
echo "Starting server on ${ADDR}..."
exec /usr/local/bin/criradio-server
