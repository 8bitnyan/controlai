BINARY         := controlai
INGEST_BINARY  := controlai-ingest
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS        := -ldflags "-X controlai/internal/version.Version=$(VERSION)"
GOFLAGS        :=

.PHONY: build build-ingest test lint vet tidy integration clean test-aws-https

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

# Integration test for the HTTPS API endpoint on a live AWS deployment.
# Requires: DEPLOYMENT_NAME, AWS_REGION (reads state from deploy/aws/.state/<DEPLOYMENT_NAME>.json).
# Optional: BEARER_TOKEN (auto-created/revoked if unset), ALLOW_STAGING=--allow-staging.
test-aws-https:
	@bash deploy/aws/test/test_https_api.sh $(ALLOW_STAGING)

.DEFAULT_GOAL := build
