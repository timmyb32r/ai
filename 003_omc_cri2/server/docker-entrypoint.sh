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
DICT="${DICT:-bkrs}"
case "${DICT}" in
    bkrs)
        if [ ! -f "${BKRS_PATH}" ]; then
            echo "ERROR: BKRS dictionary not found at ${BKRS_PATH}" >&2
            exit 1
        fi
        echo "BKRS dump:  ${BKRS_PATH} ($(du -h "${BKRS_PATH}" 2>/dev/null | cut -f1 || echo 'N/A'))"
        ;;
    cedict)
        if [ ! -f "${CEDICT_PATH}" ]; then
            echo "ERROR: CC-CEDICT dictionary not found at ${CEDICT_PATH}" >&2
            exit 1
        fi
        echo "CEDICT:     ${CEDICT_PATH} ($(wc -l < "${CEDICT_PATH}" 2>/dev/null || echo 'N/A') entries)"
        ;;
    wiktionary)
        if [ ! -f "${WIKTIONARY_PATH}" ]; then
            echo "ERROR: Wiktionary JSONL dump not found at ${WIKTIONARY_PATH}" >&2
            exit 1
        fi
        echo "Wiktionary: ${WIKTIONARY_PATH} ($(du -h "${WIKTIONARY_PATH}" 2>/dev/null | cut -f1 || echo 'N/A'))"
        ;;
    *)
        echo "ERROR: Unknown DICT '${DICT}'. Valid: bkrs, cedict, wiktionary" >&2
        exit 1
        ;;
esac

# ── Validate ffmpeg ──────────────────────────────────────────────────────
if ! command -v ffmpeg >/dev/null 2>&1; then
    echo "ERROR: ffmpeg not found in PATH" >&2
    exit 1
fi

# ── Create output directories ─────────────────────────────────────────────
mkdir -p "${OUTPUT_DIR}/hls" "${OUTPUT_DIR}/metadata"

# ── Start HanLP NLP tokenizer in background ───────────────────────────
# Unset HANLP_URL — HanLP library reads it as a download mirror URL.
HANLP_PORT="${HANLP_PORT:-8765}"
echo ""
echo "Starting HanLP tokenizer on :${HANLP_PORT}..."
env -u HANLP_URL python3 /usr/local/bin/hanlp_server.py "${HANLP_PORT}" &
HANLP_PID=$!

# Wait for HanLP to become ready.
echo "Waiting for HanLP to become ready..."
for i in $(seq 1 60); do
    if python3 -c "
import urllib.request, json
try:
    req = urllib.request.Request(
        'http://127.0.0.1:${HANLP_PORT}/parse',
        data=json.dumps({'text':'测试'}).encode(),
        headers={'Content-Type':'application/json'}
    )
    urllib.request.urlopen(req, timeout=5)
    exit(0)
except Exception:
    exit(1)
" 2>/dev/null; then
        echo "HanLP ready (attempt $i)"
        break
    fi
    sleep 2
done

# Verify HanLP is still running.
if ! kill -0 "${HANLP_PID}" 2>/dev/null; then
    echo "ERROR: HanLP failed to start" >&2
    exit 1
fi

echo ""
echo "Starting server on ${ADDR}..."
exec /usr/local/bin/criradio-server
