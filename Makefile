# Meridian - build, test and release.
#
#   make            web UI + panel + agents for this machine's OS (panel) and Linux (agents)
#   make test       Go tests (race detector) + UI type check
#   make check      test + vet + staticcheck + govulncheck + npm audit
#   make release    tarballs for linux/amd64 and linux/arm64 in dist/release
#   make dev        panel on :18080 with a throwaway data dir, UI dev server on :5173

VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)
GO      ?= go
LDFLAGS  = -s -w
PANEL_LD = $(LDFLAGS) -X meridian/internal/panel.Version=$(VERSION)
AGENT_LD = $(LDFLAGS) -X meridian/internal/agent.Version=$(VERSION)
GOFLAGS  = -trimpath
TOOLS   ?= $(shell $(GO) env GOPATH)/bin

.PHONY: all web panel agents test check vet lint vuln audit docs release dev clean

all: web panel agents

web: web/node_modules
	cd web && npm run build

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	@touch web/node_modules

panel: web
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(PANEL_LD)" -o dist/meridian ./cmd/meridian

agents:
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build $(GOFLAGS) -ldflags "$(AGENT_LD)" \
			-o dist/meridian-agent-linux-$$arch ./cmd/meridian-agent || exit 1; \
	done

test:
	MERIDIAN_NO_GEO_DOWNLOAD=1 $(GO) test -race -count=1 ./...
	cd web && npm run typecheck

vet:
	$(GO) vet ./...
	GOOS=linux $(GO) vet ./...

lint:
	@command -v staticcheck >/dev/null 2>&1 || $(GO) install honnef.co/go/tools/cmd/staticcheck@latest
	PATH="$(TOOLS):$$PATH" staticcheck ./...
	PATH="$(TOOLS):$$PATH" GOOS=linux staticcheck ./...

vuln:
	@command -v govulncheck >/dev/null 2>&1 || $(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	PATH="$(TOOLS):$$PATH" govulncheck ./...
	PATH="$(TOOLS):$$PATH" GOOS=linux govulncheck ./...

audit:
	cd web && npm audit --audit-level=low

check: test vet lint vuln audit

# the subscription formats checked by the real clients' own parsers (test/formats/README.md); the
# first run downloads the pinned clients
.PHONY: formats
formats:
	eval "$$(bash test/formats/fetch-clients.sh --env)" && MERIDIAN_NO_GEO_DOWNLOAD=1 $(GO) test -count=1 \
		-run 'TestFormatsInRealClients|TestShareLinksRoundTrip' -v ./internal/panel

# docs/openapi.json is generated from the code - never edit it by hand
docs:
	$(GO) run -ldflags "-X meridian/internal/panel.Version=$(VERSION)" ./cmd/meridian openapi > docs/openapi.json

release: web agents
	rm -rf dist/release && mkdir -p dist/release
	for arch in amd64 arm64; do \
		dir=dist/release/meridian-$(VERSION)-linux-$$arch; \
		mkdir -p $$dir/agent; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build $(GOFLAGS) -ldflags "$(PANEL_LD)" -o $$dir/meridian ./cmd/meridian || exit 1; \
		cp dist/meridian-agent-linux-amd64 dist/meridian-agent-linux-arm64 $$dir/agent/; \
		cp deploy/install-panel.sh README.md SECURITY.md LICENSE $$dir/ || exit 1; \
		COPYFILE_DISABLE=1 tar --no-xattrs -C dist/release -czf dist/release/meridian-$(VERSION)-linux-$$arch.tar.gz meridian-$(VERSION)-linux-$$arch || exit 1; \
	done
	# the same binary on its own, for a Linux desktop: the MCP stdio bridge (meridian mcp) and backups.
	# Rosélune runs on Linux only - no other systems are built or supported.
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build $(GOFLAGS) -ldflags "$(PANEL_LD)" \
			-o dist/release/meridian-cli-$(VERSION)-linux-$$arch ./cmd/meridian || exit 1; \
	done
	cd dist/release && (command -v sha256sum >/dev/null && sha256sum *.tar.gz meridian-cli-* || shasum -a 256 *.tar.gz meridian-cli-*) > SHA256SUMS
	# panels install a release only with this signature (internal/update); the key stays off the repo
	$(GO) run ./cmd/meridian-sign -key "$${MERIDIAN_SIGNING_KEY:-$$HOME/.config/meridian/release-signing.key}" dist/release/SHA256SUMS
	@echo "release files in dist/release"

dev: agents
	cd web && npm run build
	CGO_ENABLED=0 $(GO) build -o dist/meridian ./cmd/meridian
	mkdir -p .dev
	MERIDIAN_NO_GEO_DOWNLOAD=1 MERIDIAN_ADMIN_PASSWORD=$${MERIDIAN_ADMIN_PASSWORD:-change-me-now} \
		dist/meridian serve --listen 127.0.0.1:18080 --data .dev --agent-dir dist & \
		cd web && npm run dev

clean:
	rm -rf dist/meridian dist/release web/dist/assets .dev
