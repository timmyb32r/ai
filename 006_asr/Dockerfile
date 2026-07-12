# Server image: Go binary (compiled by docker-build.sh on host) layered onto base.
# No golang image or cross-compilation needed — fast rebuild when code changes.
#
#   ./download-cache.sh              # one-time cache fill
#   ./docker-build.sh                # Go build (native) + Docker image

FROM yt2srt-base:latest
ARG CACHE_BUST
COPY yt2srt /usr/local/bin/yt2srt
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
