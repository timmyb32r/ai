# Server image: builds the Go binary and layers it onto criradio-base.
# This is the image you rebuild every time Go code changes — it's thin and fast.
#
# Prerequisites:
#   ./download-cache.sh                                    # one-time
#   docker build -f Dockerfile.base -t criradio-base:latest .  # one-time
#
# Then:
#   ./docker-build.sh                                      # fast: Go build + copy

ARG GO_VERSION=1.24
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

# Cache module downloads — invalidates only when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build. BuildKit cache mount keeps Go build cache across rebuilds.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/criradio-server ./cmd/server

# ── Final image: just the binary on top of the base ──────────────────────────
FROM criradio-base:latest
COPY --from=build /out/criradio-server /usr/local/bin/criradio-server
