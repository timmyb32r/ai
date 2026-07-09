#!/bin/bash
# Download models, dictionary, and gse into .docker-cache/ for offline Docker build.
# Run ONCE before docker-build.sh.  Pass the short model name as the only argument.
#
# Prerequisites: curl, tar, bzip2, unzip
#   sudo apt-get install -y curl bzip2 unzip  # Debian/Ubuntu
#
# Usage:
#   ./download-cache.sh sense-voice-2024   # sherpa-onnx + SenseVoice
#   ./download-cache.sh ggml-small          # whisper.cpp + ggml-small
#   ./download-cache.sh ggml-large          # whisper.cpp + ggml-large-v3
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CACHE_DIR="${SCRIPT_DIR}/.docker-cache"

MODEL="${1:-}"
DICT_ONLY=false
if [ "${MODEL}" = "--dict-only" ]; then
    DICT_ONLY=true
    MODEL=""
fi

if [ -z "${MODEL}" ] && [ "${DICT_ONLY}" = false ]; then
    echo "USAGE: $0 <model-short-name>" >&2
    echo "   or: $0 --dict-only         (download only dictionaries: CC-CEDICT + BKRS + gse)" >&2
    echo "" >&2
    echo "sherpa-onnx models:" >&2
    echo "  sense-voice-2024      SenseVoice Small (zh/en/ja/ko/yue, int8, ~140MB)" >&2
    echo "  sense-voice-v1         Same model, alias" >&2
    echo "  paraformer-zh          Paraformer (zh, int8)" >&2
    echo "  sherpa-whisper-small   Whisper via sherpa-onnx (ONNX runtime)" >&2
    echo "" >&2
    echo "whisper.cpp (ggml) models:" >&2
    echo "  ggml-tiny              whisper tiny (~77MB)" >&2
    echo "  ggml-small             whisper small (~488MB)" >&2
    echo "  ggml-medium            whisper medium (~1.5GB)" >&2
    echo "  ggml-large             whisper large-v3 (~3.1GB)" >&2
    exit 1
fi

mkdir -p "${CACHE_DIR}"
cd "${CACHE_DIR}"

if [ "${DICT_ONLY}" = true ]; then
    echo "=== CRI Radio: Download Cache (dictionaries only) ==="
    echo "Cache:  ${CACHE_DIR}"
    echo ""
