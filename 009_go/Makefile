.PHONY: build test race bench clean

GOCACHE := /tmp/transferia-go-cache
PGO ?= auto

build:
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -trimpath -pgo=$(PGO) -ldflags='-s -w' -o bin/transferia ./cmd/transferia

test:
	GOCACHE=$(GOCACHE) go test ./...
	GOCACHE=$(GOCACHE) go vet ./...

race:
	GOCACHE=$(GOCACHE) go test -race ./internal/parser ./internal/clickhouse ./internal/pipeline

bench:
	GOCACHE=$(GOCACHE) go test ./internal/parser ./internal/clickhouse -run '^$$' -bench . -benchmem

clean:
	rm -f bin/transferia
