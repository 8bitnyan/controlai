BINARY         := controlai
INGEST_BINARY  := controlai-ingest
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS        := -ldflags "-X controlai/internal/version.Version=$(VERSION)"
GOFLAGS        :=

.PHONY: build build-ingest test lint vet tidy integration clean

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/controlai

build-ingest:
	go build $(LDFLAGS) -o $(INGEST_BINARY) ./services/ingest

build-all: build build-ingest

test:
	go test $(GOFLAGS) ./...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

tidy:
	go mod tidy

integration:
	go test -tags integration -timeout 300s ./...

clean:
	rm -f $(BINARY) $(INGEST_BINARY)
	go clean -testcache

.DEFAULT_GOAL := build
