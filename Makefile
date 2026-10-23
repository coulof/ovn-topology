SHELL := /usr/bin/env bash
VERSION ?= 0.1.0-dev
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -w -s -X main.version=$(VERSION) -X main.gitCommit=$(GIT_COMMIT) -X main.buildDate=$(BUILD_DATE)

.PHONY: all
all: generate lint test build

.PHONY: generate
generate:
	@echo "==> Running codegen..."
	./hack/generate.sh

.PHONY: lint
lint:
	@echo "==> Running golangci-lint (enforcing read-only and depguard rules)..."
	golangci-lint run ./...
	@echo "==> Running ADR-0001 dependency check..."
	./hack/check-deps.sh

.PHONY: test
test:
	@echo "==> Running Go unit and in-memory tests..."
	go test -v -race ./pkg/... ./cmd/...

.PHONY: build
build:
	@echo "==> Building daemon and TUI binaries..."
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/ovn-topologyd ./cmd/ovn-topologyd
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o bin/ovn-topo ./cmd/ovn-topo

.PHONY: image
image:
	@echo "==> Building container image (distroless, non-root)..."
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t ghcr.io/coulof/ovn-topologyd:$(VERSION) .

.PHONY: dev-cluster
dev-cluster:
	@echo "==> Setting up local dev cluster..."
	./hack/dev-cluster.sh
