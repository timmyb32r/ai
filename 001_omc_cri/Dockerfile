# syntax=docker/dockerfile:1
#
# Self-contained image for the CRI-radio live-subtitle server.
# Bakes in: the server binary + ffmpeg (libmp3lame/HLS) + sherpa-onnx-offline
# (+ onnxruntime) + the SenseVoice int8 model. One `docker run` starts everything.
#
#   docker build -t criradio-server .
#   docker run --rm -p 8080:8080 criradio-server
#
# The only network egress at runtime is the radio HLS URL (-e CHANNEL_URL=...).

############################################
# Stage 1 — build the static server binary
############################################
ARG GO_VERSION=1.24
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
# Module is stdlib-only (no external deps to download).
COPY . .
# Static, CGo-free binary => no libc dependency in the runtime image.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/criradio-server ./cmd/server

############################################
# Stage 2 — runtime: ffmpeg + sherpa + model
############################################
FROM python:3.12-slim-bookworm AS runtime

# ffmpeg (incl. libmp3lame for MP3 + HLS/https demuxing), TLS roots, fetch tools.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates wget bzip2 \
 && rm -rf /var/lib/apt/lists/*

# SenseVoice int8 model (zh/en/ja/ko/yue). Baked in so the image is self-contained.
# Placed before the pip layer (it's the most stable input) for better build caching.
# Override the version with: --build-arg MODEL_NAME=sherpa-onnx-sense-voice-...-<date>
ARG MODEL_NAME=sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17
RUN mkdir -p /opt/model \
 && wget -qO /tmp/model.tar.bz2 \
      "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/${MODEL_NAME}.tar.bz2" \
 && tar xjf /tmp/model.tar.bz2 -C /opt/model --strip-components=1 \
 && rm /tmp/model.tar.bz2 \
 && test -f /opt/model/model.int8.onnx \
 && test -f /opt/model/tokens.txt

# The C++ CLI binaries (sherpa-onnx-offline + onnxruntime .so) live in the
# `sherpa-onnx-bin` package (the plain `sherpa-onnx` package is Python bindings
# only and does NOT provide the CLI). pip installs them onto /usr/local/bin.
# `command -v` fails the build early if the CLI didn't land on PATH.
RUN pip install --no-cache-dir sherpa-onnx-bin \
 && command -v sherpa-onnx-offline

COPY --from=build /out/criradio-server /usr/local/bin/criradio-server
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Runtime knobs — override any with `docker run -e NAME=value`.
ENV ADDR=:8080 \
    MODEL_DIR=/opt/model \
    CHANNEL_URL=https://sk.cri.cn/905.m3u8 \
    DELAY=180s

EXPOSE 8080

# /v1/status responds even when idle (state "stopped"), so it's a good liveness probe.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:8080/v1/status" >/dev/null 2>&1 || exit 1

# Entrypoint resolves absolute tool paths (chineseasr.New stat()s them) and maps
# ENV -> server flags. Extra args after the image name are passed through.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
