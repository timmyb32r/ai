#!/bin/bash
# Download all external assets to a local cache so Docker builds don't fetch
# them from the internet every time. Run this whenever the model version changes
# or you want to refresh the CC-CEDICT dictionary.
#
# Usage:
#   ./download-cache.sh                        # use defaults
#   MODEL_NAME=sherpa-onnx-... ./download-cache.sh  # override model
#   CACHE_DIR=/other/path ./download-cache.sh  # override cache location
#
# After caching, build with:
#   ./docker-build.sh

set -eu

CACHE_DIR="${CACHE_DIR:-$HOME/tmp/docker-cache}"
MODEL_NAME="${MODEL_NAME:-sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17}"
GSE_VERSION="v1.0.2"

# ---------- helpers ----------
section() { echo ""; echo "=== $* ==="; }

download() {
    local url="$1" dst="$2" label="${3:-$(basename "$dst")}"
    if [ -f "$dst" ]; then
        echo "  ✓ already cached: $label"
    else
        echo "  ↓ $label …"
        curl -fL --progress-bar -o "$dst.part" "$url"
        mv "$dst.part" "$dst"
        echo "  ✓ cached → $dst"
    fi
}

# ---------- main ----------
mkdir -p "$CACHE_DIR"

# 1 — Sherpa-onnx SenseVoice model ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
section "Sherpa-onnx model: $MODEL_NAME"
MODEL_URL="https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/${MODEL_NAME}.tar.bz2"
download "$MODEL_URL" "$CACHE_DIR/${MODEL_NAME}.tar.bz2" "$MODEL_NAME"

# 2 — CC-CEDICT Chinese-English dictionary ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
section "CC-CEDICT dictionary"
CEDICT_URL="https://www.mdbg.net/chinese/export/cedict/cedict_1_0_ts_utf-8_mdbg.zip"
CEDICT_FILE="$CACHE_DIR/cedict_1_0_ts_utf-8_mdbg.zip"
if [ -f "$CEDICT_FILE" ]; then
    echo "  ✓ already cached: cedict_1_0_ts_utf-8_mdbg.zip"
else
    echo "  ↓ cedict_1_0_ts_utf-8_mdbg.zip …"
    curl -fL --progress-bar \
        --user-agent "Mozilla/5.0" \
        -o "$CEDICT_FILE.part" \
        "$CEDICT_URL"
    mv "$CEDICT_FILE.part" "$CEDICT_FILE"
    echo "  ✓ cached → $CEDICT_FILE"
fi

# 3 — sherpa-onnx-bin pip wheels (via docker for correct linux platform) ~~~~~~
section "sherpa-onnx-bin wheels"
WHEELS_DIR="$CACHE_DIR/wheels"

if [ -f "$WHEELS_DIR/.done" ]; then
    echo "  ✓ wheels already cached (found .done marker)"
else
    echo "  ↓ downloading sherpa-onnx-bin + deps (via docker python:3.12-slim-bookworm) …"
    mkdir -p "$WHEELS_DIR"
    docker run --rm \
        -v "$WHEELS_DIR:/wheels" \
        python:3.12-slim-bookworm \
        pip download --dest /wheels sherpa-onnx-bin
    touch "$WHEELS_DIR/.done"
    echo "  ✓ wheels cached → $WHEELS_DIR"
fi

# 4 — gse Chinese word segmentation dictionaries ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~
section "gse dictionaries ($GSE_VERSION)"
GSE_DICT_DIR="$CACHE_DIR/gse-dict/zh"
GSE_BASE="https://raw.githubusercontent.com/go-ego/gse/${GSE_VERSION}/data/dict/zh"

mkdir -p "$GSE_DICT_DIR"
download "$GSE_BASE/s_1.txt"       "$GSE_DICT_DIR/s_1.txt"       "gse s_1.txt"
download "$GSE_BASE/t_1.txt"       "$GSE_DICT_DIR/t_1.txt"       "gse t_1.txt"
download "$GSE_BASE/stop_word.txt" "$GSE_DICT_DIR/stop_word.txt" "gse stop_word.txt"

# ---------------------------------------------------------------------------
section "Done"
echo "Cache dir : $CACHE_DIR"
echo "Model     : $MODEL_NAME"
echo "gse ver   : $GSE_VERSION"
echo ""
echo "Build with:"
echo "  ./docker-build.sh"