else
    # Determine engine from model name
    case "${MODEL}" in
        sense-voice-2024|sense-voice-v1|paraformer-zh|sherpa-whisper-small|sherpa-whisper)
            ENGINE="sherpa-onnx" ;;
        ggml-*)
            ENGINE="whisper" ;;
        *)
            echo "ERROR: unknown model '${MODEL}'" >&2
            exit 1
            ;;
    esac

    echo "=== CRI Radio: Download Cache ==="
    echo "Model:  ${MODEL}"
    echo "Engine: ${ENGINE}"
    echo "Cache:  ${CACHE_DIR}"
    echo ""

    # ── Download model ────────────────────────────────────────────────────────

    case "${MODEL}" in
        sense-voice-2024|sense-voice-v1)
            DIR="sense-voice-2024"
            URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2"
            if [ -f "${DIR}/model.int8.onnx" ] && [ -f "${DIR}/tokens.txt" ]; then
                echo "==> SenseVoice int8 already cached"
            else
                echo "==> Downloading SenseVoice int8 (~140MB)..."
                rm -rf "${DIR}"
                mkdir -p "${DIR}"
                curl -L --progress-bar "${URL}" -o /tmp/sense-voice.tar.bz2
                tar xjf /tmp/sense-voice.tar.bz2 -C "${DIR}" --strip-components=1
                rm /tmp/sense-voice.tar.bz2
                echo "   ✓ model.int8.onnx ($(du -h "${DIR}/model.int8.onnx" | cut -f1))"
            fi
            ;;

        paraformer-zh)
            DIR="paraformer-zh"
            URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-paraformer-zh-2023-09-14.tar.bz2"
            if [ -f "${DIR}/model.int8.onnx" ]; then
                echo "==> Paraformer already cached"
            else
                echo "==> Downloading Paraformer..."
                rm -rf "${DIR}"
                mkdir -p "${DIR}"
                curl -L --progress-bar "${URL}" -o /tmp/paraformer.tar.bz2
                tar xjf /tmp/paraformer.tar.bz2 -C "${DIR}" --strip-components=1
                rm /tmp/paraformer.tar.bz2
                echo "   ✓ model.int8.onnx ($(du -h "${DIR}/model.int8.onnx" | cut -f1))"
            fi
            ;;

        sherpa-whisper-small|sherpa-whisper)
            DIR="sherpa-whisper"
            URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-whisper-small.tar.bz2"
            if [ -f "${DIR}/encoder.onnx" ]; then
                echo "==> Sherpa-whisper already cached"
            else
                echo "==> Downloading sherpa-onnx whisper-small..."
                rm -rf "${DIR}"
                mkdir -p "${DIR}"
                curl -L --progress-bar "${URL}" -o /tmp/sherpa-whisper.tar.bz2
                tar xjf /tmp/sherpa-whisper.tar.bz2 -C "${DIR}" --strip-components=1
                rm /tmp/sherpa-whisper.tar.bz2
                echo "   ✓ ${DIR}/"
            fi
            ;;

        ggml-tiny|ggml-small|ggml-medium)
            FILE="${MODEL}.bin"
            URL="https://huggingface.co/ggerganov/whisper.cpp/resolve/main/${FILE}"
            if [ -f "${FILE}" ]; then
                echo "==> ${FILE} already cached ($(du -h "${FILE}" | cut -f1))"
            else
                echo "==> Downloading ${FILE}..."
                curl -L --progress-bar "${URL}" -o "${FILE}"
                echo "   ✓ ${FILE} ($(du -h "${FILE}" | cut -f1))"
            fi
            ;;

        ggml-large)
            FILE="ggml-large-v3.bin"
            URL="https://huggingface.co/ggerganov/whisper.cpp/resolve/main/${FILE}"
            if [ -f "${FILE}" ]; then
                echo "==> ${FILE} already cached ($(du -h "${FILE}" | cut -f1))"
            else
                echo "==> Downloading ${FILE} (~3.1GB)..."
                curl -L --progress-bar "${URL}" -o "${FILE}"
                echo "   ✓ ${FILE} ($(du -h "${FILE}" | cut -f1))"
            fi
            ;;
    esac
fi

# ── CC-CEDICT dictionary ──────────────────────────────────────────────────
CEDICT_FILE="cedict_ts.u8"
if [ -f "${CEDICT_FILE}" ]; then
    echo "==> CC-CEDICT already cached ($(wc -l < "${CEDICT_FILE}") entries)"
else
    echo "==> Downloading CC-CEDICT dictionary..."
    curl -L --progress-bar \
        "https://www.mdbg.net/chinese/export/cedict/cedict_1_0_ts_utf-8_mdbg.zip" \
        -o /tmp/cedict.zip
    unzip -o /tmp/cedict.zip -d /tmp/cedict-extract/ >/dev/null
    cp /tmp/cedict-extract/cedict_ts.u8 .
    rm -rf /tmp/cedict.zip /tmp/cedict-extract
    echo "   ✓ cedict_ts.u8 ($(wc -l < "${CEDICT_FILE}") entries)"
fi

# ── BKRS (大БКРС) Chinese-Russian dictionary ────────────────────────────────
# Daily raw dump from bkrs.info (plain UTF-8, no password).
# Try today's date first, fall back to yesterday.
BKRS_FILE="dabkrs.gz"
if [ -f "${BKRS_FILE}" ]; then
    echo "==> BKRS dump already cached ($(du -h "${BKRS_FILE}" | cut -f1))"
