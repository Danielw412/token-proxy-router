# Token Proxy Router build targets. See docs/operations.md.
GO          ?= go
BIN         ?= tpr
DIST        ?= dist
PANEL_OUT   := internal/tpr/webui/dist/management.html
MODULE      := github.com/router-for-me/CLIProxyAPI/v8

VERSION     ?= $(shell git describe --tags --match 'v[0-9]*' --exclude '*upstream*' --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
UPSTREAM    := $(shell awk -F'|' '/^\| Server/ {gsub(/ /,"",$$4); print $$4}' UPSTREAM.md)
PANEL       := $(shell awk -F'|' '/^\| Panel/ {gsub(/ /,"",$$4); print $$4}' UPSTREAM.md)

LDFLAGS := -s -w \
	-X 'main.Version=$(VERSION)' -X 'main.Commit=$(COMMIT)' -X 'main.BuildDate=$(BUILD_DATE)' \
	-X '$(MODULE)/internal/tpr/update.UpstreamVersion=$(UPSTREAM)' \
	-X '$(MODULE)/internal/tpr/update.PanelVersion=$(PANEL)'

# Packages upstream owns that carry a TPR hook.
TOUCHED := ./cmd/server ./internal/api/... ./internal/managementasset

PLATFORMS := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64

.PHONY: panel build check release-local clean

# Build the panel and copy it where go:embed picks it up.
panel:
	@if command -v bun >/dev/null 2>&1; then \
		cd panel && bun install --frozen-lockfile && VERSION='$(VERSION)' bun run build; \
	else \
		echo "bun not found, using npm"; \
		cd panel && npm install --no-audit --no-fund --no-package-lock && VERSION='$(VERSION)' npm run build; \
	fi
	cp panel/dist/index.html $(PANEL_OUT)

# Build ./tpr for this machine. It works without a panel build too, and then
# serves a page saying to run `make panel`.
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/server

check:
	@unformatted="$$(gofmt -l cmd internal/tpr)"; if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./internal/tpr/... $(TOUCHED)
	$(GO) test ./internal/tpr/... $(TOUCHED)
	cd panel && bun install --frozen-lockfile && bun run type-check && bun run test

# Everything a release contains, built locally into dist/.
release-local: panel
	rm -rf $(DIST) && mkdir -p $(DIST)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/tpr-$$os-$$arch$$ext ./cmd/server || exit 1; \
	done
	cd $(DIST) && for file in tpr-*; do gzip -9 -n -c "$$file" > "$$file.gz" || exit 1; done
	cp scripts/install.sh scripts/install.ps1 $(DIST)/
	cd $(DIST) && sha256sum tpr-* install.sh install.ps1 > checksums.txt

clean:
	rm -f $(BIN) $(BIN).exe
	rm -rf $(DIST)
