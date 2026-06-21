# Stage 1: Build
FROM golang:1.25-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o timmy-code ./cmd/timmy-code

# Stage 2: Runtime
FROM alpine:latest
RUN apk add --no-cache ca-certificates git
WORKDIR /work
COPY --from=builder /build/timmy-code /usr/local/bin/timmy-code
ENTRYPOINT ["/usr/local/bin/timmy-code"]
