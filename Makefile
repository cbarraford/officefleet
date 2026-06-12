.PHONY: web web-clean build test vet lint

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

# Build the SPA into internal/web/dist (only .gitkeep is tracked there).
web: web/node_modules
	cd web && npm run build

web/node_modules: web/package-lock.json
	cd web && npm ci
	@touch web/node_modules

# Wipe build output back to the committed .gitkeep (hashed assets accumulate
# because vite runs with emptyOutDir: false).
web-clean:
	find internal/web/dist -mindepth 1 ! -name .gitkeep -delete

build: web
	go build -ldflags "$(LDFLAGS)" -o fleet ./cmd/fleet

test: web/node_modules
	go test ./...
	cd web && npm run check

vet:
	go vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found; falling back to go vet ./..."; \
		go vet ./...; \
	fi
