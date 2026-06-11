.PHONY: web web-clean build test

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
	go build -o fleet ./cmd/fleet

test: web/node_modules
	go test ./...
	cd web && npm run check
