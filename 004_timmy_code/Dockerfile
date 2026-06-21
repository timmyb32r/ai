# Stage 1: Build
FROM timmy-code-base:latest AS builder
WORKDIR /build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o timmy-code ./cmd/timmy-code

# Stage 2: Runtime
FROM alpine:latest
RUN apk add --no-cache ca-certificates git go
WORKDIR /work
COPY --from=builder /build/timmy-code /usr/local/bin/timmy-code
ARG COMMIT_HASH=unknown
LABEL com.timmy.commit-hash="${COMMIT_HASH}"
ENTRYPOINT ["/usr/local/bin/timmy-code"]
