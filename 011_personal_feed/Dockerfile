FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /personal-feed ./cmd/personal-feed

FROM debian:bookworm-slim
# Bootstrap HTTPS trust before the minimal image installs ca-certificates.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::Retries=3 -o APT::Update::Error-Mode=any update \
    && apt-get -o Acquire::Retries=3 install -y --no-install-recommends chromium ca-certificates fonts-dejavu-core fonts-noto-cjk tini \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home feed && mkdir /data && chown feed:feed /data
COPY --from=build /personal-feed /usr/local/bin/personal-feed
WORKDIR /app
USER feed
EXPOSE 8080
ENTRYPOINT ["/usr/bin/tini", "--", "personal-feed"]
CMD ["-config", "/app/config.yaml"]
