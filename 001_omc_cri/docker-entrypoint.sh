#!/bin/sh
# Resolve absolute tool paths — chineseasr.New stat()s -ffmpeg/-sherpa, so bare
# names ("ffmpeg") would fail validation. command -v returns the absolute path.
set -eu

FFMPEG="$(command -v ffmpeg || true)"
[ -n "$FFMPEG" ] || { echo "FATAL: ffmpeg not found on PATH" >&2; exit 1; }

SHERPA="$(command -v sherpa-onnx-offline || true)"
[ -n "$SHERPA" ] || { echo "FATAL: sherpa-onnx-offline not found on PATH (install sherpa-onnx-bin)" >&2; exit 1; }

MODEL_DIR="${MODEL_DIR:-/opt/model}"
[ -f "$MODEL_DIR/model.int8.onnx" ] || { echo "FATAL: $MODEL_DIR/model.int8.onnx missing" >&2; exit 1; }

GSE_DICT="${GSE_DICT:-/opt/gse-dict}"
CEDICT="${CEDICT:-/opt/cedict_ts.u8}"

echo "criradio: ffmpeg=$FFMPEG sherpa=$SHERPA model=$MODEL_DIR gse-dict=$GSE_DICT cedict=$CEDICT channel=${CHANNEL_URL:-default} delay=${DELAY:-180s}" >&2

exec criradio-server \
  -addr "${ADDR:-:8080}" \
  -ffmpeg "$FFMPEG" \
  -sherpa "$SHERPA" \
  -model-dir "$MODEL_DIR" \
  -gse-dict "$GSE_DICT" \
  -cedict "$CEDICT" \
  -channel-url "${CHANNEL_URL:-https://sk.cri.cn/905.m3u8}" \
  -delay "${DELAY:-180s}" \
  "$@"
