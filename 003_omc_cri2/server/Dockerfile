# Server image: Go binary (compiled by docker-build.sh on host) layered onto base.
# No golang image or cross-compilation needed.
#
#   ./download-cache.sh              # one-time cache fill
#   ./docker-build.sh                # Go build (native) + Docker image

FROM criradio-base:latest
COPY criradio-server /usr/local/bin/criradio-server
