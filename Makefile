# Null Drive — developer tasks. Run `make help` for a list.
#
# Requirements: Go 1.26+, Node 22+ (UI), golangci-lint (lint), curl + jq (smoke).

BINARY   := bin/nulldrive
PKG      := github.com/Null0Grid/null-drive/server/internal/buildinfo
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS  := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT)
UI       := ui
IMAGE    ?= nulldrive
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: help dev dev-api dev-ui run build build-go ui-install ui-build windows msix wine-test icons \
        test test-go test-ui test-race e2e screenshots lint fmt smoke e2ee-ref deb docker-offline clean \
        docker docker-multiarch docker-run lint-docker lint-actions \
        client-dev client-build client-test

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

# ---------- development ----------

dev: ui-install ## Go API on :8080 + Vite UI with hot reload on :5173 (open that one)
	@echo "→ open http://localhost:5173  (API: http://localhost:8080)"
	@$(MAKE) -j2 --no-print-directory dev-api dev-ui

dev-api:
	NULLDRIVE_LOG_LEVEL=debug go run ./server/cmd/nulldrive

dev-ui:
	cd $(UI) && npm run dev

run: build ## Build everything and run the single binary on :8080
	./$(BINARY)

# ---------- build ----------

ui-install: ## Install UI dependencies (only when package-lock.json changed)
	@cd $(UI) && { [ -d node_modules ] && [ node_modules/.package-lock.json -nt package-lock.json ]; } || npm ci

ui-build: ui-install ## Build the UI into server/internal/webui/dist (embedded by go build)
	cd $(UI) && npm run build

build: ui-build build-go ## Build the UI, then a static server binary with the UI embedded

build-go: ## Build only the Go binary (embeds whatever UI build is present)
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./server/cmd/nulldrive
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/nulldrive-e2ee ./cmd/nulldrive-e2ee

# ---------- quality ----------

test: test-go test-ui ## Go tests + UI unit/component tests

test-go: ## Go unit + integration tests
	go test ./...

test-ui: ui-install ## Vitest unit + component tests
	cd $(UI) && npm test

test-race: ## Go tests with the race detector (needs CGO)
	go test -race -count=1 ./...

e2e: build ## Playwright against the real binary: wizard, login, files, pairing, live updates
	cd $(UI) && npx playwright test --project=e2e

screenshots: build ## Regenerate docs/screenshots/*.png
	cd $(UI) && npx playwright test --project=screenshots

lint: ui-install ## go vet + golangci-lint + svelte-check
	go vet ./...
	golangci-lint run ./...
	cd $(UI) && npm run check

fmt: ## Format Go code
	gofmt -s -w server shared

smoke: build ## curl walkthrough of the API against a fresh data dir
	./scripts/smoke.sh $(BINARY)

e2ee-ref: build ## Python E2EE reference client: test vectors + live flow (needs pynacl)
	./scripts/e2ee-reference/run.sh $(BINARY)

# ---------- desktop client (needs the Wails CLI: go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2) ----------

client-dev: ## Desktop app with hot reload (wails dev)
	cd client && wails dev

windows: ## Windows installer + portable zip + winget manifests in dist/ (cross-built from any OS; needs makensis)
	scripts/build-windows.sh

msix: ## Microsoft Store package (MSIX) in dist/, after `make windows` (needs MakeAppx or makemsix)
	scripts/build-msix.sh

wine-test: ## Installer + nulldrive:// links under Wine, after `make windows` (needs wine, xvfb-run)
	scripts/wine-test.sh

icons: ## Regenerate every logo, app icon, tray icon and installer image (needs Pillow)
	python3 scripts/gen-icons.py

client-build: ## Build the desktop app for this OS into client/build/bin
	cd client && wails build -trimpath -ldflags "-X main.version=$(VERSION)"

client-test: ## Desktop frontend type check + tests (sync engine tests run in `make test-go`)
	cd client/frontend && npm ci && npm run check && npm test

# ---------- docker ----------

docker: ## Build the image for this machine's architecture (loaded into local Docker)
	docker buildx build --load --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-multiarch: ## Build amd64 + arm64 (no push; CI's release workflow pushes)
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(IMAGE):$(VERSION) .

docker-run: docker ## Build and run the image on :8080 with a named volume
	docker run --rm -it --name nulldrive -p 8080:8080 -v nulldrive-data:/data $(IMAGE):latest

docker-offline: ## amd64 + arm64 image tarballs in dist/ without pulling base images (after build-release.sh)
	./scripts/build-image-offline.sh $(VERSION)

deb: ## Debian/Ubuntu packages (amd64, arm64) in dist/ (after build-release.sh)
	./scripts/build-deb.sh $(VERSION)

lint-docker: ## hadolint the Dockerfile
	hadolint Dockerfile

lint-actions: ## actionlint the GitHub workflows
	actionlint .github/workflows/*.yml

clean: ## Remove build output and local data
	rm -rf bin data $(UI)/test-results $(UI)/playwright-report $(UI)/.e2e-data
	find server/internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