else
    TODAY=$(date -u +%y%m%d)
    YESTERDAY=$(date -u -d "yesterday" +%y%m%d 2>/dev/null || date -u -v-1d +%y%m%d)
    BKRS_URL="https://bkrs.info/downloads/daily/dabkrs_${TODAY}.gz"
    echo "==> Downloading BKRS daily dump (trying ${TODAY})..."
    if ! curl -fL --progress-bar "${BKRS_URL}" -o "${BKRS_FILE}" 2>/dev/null; then
        BKRS_URL="https://bkrs.info/downloads/daily/dabkrs_${YESTERDAY}.gz"
        echo "==> Today's dump not available, trying yesterday (${YESTERDAY})..."
        curl -fL --progress-bar "${BKRS_URL}" -o "${BKRS_FILE}"
    fi
    echo "   ✓ dabkrs.gz ($(du -h "${BKRS_FILE}" | cut -f1))"
fi

# ── Unihan readings (probable pinyin for ambiguous single characters) ──────
# Unicode UCD Unihan.zip → Unihan_Readings.txt. Provides kHanyuPinlu frequency
# data used to fill single-character "?" readings with the most common one.
UNIHAN_FILE="Unihan_Readings.txt"
if [ -f "${UNIHAN_FILE}" ]; then
    echo "==> Unihan readings already cached ($(wc -l < "${UNIHAN_FILE}") lines)"
else
    echo "==> Downloading Unihan database..."
    if curl -fL --progress-bar "https://www.unicode.org/Public/UCD/latest/ucd/Unihan.zip" -o /tmp/unihan.zip 2>/dev/null; then
        unzip -o /tmp/unihan.zip Unihan_Readings.txt -d . >/dev/null
        rm -f /tmp/unihan.zip
        echo "   ✓ Unihan_Readings.txt ($(wc -l < "${UNIHAN_FILE}") lines)"
    else
        echo "   ! Unihan download failed — single-char '?' fill will be disabled" >&2
    fi
fi

# ── gse dictionaries ──────────────────────────────────────────────────────
if [ -d "gse-dict" ] && [ -f "gse-dict/zh/s_1.txt" ]; then
    echo "==> gse dictionaries already cached"
else
    echo "==> Downloading gse Chinese segmentation dictionaries..."
    mkdir -p gse-dict/zh
    GSE_BASE="https://raw.githubusercontent.com/go-ego/gse/v1.0.3/data/dict"
    for f in zh/s_1.txt zh/t_1.txt zh/stop_word.txt; do
        curl -L --progress-bar "${GSE_BASE}/${f}" -o "gse-dict/${f}"
    done
    echo "   ✓ gse-dict/zh/ ($(ls gse-dict/zh/ | wc -l) files)"
fi

# ── Write build config for docker-build.sh ────────────────────────────────
if [ "${DICT_ONLY}" = true ] && [ -f "${CACHE_DIR}/.build-config" ]; then
    # Preserve existing ASR_ENGINE/ASR_MODEL, update BKRS_PATH
    ENGINE=$(grep ASR_ENGINE= "${CACHE_DIR}/.build-config" | cut -d= -f2)
    MODEL=$(grep ASR_MODEL= "${CACHE_DIR}/.build-config" | cut -d= -f2)
    cat > "${CACHE_DIR}/.build-config" <<EOF
ASR_ENGINE=${ENGINE}
ASR_MODEL=${MODEL}
BKRS_PATH=/opt/dabkrs.gz
EOF
else
    cat > "${CACHE_DIR}/.build-config" <<EOF
ASR_ENGINE=${ENGINE}
ASR_MODEL=${MODEL}
BKRS_PATH=/opt/dabkrs.gz
EOF
fi

# ── Write .env for docker-compose (static vars only; ASR is baked into image) ─
cat > "${SCRIPT_DIR}/.env" <<EOF
MODEL_PATH=/opt/models
EOF

echo ""
if [ "${DICT_ONLY}" = true ]; then
    echo "=== Done! Dictionaries cached ==="
    echo "Next: ./docker-build.sh --rebuild-base"
else
    echo "=== Done! ${ENGINE} / ${MODEL} ==="
    echo "Next: ./docker-build.sh --rebuild-base"
fi
