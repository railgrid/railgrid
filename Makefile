.PHONY: sync-portalkit verify-portalkit verify-provider-contract verify-agentkit verify-ui-conformance verify-design-docs verify-tilt-browser-deployment test-portal test-portal-settings-conformance test-create-flow-conformance serve-model-form-visual test-model-form-visual build-portal test-macos-agent test-edges-provider test-edges-portal build-macos-agent build-macos-agent-arm64 build-macos-agent-amd64 build-macos-stub build-macos-stub-native build-macos-stub-arm64 build-macos-stub-amd64 verify-macos-edges
.PHONY: build-access-proxy docker-build-access-proxy
.PHONY: test-runner lint-runner fix-lint-runner build-runner build-runner-darwin build-runner-linux
.PHONY: dev-edge-create dev-run-edge build test lint lint-providers lint-provider-sdk fix-lint codegen crds clean certs dev-setup run-dex run-hub run-hub-static run-hub-embedded run-hub-embedded-static run-hub-standalone run-kcp dev-login dev-login-static dev-create-workload dev dev-infra dev-run-kcp path boilerplate verify-boilerplate verify-codegen ldflags tools docker-build docker-build-hub docker-build-agent docker-build-dex docker-build-dev-agent load-dev-agent-image docker-build-universal-dev-image load-universal-dev-image docker-push-dex verify help-dev dev-status dev-clean-hooks helm-build-local helm-push-local helm-clean build-quickstart-provider build-quickstart-provider-portal build-kuery-provider build-kuery-provider-portal run-provider-kuery kuery-db-up kuery-db-down install-provider-kuery init-provider-kuery uninstall-provider-kuery run-provider-quickstart install-provider-quickstart init-provider-quickstart uninstall-provider-quickstart build-infrastructure-provider build-infrastructure-provider-portal codegen-infrastructure-provider run-provider-infrastructure install-provider-infrastructure init-provider-infrastructure uninstall-provider-infrastructure build-app-studio-provider build-app-studio-provider-portal codegen-app-studio-provider app-studio-preview-bridge-dev-key verify-app-studio-preview-bridge-dev-key verify-app-studio-eval app-studio-db-up app-studio-db-down run-provider-app-studio install-provider-app-studio init-provider-app-studio uninstall-provider-app-studio build-agents-provider build-agents-provider-portal codegen-agents-provider agents-db-up agents-db-down run-provider-agents install-provider-agents init-provider-agents uninstall-provider-agents build-code-provider build-code-provider-portal codegen-code-provider run-provider-code install-provider-code init-provider-code uninstall-provider-code dev-kro-up dev-kro-down dev-kro-seed e2e-infrastructure e2e-provider e2e-provider-flags e2e-provider-all e2e-kuery-provider

BINDIR ?= bin
GOFLAGS ?=
TOOLSDIR := hack/tools
TOOLS_GOBIN_DIR := $(abspath $(TOOLSDIR))
GO_INSTALL := ./hack/go-install.sh

# --- Tool versions ---
DEX_VER := v2.41.1
DEX := $(TOOLSDIR)/dex-$(DEX_VER)

KCP_VER := v0.33.0
KCP := $(TOOLSDIR)/kcp-$(KCP_VER)
KCP_DATA_DIR := .kcp

# Browser/CLI address of every local hub (make run-hub-*, both Tiltfiles,
# `railgrid dev init`). Public DNS answers every *.127.0.0.1.sslip.io name with
# 127.0.0.1, so no /etc/hosts entry is needed, and published apps under
# apps.127.0.0.1.sslip.io share its site (private-app sign-in cookies stay
# first-party). certs/apiserver.crt covers *.127.0.0.1.sslip.io.
DEV_HUB_URL ?= https://console.127.0.0.1.sslip.io:9443

CONTROLLER_GEN_VER := v0.16.5
CONTROLLER_GEN_BIN := controller-gen
CONTROLLER_GEN := $(TOOLSDIR)/$(CONTROLLER_GEN_BIN)-$(CONTROLLER_GEN_VER)
export CONTROLLER_GEN

KCP_APIGEN_VER := v0.33.0
KCP_APIGEN_BIN := apigen
KCP_APIGEN_GEN := $(TOOLSDIR)/$(KCP_APIGEN_BIN)-$(KCP_APIGEN_VER)
export KCP_APIGEN_GEN

GOLANGCI_LINT_VER := v2.11.4
GOLANGCI_LINT_BIN := golangci-lint
GOLANGCI_LINT := $(TOOLSDIR)/$(GOLANGCI_LINT_BIN)-$(GOLANGCI_LINT_VER)

ACTIONLINT_VER := v1.7.7
ACTIONLINT := $(TOOLSDIR)/actionlint-$(ACTIONLINT_VER)

OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH := $(shell uname -m)
ifeq ($(ARCH),x86_64)
  ARCH := amd64
endif
ifeq ($(ARCH),aarch64)
  ARCH := arm64
endif

# --- Version info ---
# --match 'v*' excludes the provider-sdk/* submodule tags (e.g.
# provider-sdk/v0.0.12) that git describe would otherwise latch onto, keeping the
# version a real railgrid release tag (or a bare SHA when none is reachable).
VERSION ?= $(shell git describe --tags --always --dirty --match 'v*' 2>/dev/null || echo dev)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS_PKG := github.com/railgrid/railgrid/pkg/version
LDFLAGS := -s -w -X $(LDFLAGS_PKG).Version=$(VERSION) -X $(LDFLAGS_PKG).GitCommit=$(GIT_COMMIT) -X $(LDFLAGS_PKG).BuildDate=$(BUILD_DATE)

ldflags: ## Print ldflags for goreleaser
	@echo "$(LDFLAGS)"

all: build

build: build-railgrid build-hub

build-railgrid:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid ./cmd/railgrid/

## CLI reference docs (docs/cli/*.md) are generated from the cobra command
## tree so they can never drift from the binary. verify-docs-cli is part of
## `make verify`.
docs-cli: build-railgrid ## Regenerate docs/cli from the railgrid command tree
	$(BINDIR)/railgrid docs --dir docs/cli

verify-docs-cli: docs-cli ## Fail when docs/cli is out of date with the command tree
	@if [ -n "$$(git status --porcelain -- docs/cli)" ]; then \
		echo "docs/cli is out of date; run 'make docs-cli' and commit the result"; \
		git --no-pager status --short -- docs/cli; \
		exit 1; \
	fi

build-release: ## Build the release-tagging helper (release <component|all>)
	go build $(GOFLAGS) -o $(BINDIR)/release ./cmd/release/

build-hub:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-hub ./cmd/railgrid-hub/

test-runner: ## Run focused generic runner and harness tests
	go test -count=1 ./pkg/runner/... ./cmd/railgrid-runner/...
	python3 -m unittest discover -s hack/runner-install -p 'test_*.py'

lint-runner: $(GOLANGCI_LINT) ## Lint the standalone runner and adapters
	$(GOLANGCI_LINT) run ./pkg/runner/... ./cmd/railgrid-runner/...

fix-lint-runner: $(GOLANGCI_LINT) ## Format and auto-fix the standalone runner
	$(GOLANGCI_LINT) run --fix ./pkg/runner/... ./cmd/railgrid-runner/...

build-runner: ## Build the standalone loopback runner binary
	mkdir -p $(BINDIR)
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-runner ./cmd/railgrid-runner/

build-runner-darwin: ## Build the standalone runner for Darwin arm64 and amd64
	mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-runner-darwin-arm64 ./cmd/railgrid-runner/
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-runner-darwin-amd64 ./cmd/railgrid-runner/

build-runner-linux: ## Build the standalone runner for Linux arm64 and amd64
	mkdir -p $(BINDIR)
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-runner-linux-arm64 ./cmd/railgrid-runner/
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-runner-linux-amd64 ./cmd/railgrid-runner/

build-hub-portal: build-portal ## Build hub with embedded portal
	mkdir -p pkg/hub/portal
	rm -rf pkg/hub/portal/dist
	cp -r portal/dist pkg/hub/portal/dist
	go build $(GOFLAGS) -tags portal_embed -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-hub ./cmd/railgrid-hub/

build-portal: ## Build the portal Vue.js SPA
	cd portal && npm ci && npm run build

dev-portal: ## Run the portal dev server
	cd portal && npm run dev -- $(ARGS)


build-access-proxy: ## Build the published-app access-proxy binary (infrastructure module)
	cd providers/infrastructure && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/railgrid-access-proxy ./cmd/access-proxy/

# build-agent is an alias for build-railgrid: the agent container image now ships
# the railgrid CLI binary (cmd/railgrid/) with ENTRYPOINT [/railgrid, agent, run].
build-agent: build-railgrid

## macOS agent compile/test gates. These targets deliberately use the caller's
## GOCACHE/GOTMPDIR/TMPDIR so local and CI builds share the environment-provided
## disk-backed caches. They only build binaries; no release or publishing step is
## implied.
test-macos-agent: ## Run focused root agent/client/apiurl/CLI tests
	go test ./pkg/agent/... ./pkg/client ./pkg/apiurl ./pkg/cli/...

test-edges-provider: ## Run the complete standalone Edges provider test suite
	mkdir -p providers/edges/portal/dist && touch providers/edges/portal/dist/.gitkeep
	cd providers/edges && go test ./...

test-edges-portal: ## Run the Edges portal tests and TypeScript check
	cd providers/edges/portal && npm ci && npm test && npm run typecheck

build-macos-agent: build-macos-agent-arm64 build-macos-agent-amd64 ## Compile the agent for Darwin arm64 and amd64

build-macos-agent-arm64: ## Compile the agent for Darwin arm64
	mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-darwin-arm64 ./cmd/railgrid/

build-macos-agent-amd64: ## Compile the agent for Darwin amd64
	mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/railgrid-darwin-amd64 ./cmd/railgrid/

build-macos-stub-native: ## Compile the localhost-only health stub for the current platform
	mkdir -p $(BINDIR)
	go build $(GOFLAGS) -o $(BINDIR)/macos-stub ./hack/edges-macos/

build-macos-stub: build-macos-stub-arm64 build-macos-stub-amd64 ## Compile the health stub for downloadable Darwin architectures

build-macos-stub-arm64: ## Compile the health stub for Darwin arm64
	mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -o $(BINDIR)/macos-stub-darwin-arm64 ./hack/edges-macos/

build-macos-stub-amd64: ## Compile the health stub for Darwin amd64
	mkdir -p $(BINDIR)
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -o $(BINDIR)/macos-stub-darwin-amd64 ./hack/edges-macos/

verify-macos-edges: test-macos-agent test-edges-provider test-edges-portal build-macos-agent build-macos-stub ## Run all macOS Edges compile and focused verification gates

build-quickstart-provider-portal: ## Build the quickstart provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/quickstart/portal && npm install --no-audit --no-fund && npm run build

build-quickstart-provider: build-quickstart-provider-portal ## Build the quickstart reference provider binary (portal embedded)
	cd providers/quickstart && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/quickstart-provider .

build-kuery-provider-portal: ## Build the kuery provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/kuery/portal && npm install --no-audit --no-fund && npm run build

.PHONY: test-kuery-provider-portal
test-kuery-provider-portal: ## Run the Kuery portal regression suite and typecheck
	cd providers/kuery/portal && npm test && npm run typecheck

build-kuery-provider: build-kuery-provider-portal ## Build the kuery provider binary (portal embedded)
	cd providers/kuery && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/kuery-provider .

build-infrastructure-provider-portal: ## Build the infrastructure provider's micro-frontend (Vite + Vue → portal/dist)
	cd providers/infrastructure/portal && npm install --no-audit --no-fund && npm run build

build-infrastructure-provider: build-infrastructure-provider-portal ## Build the infrastructure provider binary (portal embedded)
	cd providers/infrastructure && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/infrastructure-provider .

build-edges-provider-portal: ## Build the edges provider's micro-frontend (Vite → portal/dist)
	cd providers/edges/portal && npm install --no-audit --no-fund && npm run build

build-edges-provider: build-edges-provider-portal ## Build the edges provider binary (portal embedded)
	cd providers/edges && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/edges-provider .

## Generate deepcopy + CRD YAML + kcp APIResourceSchemas for the edges provider's
## API (KubernetesCluster, LinuxServer, and MacOSServer in edges.railgrid.ai), then
## sync the schema bodies into the Helm chart's files/schemas/ directory.
## Provider init applies them at runtime so tenants that bind the APIExport get
## all connectable edge kinds.
codegen-edges-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the edges provider's local API (+ chart schemas)
	@mkdir -p providers/edges/config/crds providers/edges/config/kcp providers/edges/deploy/chart/files/schemas
	cd providers/edges && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./internal/edgeapi/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/edges/config/crds
	./hack/apigen.sh --input-dir providers/edges/config/crds --output-dir providers/edges/config/kcp
	@for r in kubernetesclusters linuxservers macosservers workloads placements services addons; do \
		cp providers/edges/config/kcp/apiresourceschema-$$r.edges.railgrid.ai.yaml \
		   providers/edges/deploy/chart/files/schemas/$$r.edges.railgrid.ai.yaml; \
	done
	@# One APIExport, generated: apigen supplies spec.resources, manifest.yaml
	@# supplies metadata.name (spec.export.name) and spec.permissionClaims (from
	@# spec.requires). The group-named file
	@# apigen leaves behind is deleted by the generator (it is not the export's
	@# name). --schemas-dir pins the resource list to the schemas the chart ships.
	cd provider-sdk && go run ./cmd/apiexportgen \
		--manifest $(CURDIR)/providers/edges/manifest.yaml \
		--apigen-export $(CURDIR)/providers/edges/config/kcp/apiexport-edges.railgrid.ai.yaml \
		--schemas-dir $(CURDIR)/providers/edges/deploy/chart/files/schemas \
		--out $(CURDIR)/providers/edges/config/kcp/apiexport-edges.providers.railgrid.ai.yaml
	cp providers/edges/config/kcp/apiexport-edges.providers.railgrid.ai.yaml providers/edges/deploy/chart/files/apiexport.yaml
	./hack/ensure-boilerplate.sh

.PHONY: codegen-quickstart-provider codegen-kuery-provider codegen-edges-provider build-edges-provider build-edges-provider-portal \
	install-provider-edges init-provider-edges run-provider-edges uninstall-provider-edges docker-build-edges-provider

## --- edges provider dev lifecycle (install → init → run) --------------------
install-provider-edges: ## Apply edges Provider + CatalogEntry into root:railgrid:providers
	@test -f $(EDGES_KCP_KUBECONFIG) || { echo "kubeconfig not found at $(EDGES_KCP_KUBECONFIG); start the hub first (make run-hub-embedded-static)"; exit 1; }
	kubectl --kubeconfig=$(EDGES_KCP_KUBECONFIG) \
		--server=$(EDGES_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(EDGES_PROVIDER_MANIFEST) -f $(EDGES_MANIFEST)

init-provider-edges: build-edges-provider ## Bootstrap edges APIExport + write dev runtime kubeconfig
	@test -f $(EDGES_KCP_KUBECONFIG) || { echo "kubeconfig not found at $(EDGES_KCP_KUBECONFIG); start the hub first"; exit 1; }
	@TOKEN=$$(kubectl --kubeconfig=$(EDGES_KCP_KUBECONFIG) \
		--server=$(EDGES_KCP_SERVER)/clusters/$(EDGES_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: railgrid\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: railgrid\n  context:\n    cluster: railgrid\n    user: railgrid\ncurrent-context: railgrid\nusers:\n- name: railgrid\n  user:\n    token: %s\n' \
		"$(EDGES_KCP_SERVER)/clusters/$(EDGES_WORKSPACE_PATH)" "$$TOKEN" \
		> $(EDGES_RUNTIME_KUBECONFIG)
	RAILGRID_PROVIDER_KUBECONFIG=$(EDGES_RUNTIME_KUBECONFIG) \
	RAILGRID_KCP_DIR=$(EDGES_KCP_DIR) \
	RAILGRID_DATAPLANE_URL=http://localhost:$(EDGES_PORT) \
	EDGES_WORKSPACE_PATH=$(EDGES_WORKSPACE_PATH) \
		$(BINDIR)/edges-provider init

# Replica identity for the tunnel-routing registry (multi-replica dev): every
# instance needs a distinct POD_NAME/EDGES_INTERNAL_PORT; POD_IP is loopback
# for host binaries. A second instance: make run-provider-edges
# EDGES_PORT=18088 EDGES_INTERNAL_PORT=18090 POD_NAME=edges-local-2
EDGES_INTERNAL_PORT ?= 8090
run-provider-edges: build-edges-provider ## Run the edges provider (needs: hub + install + init)
	@echo "Starting edges provider on :$(EDGES_PORT) (replica $${POD_NAME:-edges-local-1}, relay :$(EDGES_INTERNAL_PORT))"
	PORT=$(EDGES_PORT) \
	POD_NAME=$${POD_NAME:-edges-local-1} \
	POD_IP=$${POD_IP:-127.0.0.1} \
	EDGES_INTERNAL_PORT=$(EDGES_INTERNAL_PORT) \
	RAILGRID_HUB_URL=$(EDGES_HUB_URL) \
	RAILGRID_HUB_EXTERNAL_URL=$(EDGES_HUB_EXTERNAL_URL) \
	RAILGRID_HUB_INSECURE=true \
	RAILGRID_PROVIDER_NAME=edges \
	RAILGRID_CATALOGENTRY_FILE=$(CURDIR)/providers/edges/manifest.yaml \
	RAILGRID_PROVIDER_KUBECONFIG=$(EDGES_RUNTIME_KUBECONFIG) \
	RAILGRID_DEV_MODE=true \
		$(BINDIR)/edges-provider serve

uninstall-provider-edges: ## Delete edges CatalogEntry + Provider
	-kubectl --kubeconfig=$(EDGES_KCP_KUBECONFIG) \
		--server=$(EDGES_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(EDGES_MANIFEST) -f $(EDGES_PROVIDER_MANIFEST)

docker-build-edges-provider: ## Build the edges provider image (context = providers/edges)
	docker build \
		--platform $(DOCKER_PLATFORM) \
		-t ghcr.io/railgrid/railgrid-edges-provider:$(VERSION) \
		providers/edges

build-app-studio-provider-portal: ## Build the App Studio provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/app-studio/portal && npm install --no-audit --no-fund && npm run build

build-app-studio-provider: build-app-studio-provider-portal ## Build the App Studio provider binary (portal embedded)
	cd providers/app-studio && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/app-studio-provider .

build-agents-provider-portal: ## Build the agents provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/agents/portal && npm install --no-audit --no-fund && npm run build

build-agents-provider: build-agents-provider-portal ## Build the agents provider binary (portal embedded)
	cd providers/agents && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/agents-provider .

build-code-provider-portal: ## Build the code provider's micro-frontend (Vite + Vue → portal/dist)
	cd providers/code/portal && npm ci --include=dev --no-audit --no-fund && npm run build

build-code-provider: build-code-provider-portal ## Build the code provider binary (portal embedded)
	cd providers/code && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/code-provider .

test-hub-chart: ## Lint and render the railgrid-hub chart's provider hardening values
	@set -eu; \
		tmp_parent="$${CODEX_BUILD_CACHE_ROOT:-/var/tmp/codex-build}"; \
		mkdir -p "$$tmp_parent"; \
		tmp_dir="$$(mktemp -d "$$tmp_parent/railgrid-hub-chart.XXXXXX")"; \
		cleanup() { rm -rf -- "$$tmp_dir"; }; \
		trap cleanup EXIT HUP INT TERM; \
		chart=deploy/charts/railgrid-hub; url=https://railgrid.example.com; \
		helm lint "$$chart" --set hub.hubExternalURL="$$url"; \
		helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" >"$$tmp_dir/default.yaml"; \
		if grep -q -- '--provider-' "$$tmp_dir/default.yaml"; then \
			echo "default values rendered a provider hardening flag; they must leave the binary default"; exit 1; \
		fi; \
		helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" \
			--set hub.security.providerHeartbeatAuth=enforce \
			--set hub.security.providerDelegatedTokens=platform \
			--set 'hub.security.providerDelegatedTokensExclude={edges,mcp}' \
			--set hub.security.providerWorkspaceClusterAdmin=false >"$$tmp_dir/hardened.yaml"; \
		grep -q -- '- --provider-heartbeat-auth=enforce$$' "$$tmp_dir/hardened.yaml"; \
		grep -q -- '- --provider-delegated-tokens=platform$$' "$$tmp_dir/hardened.yaml"; \
		grep -q -- '- --provider-delegated-tokens-exclude=edges$$' "$$tmp_dir/hardened.yaml"; \
		grep -q -- '- --provider-delegated-tokens-exclude=mcp$$' "$$tmp_dir/hardened.yaml"; \
		grep -q -- '- --provider-workspace-cluster-admin=false$$' "$$tmp_dir/hardened.yaml"; \
		helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" \
			--set hub.security.providerWorkspaceClusterAdmin=true >"$$tmp_dir/admin-true.yaml"; \
		grep -q -- '- --provider-workspace-cluster-admin=true$$' "$$tmp_dir/admin-true.yaml"; \
		helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" \
			--set-string hub.security.providerWorkspaceClusterAdmin=false >"$$tmp_dir/admin-false-string.yaml"; \
		grep -q -- '- --provider-workspace-cluster-admin=false$$' "$$tmp_dir/admin-false-string.yaml"; \
		helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" \
			--set 'hub.extraArgs={--providers=edges\,infrastructure,--disable-token-login}' >"$$tmp_dir/extra.yaml"; \
		grep -q -- '- "--providers=edges,infrastructure"$$' "$$tmp_dir/extra.yaml"; \
		grep -q -- '- "--disable-token-login"$$' "$$tmp_dir/extra.yaml"; \
		if helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" --set hub.security.providerHeartbeatAuth=maybe >/dev/null 2>&1; then \
			echo "invalid providerHeartbeatAuth unexpectedly rendered"; exit 1; \
		fi; \
		if helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" --set hub.security.providerDelegatedTokens=some >/dev/null 2>&1; then \
			echo "invalid providerDelegatedTokens unexpectedly rendered"; exit 1; \
		fi; \
		if helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" --set hub.security.providerWorkspaceClusterAdmin=maybe >/dev/null 2>&1; then \
			echo "invalid providerWorkspaceClusterAdmin unexpectedly rendered"; exit 1; \
		fi; \
		if helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" --set hub.security.providerHubAccessPlatformDefault=maybe >/dev/null 2>&1; then \
			echo "invalid providerHubAccessPlatformDefault unexpectedly rendered"; exit 1; \
		fi; \
		helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" --set hub.security.providerHubAccessPlatformDefault=false | grep -q -- '--provider-hub-access-platform-default=false'; \
		if helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" --set 'hub.extraArgs={--dev-mode}' >/dev/null 2>&1; then \
			echo "extraArgs repeating a modelled flag unexpectedly rendered"; exit 1; \
		fi; \
		if helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" --set 'hub.extraArgs={--provider-heartbeat-auth=enforce}' >/dev/null 2>&1; then \
			echo "extraArgs repeating --provider-heartbeat-auth unexpectedly rendered"; exit 1; \
		fi; \
		if helm template railgrid "$$chart" --set hub.hubExternalURL="$$url" --set 'hub.extraArgs={--provider-delegated-tokens=platform}' >/dev/null 2>&1; then \
			echo "extraArgs repeating --provider-delegated-tokens unexpectedly rendered"; exit 1; \
		fi

## Generate deepcopy methods + CRD YAML for the infrastructure provider's
## own API types (providers/infrastructure/apis/v1alpha1/...). The CRDs land
## under providers/infrastructure/config/crds/ and are embedded into the
## binary via go:embed — the hub does not install them, the provider does
## (one of the deliberate self-contained-system properties).
codegen-infrastructure-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the infrastructure provider's local API (+ chart schemas)
	@mkdir -p providers/infrastructure/config/crds providers/infrastructure/config/kcp providers/infrastructure/deploy/chart/files/schemas
	cd providers/infrastructure && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/infrastructure/config/crds
	# The provider embeds its platform-facing CRDs from install/crds/ (//go:embed
	# in install/crds.go) and applies them into the kcp provider workspace at
	# init; the Templates CRD is also what the runtime-minted, virtual-storage
	# templates schema is derived from. Keep the embed copy in lockstep with the
	# generated CRDs. InfrastructureProvider is intentionally NOT embedded (it is
	# applied to the host cluster by the chart), so it stays in config/ only.
	# Remove stale embed files first so deleted platform APIs cannot remain
	# installed accidentally.
	find providers/infrastructure/install/crds -maxdepth 1 -type f -name '*.yaml' -delete
	cp providers/infrastructure/config/crds/infrastructure.railgrid.ai_templates.yaml \
	   providers/infrastructure/config/crds/infrastructure.railgrid.ai_instances.yaml \
	   providers/infrastructure/install/crds/
	./hack/apigen.sh --input-dir providers/infrastructure/config/crds --output-dir providers/infrastructure/config/kcp
	@for r in instances templates; do \
		cp providers/infrastructure/config/kcp/apiresourceschema-$$r.infrastructure.railgrid.ai.yaml \
		   providers/infrastructure/deploy/chart/files/schemas/$$r.infrastructure.railgrid.ai.yaml; \
	done
	@# One APIExport, generated: apigen supplies spec.resources, manifest.yaml
	@# supplies metadata.name (spec.export.name) and spec.permissionClaims (from
	@# spec.requires). --schemas-dir pins the
	@# resource list to the schemas the chart ships (the host-cluster
	@# InfrastructureProvider CRD is not one of them). At init the provider
	@# re-points the templates entry at CachedResource virtual storage; the
	@# instances entry and every instances/<verb> subresource are served as
	@# generated.
	cd provider-sdk && go run ./cmd/apiexportgen \
		--manifest $(CURDIR)/providers/infrastructure/manifest.yaml \
		--apigen-export $(CURDIR)/providers/infrastructure/config/kcp/apiexport-infrastructure.railgrid.ai.yaml \
		--schemas-dir $(CURDIR)/providers/infrastructure/deploy/chart/files/schemas \
		--out $(CURDIR)/providers/infrastructure/config/kcp/apiexport-infrastructure.providers.railgrid.ai.yaml
	cp providers/infrastructure/config/kcp/apiexport-infrastructure.providers.railgrid.ai.yaml providers/infrastructure/deploy/chart/files/apiexport.yaml
	./hack/ensure-boilerplate.sh

## Generate deepcopy + CRD YAML + kcp APIResourceSchemas for the code
## provider's own API types, then sync the schema bodies into the Helm chart's
## files/schemas/ directory. Provider init applies these schemas at runtime.
codegen-code-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the code provider's local API (+ manifest + chart schemas)
	@mkdir -p providers/code/config/crds providers/code/config/kcp providers/code/deploy/chart/files/schemas
	cd providers/code && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/code/config/crds
	./hack/apigen.sh --input-dir providers/code/config/crds --output-dir providers/code/config/kcp
	@for r in connections repositories repositorycommits repositorycheckouts repositorybuildstatuses deploykeys collaborators packages; do \
		cp providers/code/config/kcp/apiresourceschema-$$r.code.railgrid.ai.yaml \
		   providers/code/deploy/chart/files/schemas/$$r.code.railgrid.ai.yaml; \
	done
	@# One APIExport, generated: apigen supplies spec.resources, manifest.yaml
	@# supplies metadata.name (spec.export.name) and spec.permissionClaims (from
	@# spec.requires). The group-named file
	@# apigen leaves behind is deleted by the generator (it is not the export's
	@# name). --schemas-dir pins the resource list to the schemas the chart ships.
	cd provider-sdk && go run ./cmd/apiexportgen \
		--manifest $(CURDIR)/providers/code/manifest.yaml \
		--apigen-export $(CURDIR)/providers/code/config/kcp/apiexport-code.railgrid.ai.yaml \
		--schemas-dir $(CURDIR)/providers/code/deploy/chart/files/schemas \
		--out $(CURDIR)/providers/code/config/kcp/apiexport-code.providers.railgrid.ai.yaml
	cp providers/code/config/kcp/apiexport-code.providers.railgrid.ai.yaml providers/code/deploy/chart/files/apiexport.yaml
	./hack/ensure-boilerplate.sh

codegen-quickstart-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the quickstart provider's local API (+ chart schemas)
	@mkdir -p providers/quickstart/config/crds providers/quickstart/config/kcp providers/quickstart/deploy/chart/files/schemas
	cd providers/quickstart && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/quickstart/config/crds
	./hack/apigen.sh --input-dir providers/quickstart/config/crds --output-dir providers/quickstart/config/kcp
	@for r in greetings; do \
		cp providers/quickstart/config/kcp/apiresourceschema-$$r.quickstart.providers.railgrid.ai.yaml \
		   providers/quickstart/deploy/chart/files/schemas/$$r.quickstart.providers.railgrid.ai.yaml; \
	done
	@# One APIExport, generated: apigen supplies spec.resources, manifest.yaml
	@# supplies metadata.name (spec.export.name) and spec.permissionClaims (from
	@# spec.requires). The group-named file
	@# apigen leaves behind is deleted by the generator (it is not the export's
	@# name). --schemas-dir pins the resource list to the schemas the chart ships.
	cd provider-sdk && go run ./cmd/apiexportgen \
		--manifest $(CURDIR)/providers/quickstart/manifest.yaml \
		--apigen-export $(CURDIR)/providers/quickstart/config/kcp/apiexport-quickstart.providers.railgrid.ai.yaml \
		--schemas-dir $(CURDIR)/providers/quickstart/deploy/chart/files/schemas \
		--out $(CURDIR)/providers/quickstart/config/kcp/apiexport-quickstart.providers.railgrid.ai.yaml
	cp providers/quickstart/config/kcp/apiexport-quickstart.providers.railgrid.ai.yaml providers/quickstart/deploy/chart/files/apiexport.yaml
	./hack/ensure-boilerplate.sh

codegen-kuery-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the kuery provider's local API (+ chart schemas)
	@mkdir -p providers/kuery/config/crds providers/kuery/config/kcp providers/kuery/deploy/chart/files/schemas
	cd providers/kuery && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/kuery/config/crds
	./hack/apigen.sh --input-dir providers/kuery/config/crds --output-dir providers/kuery/config/kcp
	@for r in savedviews; do \
		cp providers/kuery/config/kcp/apiresourceschema-$$r.kuery.providers.railgrid.ai.yaml \
		   providers/kuery/deploy/chart/files/schemas/$$r.kuery.providers.railgrid.ai.yaml; \
	done
	@# One APIExport, generated: apigen supplies spec.resources, manifest.yaml
	@# supplies metadata.name (spec.export.name) and spec.permissionClaims (from
	@# spec.requires). The group-named file
	@# apigen leaves behind is deleted by the generator (it is not the export's
	@# name). --schemas-dir pins the resource list to the schemas the chart ships.
	cd provider-sdk && go run ./cmd/apiexportgen \
		--manifest $(CURDIR)/providers/kuery/manifest.yaml \
		--apigen-export $(CURDIR)/providers/kuery/config/kcp/apiexport-kuery.providers.railgrid.ai.yaml \
		--schemas-dir $(CURDIR)/providers/kuery/deploy/chart/files/schemas \
		--out $(CURDIR)/providers/kuery/config/kcp/apiexport-kuery.providers.railgrid.ai.yaml
	cp providers/kuery/config/kcp/apiexport-kuery.providers.railgrid.ai.yaml providers/kuery/deploy/chart/files/apiexport.yaml
	./hack/ensure-boilerplate.sh

codegen-agents-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the agents provider's local API (+ chart schemas)
	@mkdir -p providers/agents/config/crds providers/agents/config/kcp providers/agents/deploy/chart/files/schemas
	cd providers/agents && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/agents/config/crds
	./hack/apigen.sh --input-dir providers/agents/config/crds --output-dir providers/agents/config/kcp
	@for r in agents connections modelcredentials schedules triggers toolsets runs; do \
		cp providers/agents/config/kcp/apiresourceschema-$$r.agents.railgrid.ai.yaml \
		   providers/agents/deploy/chart/files/schemas/$$r.agents.railgrid.ai.yaml; \
	done
	@# One APIExport, generated: apigen supplies spec.resources, manifest.yaml
	@# supplies metadata.name (spec.export.name) and spec.permissionClaims (from
	@# spec.requires). The group-named file
	@# apigen leaves behind is deleted by the generator (it is not the export's
	@# name). --schemas-dir pins the resource list to the schemas the chart ships.
	cd provider-sdk && go run ./cmd/apiexportgen \
		--manifest $(CURDIR)/providers/agents/manifest.yaml \
		--apigen-export $(CURDIR)/providers/agents/config/kcp/apiexport-agents.railgrid.ai.yaml \
		--schemas-dir $(CURDIR)/providers/agents/deploy/chart/files/schemas \
		--out $(CURDIR)/providers/agents/config/kcp/apiexport-agents.railgrid.ai.yaml
	cp providers/agents/config/kcp/apiexport-agents.railgrid.ai.yaml providers/agents/deploy/chart/files/apiexport.yaml
	./hack/ensure-boilerplate.sh

codegen-app-studio-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the App Studio provider's local API (+ manifest + chart schema)
	@mkdir -p providers/app-studio/config/crds providers/app-studio/config/kcp providers/app-studio/deploy/chart/files/schemas
	cd providers/app-studio && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/app-studio/config/crds
	./hack/apigen.sh --input-dir providers/app-studio/config/crds --output-dir providers/app-studio/config/kcp
	cp providers/app-studio/config/kcp/apiresourceschema-projects.ai.railgrid.ai.yaml \
	   providers/app-studio/deploy/chart/files/schemas/projects.ai.railgrid.ai.yaml
	cp providers/app-studio/config/kcp/apiresourceschema-sessions.ai.railgrid.ai.yaml \
	   providers/app-studio/deploy/chart/files/schemas/sessions.ai.railgrid.ai.yaml
	cp providers/app-studio/config/kcp/apiresourceschema-studios.ai.railgrid.ai.yaml \
	   providers/app-studio/deploy/chart/files/schemas/studios.ai.railgrid.ai.yaml
	@# One APIExport, generated: apigen supplies spec.resources, manifest.yaml
	@# supplies metadata.name (spec.export.name) and spec.permissionClaims (from
	@# spec.requires). The group-named file
	@# apigen leaves behind is deleted by the generator (it is not the export's
	@# name). --schemas-dir pins the resource list to the schemas the chart ships.
	cd provider-sdk && go run ./cmd/apiexportgen \
		--manifest $(CURDIR)/providers/app-studio/manifest.yaml \
		--apigen-export $(CURDIR)/providers/app-studio/config/kcp/apiexport-ai.railgrid.ai.yaml \
		--schemas-dir $(CURDIR)/providers/app-studio/deploy/chart/files/schemas \
		--out $(CURDIR)/providers/app-studio/config/kcp/apiexport-ai.railgrid.ai.yaml
	cp providers/app-studio/config/kcp/apiexport-ai.railgrid.ai.yaml providers/app-studio/deploy/chart/files/apiexport.yaml
	./hack/ensure-boilerplate.sh

test:
	go test $(shell go list ./... | grep -v '/test/e2e')

.PHONY: test-organization-bootstrap
test-organization-bootstrap: ## Verify personal and shared organization initialization and REST creation
	go test -count=1 ./pkg/hub/controllers/organization ./pkg/hub/restapi ./pkg/hub/kcp ./pkg/hub/bootstrap

.PHONY: test-tilt-sandbox-default
test-tilt-sandbox-default: ## Verify universal sandbox is opt-in in Tilt
	python3 hack/scripts/verify-tilt-sandbox-default.test.py

.PHONY: test-tilt-code-sequence
test-tilt-code-sequence: ## Verify Code updates initialize before serving in Tilt
	python3 hack/scripts/verify-tilt-code-sequence.test.py

test-util:
	go test ./pkg/util/...

.PHONY: test-provider-catalog-auth test-app-studio-mcp-access lint-app-studio-mcp-access
test-provider-catalog-auth: ## Verify tenant-scoped workload catalog authentication
	go test -count=1 ./pkg/hub -run 'TestProviderCatalog|TestKCPTenantResolver'
	go test -count=1 ./pkg/hub/tenant ./pkg/hub/providers ./pkg/hub/serviceaccounts
test-app-studio-mcp-access: ## Verify MCP workload authorization and App Studio identity provisioning
	go test -count=1 ./pkg/hub/mcpaggregate
	cd providers/app-studio && go test -count=1 ./controller/project ./hubmcp

lint-app-studio-mcp-access: $(GOLANGCI_LINT) ## Lint the MCP authorization integration across both modules
	$(GOLANGCI_LINT) run ./pkg/hub/mcpaggregate
	cd providers/app-studio && $(CURDIR)/$(GOLANGCI_LINT) run ./controller/project ./hubmcp

.PHONY: test-auth fmt-auth lint-auth
test-auth: ## Verify standalone auth and status-page surfaces
	go test -count=1 ./pkg/cli/auth ./pkg/hub/appauth
	cd hack/dex && go test -count=1 ./...
	cd provider-sdk && go test -count=1 ./statuspage
	cd providers/agents && go test -count=1 ./api
	cd providers/code && go test -count=1 ./oauthgithub

fmt-auth: $(GOLANGCI_LINT) ## Format auth and status-page sources with the pinned formatter
	$(GOLANGCI_LINT) fmt pkg/cli/auth/authenticator.go pkg/cli/auth/authenticator_test.go pkg/hub/appauth/appauth.go pkg/hub/appauth/appauth_test.go
	cd hack/dex && $(CURDIR)/$(GOLANGCI_LINT) fmt web/web_test.go
	cd provider-sdk && $(CURDIR)/$(GOLANGCI_LINT) fmt statuspage/statuspage.go statuspage/statuspage_test.go
	cd providers/agents && $(CURDIR)/$(GOLANGCI_LINT) fmt api/oauth.go
	cd providers/code && $(CURDIR)/$(GOLANGCI_LINT) fmt oauthgithub/oauth.go oauthgithub/oauth_test.go

lint-auth: $(GOLANGCI_LINT) ## Lint auth and status-page sources with the pinned linter
	$(GOLANGCI_LINT) run $(ARGS) ./pkg/cli/auth ./pkg/hub/appauth
	cd provider-sdk && $(CURDIR)/$(GOLANGCI_LINT) run $(ARGS) ./statuspage
	cd providers/agents && $(CURDIR)/$(GOLANGCI_LINT) run $(ARGS) ./api
	cd providers/code && $(CURDIR)/$(GOLANGCI_LINT) run $(ARGS) ./oauthgithub

.PHONY: test-code-provider test-code-provider-race lint-code-provider fix-lint-code-provider
test-code-provider: ## Run standalone Code provider tests
	cd providers/code && go test -count=1 ./...

test-code-provider-race: ## Race-check GitHub polling caches and Package reconciliation
	cd providers/code && go test -race -count=1 ./backend/github ./controller/packages

lint-code-provider: $(GOLANGCI_LINT) ## Lint the standalone Code provider
	cd providers/code && $(abspath $(GOLANGCI_LINT)) run $(ARGS) ./...

fix-lint-code-provider: $(GOLANGCI_LINT) ## Format and auto-fix the standalone Code provider
	cd providers/code && $(abspath $(GOLANGCI_LINT)) run --fix $(ARGS) ./...

lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

PROVIDER_MODULES := quickstart code infrastructure edges kuery agents app-studio

lint-providers: $(GOLANGCI_LINT) ## Run golangci-lint in every standalone provider module (separate go.mod, not covered by lint)
	@rc=0; for p in $(PROVIDER_MODULES); do \
		echo "== providers/$$p"; \
		(cd providers/$$p && $(CURDIR)/$(GOLANGCI_LINT) run ./...) || rc=1; \
	done; exit $$rc

lint-provider-sdk: $(GOLANGCI_LINT) ## Run golangci-lint in provider-sdk
	cd provider-sdk && $(CURDIR)/$(GOLANGCI_LINT) run ./...

fix-lint: $(GOLANGCI_LINT) ## Run golangci-lint with auto-fix
	$(GOLANGCI_LINT) run --fix ./...

vet:
	go vet ./...

# Vulnerability gate for every Go module. Fails only on findings govulncheck
# traced to a called symbol that are not in hack/govulncheck-allow.yaml.
# Pass module directories to narrow it, e.g. `make govulncheck ARGS=providers/edges`.
govulncheck: ## Run the govulncheck gate over every Go module
	./hack/govulncheck.sh $(ARGS)

# --- Code generation ---

boilerplate: ## Ensure license boilerplate on all Go files
	./hack/ensure-boilerplate.sh

verify-boilerplate: ## Verify license boilerplate on all Go files
	./hack/ensure-boilerplate.sh --verify

crds: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Generate CRDs and kcp APIResourceSchemas
	./hack/update-codegen-crds.sh

codegen: crds codegen-code-provider codegen-app-studio-provider codegen-infrastructure-provider boilerplate ## Generate all (CRDs + kcp resources + provider schemas + boilerplate)

verify-codegen: codegen ## Verify codegen is up to date
	@if ! git diff --quiet HEAD; then \
		echo "ERROR: codegen produced a diff. Please run 'make codegen' and commit the result."; \
		git diff --stat; \
		exit 1; \
	fi

sync-portalkit: ## Vendor the shared portalkit UI kits into provider portals
	@hack/sync-portalkit.sh

verify-portalkit: ## Verify vendored portalkit copies are in sync with the canonical source
	@hack/sync-portalkit.sh --verify
	@node --test provider-sdk/portalkit/dashboardtile.conformance.test.mjs provider-sdk/portalkit/kube.behavior.test.mjs
	@$(MAKE) verify-agentkit

# EXTERNAL_PROVIDERS_DIR (the same variable `make tilt` takes) is passed to the
# permission-claim-policy generator so an out-of-tree provider's cross-provider
# requirements are checked too. Without it CI still checks every in-tree
# requirement exactly and leaves the out-of-tree rules alone -- see the
# generator's header.
CLAIM_POLICY_ARGS = $(if $(EXTERNAL_PROVIDERS_DIR),--external-providers-dir="$(EXTERNAL_PROVIDERS_DIR)",)

verify-provider-contract: ## Verify provider manifests, claims and route classes match the provider contract
	@node hack/verify-provider-contract.test.mjs
	@node hack/verify-provider-contract.mjs
	@node hack/generate-permission-claim-policy.test.mjs
	@node hack/generate-permission-claim-policy.mjs --check $(CLAIM_POLICY_ARGS)

.PHONY: permission-claim-policy
permission-claim-policy: ## Regenerate config/kcp/permissionclaimpolicy.yaml from the provider manifests
	@node hack/generate-permission-claim-policy.mjs $(CLAIM_POLICY_ARGS)

verify-agentkit: ## Verify optional AgentKit style loading and conversation contracts
	@node --test hack/verify-agentkit-dependencies.test.mjs
	@node --test provider-sdk/agentkit/styles.conformance.test.mjs
	@node --test provider-sdk/agentkit-vue/conversation.conformance.test.mjs

.PHONY: test-scoped-navigation
test-scoped-navigation: ## Verify scoped portal URLs, login returns, and provider request isolation
	cd portal && node --test src/router/scopedNavigation.test.mjs src/providers/providerFetch.test.mjs

test-portal: ## Run the complete portal test suite
	cd portal && npm test

test-portal-settings-conformance: ## Verify portal shell and organization/workspace source contracts
	@node --test portal/src/theme-bootstrap.test.mjs portal/src/pages/OrganizationsWorkspace.conformance.test.mjs portal/src/stores/tenant-read-status.test.mjs portal/src/pages/ProviderEnableDialog.deferred.test.mjs portal/src/components/TerminalDock.conformance.test.mjs portal/src/components/DashboardTile.conformance.test.mjs portal/src/pages/MCPPage.conformance.test.mjs

test-create-flow-conformance: ## Verify route-owned creation uses the canonical page skeleton
	@node --test hack/create-flow-conformance.test.mjs

verify-ui-conformance: test-portal-settings-conformance test-create-flow-conformance ## Verify provider UI source uses the canonical k-* design vocabulary
	@node --test provider-sdk/portalkit-vue/ResourceTable.selection.test.mjs
	@node hack/verify-ui-conformance.test.mjs
	@node hack/verify-ui-conformance.mjs

serve-model-form-visual: ## Serve the manual App Studio/Agents Models visual fixture
	MODEL_FORM_FONT_NODE_MODULES="$(if $(MODEL_FORM_FONT_NODE_MODULES),$(MODEL_FORM_FONT_NODE_MODULES),$(CURDIR)/portal/node_modules)" \
	providers/app-studio/portal/node_modules/.bin/vite --config hack/models-form-visual/vite.config.mjs --host "$(if $(MODEL_FORM_HOST),$(MODEL_FORM_HOST),127.0.0.1)" --port "$(if $(MODEL_FORM_PORT),$(MODEL_FORM_PORT),5198)"

test-model-form-visual: ## Compare the App Studio and Agents Models forms at the supported visual matrix
	PLAYWRIGHT_MODULE="$(PLAYWRIGHT_MODULE)" \
	MODEL_FORM_FIXTURE_URL="$(if $(MODEL_FORM_FIXTURE_URL),$(MODEL_FORM_FIXTURE_URL),http://127.0.0.1:5198)" \
	MODEL_FORM_OUTPUT="$(MODEL_FORM_OUTPUT)" \
	node hack/models-form-visual-regression.mjs

verify-design-docs: ## Validate structured design-document metadata and emit its JSON catalog
	@node hack/verify-design-docs.test.mjs
	@node hack/verify-design-docs.mjs --catalog

verify-tilt-browser-deployment: ## Verify Browser image pin and Tilt hub reachability wiring
	@bash hack/scripts/verify-tilt-browser-deployment.test.sh

# --- Tool installation ---

.PHONY: verify-ci-selection verify-workflows
verify-ci-selection: ## Test CI change selection and completion gates (requires hack/ci/requirements-test.txt)
	@python3 -c 'import yaml; assert yaml.__version__ == "6.0.3", "Install hack/ci/requirements-test.txt"'
	@python3 -m unittest discover -s hack/ci -p 'test_*.py' -v

verify-workflows: $(ACTIONLINT) ## Validate CI workflows with pinned Actionlint
	@$(ACTIONLINT) -shellcheck= -pyflakes= .github/workflows/ci.yaml .github/workflows/e2e.yaml .github/workflows/images.yaml .github/workflows/helm-images.yaml .github/workflows/macos.yaml

$(ACTIONLINT):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/rhysd/actionlint/cmd/actionlint actionlint $(ACTIONLINT_VER)

tools: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) $(GOLANGCI_LINT) ## Install all dev tools

$(CONTROLLER_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/controller-tools/cmd/controller-gen $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

$(KCP_APIGEN_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/kcp-dev/sdk/cmd/apigen $(KCP_APIGEN_BIN) $(KCP_APIGEN_VER)

$(GOLANGCI_LINT):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint $(GOLANGCI_LINT_BIN) $(GOLANGCI_LINT_VER)

# --- Dev environment ---

.PHONY: certs
certs:
	@mkdir -p certs
	@if [ -s certs/apiserver.crt ] && [ -s certs/apiserver.key ] && \
		openssl x509 -in certs/apiserver.crt -noout -checkend 86400 >/dev/null 2>&1 && \
		openssl x509 -in certs/apiserver.crt -noout \
			-checkhost console.127.0.0.1.sslip.io >/dev/null 2>&1 && \
		openssl x509 -in certs/apiserver.crt -noout \
			-checkhost host.docker.internal >/dev/null 2>&1; then \
		exit 0; \
	fi; \
	openssl req -x509 -newkey rsa:2048 -nodes \
		-keyout certs/apiserver.key -out certs/apiserver.crt \
		-days 365 -subj "/CN=localhost" \
		-addext "subjectAltName=DNS:localhost,DNS:*.127.0.0.1.sslip.io,DNS:host.docker.internal,IP:127.0.0.1"

dev-setup: certs

$(DEX):
	@mkdir -p $(TOOLSDIR)
	@echo "Building Dex $(DEX_VER)..."
	@rm -rf $(TOOLSDIR)/dex-src
	git clone --depth 1 --branch $(DEX_VER) https://github.com/dexidp/dex.git $(TOOLSDIR)/dex-src
	cd $(TOOLSDIR)/dex-src && go build -o ../dex-$(DEX_VER) ./cmd/dex
	@rm -rf $(TOOLSDIR)/dex-src
	ln -sf $(notdir $(DEX)) $(TOOLSDIR)/dex
	@echo "Dex binary: $(DEX)"

$(KCP):
	@mkdir -p $(TOOLSDIR)
	@echo "Downloading kcp $(KCP_VER) for $(OS)/$(ARCH)..."
	curl -sL "https://github.com/kcp-dev/kcp/releases/download/$(KCP_VER)/kcp_$(subst v,,$(KCP_VER))_$(OS)_$(ARCH).tar.gz" | \
		tar xz -C $(TOOLSDIR) bin/kcp
	mv $(TOOLSDIR)/bin/kcp $(KCP)
	rmdir $(TOOLSDIR)/bin 2>/dev/null || true
	chmod +x $(KCP)
	ln -sf $(notdir $(KCP)) $(TOOLSDIR)/kcp
	@echo "kcp binary: $(KCP)"

dev-login: build-railgrid
	PATH=$(CURDIR)/$(BINDIR):$$PATH $(BINDIR)/railgrid login --hub-url $(DEV_HUB_URL) --insecure-skip-tls-verify

dev-login-static: build-railgrid ## Login using static token auth (for use with run-hub-static)
	PATH=$(CURDIR)/$(BINDIR):$$PATH $(BINDIR)/railgrid login --hub-url $(DEV_HUB_URL) --insecure-skip-tls-verify --token=$(STATIC_AUTH_TOKEN)

# TYPE selects the Edge type for dev-edge-create and dev-run-edge.
# Values: kubernetes (default) | server
TYPE ?= kubernetes
# Default DEV_EDGE_NAME is per-type so kubernetes and server edges can coexist.
DEV_EDGE_NAME ?= $(if $(filter server,$(TYPE)),dev-edge-server-1,dev-edge-kube-1)

dev-edge-create: build-railgrid ## Create an Edge resource: TYPE=kubernetes (default) or TYPE=server
	PATH=$(CURDIR)/$(BINDIR):$$PATH BINDIR=$(CURDIR)/$(BINDIR) hack/scripts/dev-edge-setup.sh $(DEV_EDGE_NAME) $(TYPE) "env=dev,provider=local"

# Add-on types the dev server edge agent may materialize (docs/edge-addons.md).
# The dev agent runs as the developer, not root, so the add-on child runs as
# the same user and no --addon-user is needed. Empty disables add-ons.
DEV_EDGE_ALLOW_ADDONS ?= runner

dev-run-edge: build-railgrid ## Run the edge agent: TYPE=kubernetes (default) or TYPE=server
	@test -f .env.edge.$(TYPE) || (echo "Run 'make dev-edge-create TYPE=$(TYPE)' first (expected .env.edge.$(TYPE))"; exit 1)
ifeq ($(TYPE),server)
	$(BINDIR)/railgrid agent run \
		--hub-url=$(DEV_HUB_URL) \
		--hub-insecure-skip-tls-verify \
		--token=$(RAILGRID_EDGE_JOIN_TOKEN) \
		--tunnel-url=$(DEV_HUB_URL) \
		--edge-name=$(RAILGRID_EDGE_NAME) \
		--cluster=$(RAILGRID_EDGE_CLUSTER) \
		--type=server \
		--ssh-proxy-port=2222 \
		--ssh-user=railgrid \
		--ssh-password=password \
		$(if $(DEV_EDGE_ALLOW_ADDONS),--allow-addon=$(DEV_EDGE_ALLOW_ADDONS))
else
	hack/scripts/ensure-kind-cluster.sh
	$(BINDIR)/railgrid agent run \
		--hub-url=$(DEV_HUB_URL) \
		--hub-insecure-skip-tls-verify \
		--token=$(RAILGRID_EDGE_JOIN_TOKEN) \
		--tunnel-url=$(DEV_HUB_URL) \
		--edge-name=$(RAILGRID_EDGE_NAME) \
		--kubeconfig=.kubeconfig-railgrid-agent \
		--cluster=$(RAILGRID_EDGE_CLUSTER) \
		--type=kubernetes
endif

dev-create-workload: ## Create a demo Workload targeting dev edges
	kubectl apply -f hack/dev/examples/workload-nginx.yaml

# Export only variables declared by the optional dotenv files. A bare `export`
# also exports recursive Make variables containing $(shell ...); GNU Make 4.4.1
# can segfault while expanding those variables for Tilt's local subprocesses.
ENV_FILES := $(wildcard .env .env.edge.$(TYPE))
ifneq ($(strip $(ENV_FILES)),)
include $(ENV_FILES)
ENV_EXPORTS := $(shell sed -n -E 's/^[[:space:]]*(export[[:space:]]+)?([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*[:?+]?=.*/\2/p' $(ENV_FILES))
export $(ENV_EXPORTS)
endif

dev-infra: $(KCP) $(DEX) certs ## Run infra only (kcp + Dex)
	hack/scripts/dev-infra.sh

# Service hooks for dependency tracking
HOOKS_DIR := .hooks
SERVICE_HOOKS := hack/scripts/service-hooks.sh

# Helper to check if a service is running
define check_service
	@source $(SERVICE_HOOKS) && service_is_running $(1) || (echo "ERROR: $(1) is not running. Start with: $(2)" && exit 1)
endef

# Helper to require service not running
define check_no_service
	@source $(SERVICE_HOOKS) && ! service_is_running $(1) || (echo "ERROR: $(1) is running. Stop it first or use a different mode." && exit 1)
endef

dev-run-kcp: $(KCP) ## Run external kcp server
	@source $(SERVICE_HOOKS) && cleanup_stale_hooks
	@echo "Starting kcp..."
	@source $(SERVICE_HOOKS) && \
		($(KCP) start --root-directory=$(KCP_DATA_DIR) --feature-gates=WorkspaceMounts=true,CacheAPIs=true & \
		KCP_PID=$$!; \
		service_start kcp $$KCP_PID; \
		wait $$KCP_PID)

run-dex: $(DEX) certs ## Run Dex OIDC server
	@source $(SERVICE_HOOKS) && cleanup_stale_hooks
	@echo "Starting Dex..."
	@source $(SERVICE_HOOKS) && \
		($(DEX) serve hack/dev/dex/dex-config-dev.yaml & \
		DEX_PID=$$!; \
		service_start dex $$DEX_PID; \
		wait $$DEX_PID)

dev-run-ssh-server:
	docker run \
  --name=openssh-server \
  -e PUID=1000 \
  -e PGID=1000 \
  -e TZ=Etc/UTC \
  -e PASSWORD_ACCESS=true \
  -e USER_PASSWORD=password \
  -e USER_NAME=railgrid \
  -p 2222:2222 \
  --restart unless-stopped \
  lscr.io/linuxserver/openssh-server:latest

## --- kube edge agent, in-cluster (dev) --------------------------------------
# Runs the agent as a Deployment INSIDE the edge's kind cluster, the way the
# railgrid-agent chart does in production — instead of `dev-run-edge`, which runs it
# on the host against the cluster's kubeconfig.
#
# This matters for the Service kind: a host-run agent can serve the k8s
# subresource (the apiserver is reachable from the host) but NOT svc, which dials
# cluster DNS (home-assistant.home.svc). Only an in-cluster agent can resolve it.
#
# Networking: in Tiltfile.cluster the hub is a ClusterIP in the `kcp-tilt`
# cluster, reachable from the host only via Tilt's 127.0.0.1 port-forward — no
# use to a pod in the `railgrid-agent` cluster. Both clusters' nodes share the
# `kind` docker network, so we expose the hub via a NodePort and dial the
# kcp-tilt node IP directly.
DEV_AGENT_IMAGE_REPO ?= ghcr.io/railgrid/railgrid-agent
DEV_AGENT_NS         ?= railgrid-agent
DEV_AGENT_KIND       ?= railgrid-agent
DEV_HUB_KIND         ?= kcp-tilt
DEV_HUB_NODEPORT     ?= 30443
# Resolved at recipe time: docker assigns the node IP when the cluster is created.
DEV_HUB_NODE_IP       = $(shell docker inspect $(DEV_HUB_KIND)-control-plane \
                          -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null)

dev-edge-agent-incluster: docker-build-agent ## Run the kube edge agent IN the edge cluster (needed for the Service/svc path)
	@test -f .env.edge.kubernetes || { echo "Run 'make dev-edge-create TYPE=kubernetes' first (expected .env.edge.kubernetes)"; exit 1; }
	@test -n "$(DEV_HUB_NODE_IP)" || { echo "Cannot resolve the $(DEV_HUB_KIND) node IP — is the hub cluster running?"; exit 1; }
	@echo "==> Prereqs, in order:"
	@echo "      1. Stop the host agent (Tilt: edge-kube-agent) — two agents for one edge fight over the tunnel."
	@echo "      2. Re-run 'make dev-edge-create TYPE=kubernetes' (Tilt: edge-kube-create)."
	@echo "         The hub CLEARS status.joinToken once an agent redeems it, so the token still"
	@echo "         sitting in .env.edge.kubernetes from a previous run will fail auth. It cannot be"
	@echo "         checked from here — if the agent logs 'workspace access not permitted', this is why."
	hack/scripts/ensure-kind-cluster.sh $(DEV_AGENT_KIND)
	@echo "==> Exposing the hub to the $(DEV_AGENT_KIND) cluster (NodePort $(DEV_HUB_NODEPORT) on $(DEV_HUB_NODE_IP))"
	kubectl --context kind-$(DEV_HUB_KIND) apply -f hack/dev/railgrid-hub-nodeport.yaml
	@echo "==> Loading $(DEV_AGENT_IMAGE_REPO):$(VERSION) into kind/$(DEV_AGENT_KIND)"
	kind load docker-image $(DEV_AGENT_IMAGE_REPO):$(VERSION) --name $(DEV_AGENT_KIND)
	@# Source the edge env in-recipe rather than trusting the global
	@# `-include .env.edge.$$(TYPE)`: an exported TYPE=server would otherwise
	@# feed the server edge's name/cluster/token to the kubernetes agent.
	set -a; . ./.env.edge.kubernetes; set +a; \
	test -n "$$RAILGRID_EDGE_JOIN_TOKEN" || { echo "No RAILGRID_EDGE_JOIN_TOKEN in .env.edge.kubernetes — the token is cleared once an agent redeems it; re-run 'make dev-edge-create TYPE=kubernetes'"; exit 1; }; \
	helm --kubeconfig=.kubeconfig-$(DEV_AGENT_KIND) upgrade --install railgrid-agent deploy/charts/railgrid-agent \
		--namespace $(DEV_AGENT_NS) --create-namespace \
		--set image.repository=$(DEV_AGENT_IMAGE_REPO) \
		--set image.tag=$(VERSION) \
		--set image.pullPolicy=IfNotPresent \
		--set agent.edgeName=$$RAILGRID_EDGE_NAME \
		--set agent.cluster=$$RAILGRID_EDGE_CLUSTER \
		--set agent.hub.url=https://$(DEV_HUB_NODE_IP):$(DEV_HUB_NODEPORT) \
		--set agent.hub.token=$$RAILGRID_EDGE_JOIN_TOKEN \
		--set agent.hub.insecureSkipTLSVerify=true \
		--wait --timeout=120s
	@echo "==> Agent deployed. Logs: kubectl --kubeconfig=.kubeconfig-$(DEV_AGENT_KIND) -n $(DEV_AGENT_NS) logs -l app.kubernetes.io/name=railgrid-agent -f"

dev-edge-agent-incluster-logs: ## Tail the in-cluster edge agent
	kubectl --kubeconfig=.kubeconfig-$(DEV_AGENT_KIND) -n $(DEV_AGENT_NS) \
		logs -l app.kubernetes.io/name=railgrid-agent --tail=100 -f

dev-edge-agent-incluster-down: ## Remove the in-cluster edge agent
	helm --kubeconfig=.kubeconfig-$(DEV_AGENT_KIND) uninstall railgrid-agent -n $(DEV_AGENT_NS) --ignore-not-found

.PHONY: dev-edge-agent-incluster dev-edge-agent-incluster-logs dev-edge-agent-incluster-down

## --- Home Assistant on the kube edge (dev) ----------------------------------
# Deploys HA into the same kind cluster the kubernetes edge agent serves, so the
# edges provider's Service kind (spec.targetRef → home-assistant.home.svc:8123)
# has something real to proxy to. See
# providers/edges/contrib/manifests/homeassistant/README.md.
HA_MANIFESTS  ?= providers/edges/contrib/manifests/homeassistant
HA_NAMESPACE  ?= home
HA_KUBECONFIG ?= .kubeconfig-railgrid-agent

dev-deploy-homeassistant: ## Deploy Home Assistant into the railgrid-agent kind cluster
	hack/scripts/ensure-kind-cluster.sh
	kubectl --kubeconfig=$(HA_KUBECONFIG) apply -k $(HA_MANIFESTS)
	@echo "Waiting for Home Assistant (first boot pulls a ~1.5GB image)..."
	kubectl --kubeconfig=$(HA_KUBECONFIG) -n $(HA_NAMESPACE) \
		rollout status deploy/home-assistant --timeout=600s
	@echo "Home Assistant is up at home-assistant.$(HA_NAMESPACE).svc:8123 (in-cluster)."
	@echo "Onboard it: make dev-homeassistant-forward → http://localhost:8123"

dev-homeassistant-forward: ## Port-forward Home Assistant to localhost:8123 (onboarding + token)
	@echo "Home Assistant → http://localhost:8123 (profile → Security → Long-lived access tokens)"
	kubectl --kubeconfig=$(HA_KUBECONFIG) -n $(HA_NAMESPACE) \
		port-forward svc/home-assistant 8123:8123

dev-undeploy-homeassistant: ## Remove Home Assistant (keeps the PVC's data unless you delete the ns)
	kubectl --kubeconfig=$(HA_KUBECONFIG) delete -k $(HA_MANIFESTS) --ignore-not-found

.PHONY: dev-deploy-homeassistant dev-homeassistant-forward dev-undeploy-homeassistant

# --- Hub configuration options ---
# These can be combined to create different run configurations.

STATIC_AUTH_TOKEN ?= dev-token

# Base hub flags (always needed)
HUB_FLAGS_BASE := \
	--serving-cert-file=certs/apiserver.crt \
	--serving-key-file=certs/apiserver.key \
	--hub-external-url=$(DEV_HUB_URL) \
	--dev-mode -v 4

# Auth: OIDC via Dex
HUB_FLAGS_OIDC := \
	--idp-issuer-url=https://localhost:5554/dex \
	--idp-client-id=railgrid \
	--idp-client-secret=ZXhhbXBsZS1hcHAtc2VjcmV0

# Auth: Static token
HUB_FLAGS_STATIC := \
	--static-auth-token=$(STATIC_AUTH_TOKEN) \
	--admin-users=$(ADMIN_USERS)

# Platform-admin identities allowed at /api/admin/* + the portal /bonkers area.
# A static token's user is matched by its RBAC identity,
# railgrid:static:<first 16 hex of sha256("static-token/<token>")> (see
# identity.NewStaticToken) — for dev-token that's railgrid:static:47b9dce0e91570a1.
# Override for OIDC dev with your real email.
ADMIN_USERS ?= railgrid:static:$(shell printf 'static-token/%s' '$(STATIC_AUTH_TOKEN)' | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-16)

# KCP: External (requires running kcp separately)
HUB_FLAGS_KCP_EXTERNAL := \
	--external-kcp-kubeconfig=.kcp/admin.kubeconfig

# KCP: Embedded (runs kcp in-process)
# --kcp-shard-external-url / --kcp-shard-virtual-workspace-url ARE NOT set
# by default — kcp defaults them to localhost which works for in-process
# consumers (hub controllers, kcp proxy). Overriding
# them globally to host.docker.internal breaks anything running on the
# host (DNS doesn't resolve unless you're on Docker Desktop with the
# magic enabled). If you need EndpointSlice URLs that are reachable from
# inside a kind pod (e.g. kro pod talking to kcp), set KCP_SHARD_EXTERNAL_URL
# explicitly AND make sure host.docker.internal resolves on your host
# (or use your LAN IP).
KCP_SHARD_EXTERNAL_URL ?=
HUB_FLAGS_KCP_EMBEDDED := \
	--embedded-kcp \
	--kcp-root-dir=.kcp \
	--kcp-secure-port=6443 \
	$(if $(KCP_SHARD_EXTERNAL_URL),--kcp-shard-external-url=$(KCP_SHARD_EXTERNAL_URL) --kcp-shard-virtual-workspace-url=$(KCP_SHARD_EXTERNAL_URL),)

# Portal dev proxy: reverse-proxy /console/* to the Vite dev server at :3000
# so UI changes hot-reload without rebuilding the hub. Start the Vite server
# with: cd portal && npm run dev
PORTAL_DEV_URL ?= http://localhost:3000
HUB_FLAGS_PORTAL_DEV := \
	--portal-dev-url=$(PORTAL_DEV_URL)

# --- Run targets ---
# Naming convention: run-hub-[auth]-[kcp]
# auth: oidc | static
# kcp: external | embedded

## External KCP + OIDC auth (requires: make run-dex, make dev-run-kcp)
run-hub: build-hub certs
	@source $(SERVICE_HOOKS) && require_service dex "make run-dex"
	@source $(SERVICE_HOOKS) && require_service kcp "make dev-run-kcp"
	$(BINDIR)/railgrid-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_OIDC) $(HUB_FLAGS_KCP_EXTERNAL)

## External KCP + static token auth (requires: make dev-run-kcp)
run-hub-static: build-hub certs
	@source $(SERVICE_HOOKS) && require_service kcp "make dev-run-kcp"
	$(BINDIR)/railgrid-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EXTERNAL)

## Embedded KCP + OIDC auth (requires: make run-dex)
run-hub-embedded: build-hub certs
	@source $(SERVICE_HOOKS) && require_service dex "make run-dex"
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/railgrid-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_OIDC) $(HUB_FLAGS_KCP_EMBEDDED)

## Embedded KCP + static token auth + portal dev proxy (standalone - no external deps)
run-hub-embedded-static: build-hub certs
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/railgrid-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EMBEDDED) $(HUB_FLAGS_PORTAL_DEV)

## Embedded KCP + static token (fully standalone)
run-hub-standalone: build-hub certs
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/railgrid-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EMBEDDED)

# Local kcp checkout to iterate against. Defaults to the standard per-user Go
# workspace path. Override on the CLI or via env:
#   make tilt-cluster TILT_KCP_DIR=/path/to/kcp
#   make tilt-cluster KCP_DIR=/path/to/kcp
TILT_KCP_DIR ?= $(or $(KCP_DIR),$(HOME)/go/src/github.com/kcp-dev/kcp)
# Tilt's KIND image loader stages a full `docker save` tarball in TMPDIR. Keep
# that payload off capacity-limited /tmp tmpfs mounts used by development hosts.
# Both stacks load images into kind (the agent image, and in cluster mode the
# provider images), so both need it.
TILT_TMPDIR ?= $(CURDIR)/.kcp/tmp/tilt
# Tilt's own API/UI port. Only used to detect an already-running instance.
TILT_PORT ?= 10350
# Replicas for the hub + every railgrid provider Deployment in cluster mode.
# Default 1 keeps the dev loop light; REPLICA_COUNT=2 exercises the HA paths
# (leader election, tunnel-ownership relay, run claims, session failover):
#   make tilt-cluster REPLICA_COUNT=2
REPLICA_COUNT ?= 1
# Providers from another repository, added to the tilt / tilt-cluster session by that
# repository's Tilt library (docs/external-providers-tilt.md). An empty
# EXTERNAL_PROVIDERS_DIR disables them; an unset or empty EXTERNAL_PROVIDERS
# loads every provider in that repository, or name one to load only it:
#   make tilt EXTERNAL_PROVIDERS_DIR=../providers
#   make tilt-cluster EXTERNAL_PROVIDERS_DIR=../providers EXTERNAL_PROVIDERS=planner
EXTERNAL_PROVIDERS_DIR ?=
EXTERNAL_PROVIDERS ?= all

.PHONY: tilt tilt-cluster

## railgrid-hub as a host binary with embedded kcp + host-run portal and providers.
## The default loop: no kind cluster for the hub itself, so it starts in seconds
## and every Go change is a plain rebuild. Use tilt-cluster when you need real
## multi-shard kcp, in-cluster deployment, or to iterate on a kcp checkout.
## KCP_IMAGE runs kcp from a published image instead of the server compiled
## into the hub — that is how a kcp PR build is reached:
## ghcr.io/kcp-dev/kcp-prs:pr-<number>-<short sha>. It serves on the same :6443
## and keeps its own .kcp-external root, so the switch starts from an empty kcp.
## hack/kcp-external.sh starts it with the feature gates the embedded server
## has (CacheAPIs — without it no custom subresource is ever routed — and
## WorkspaceMounts), and reads <root>/token-auth-file.csv when present so
## static-token users exist on it too.
KCP_IMAGE ?=

tilt: ## Run Tiltfile (embedded binary mode); see tilt-cluster for the in-cluster stack
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	@# Tilt refuses a second instance on its API port, but its own message
	@# suggests TILT_PORT to run both — which is wrong here: the two stacks
	@# fight over :9443 and the shared .kcp state dir. Say so instead.
	@if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:$(TILT_PORT) -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "ERROR: a Tilt instance is already running (port $(TILT_PORT))."; \
		echo "       Only one stack at a time — both bind :9443 and share .kcp/."; \
		echo "       Stop the other one first: tilt down -f Tiltfile.cluster"; \
		exit 1; \
	fi
	@# A stray railgrid-hub or leftover port-forward on :9443 gets no such check
	@# from Tilt; it surfaces as an opaque bind failure inside the `hub`
	@# resource long after `tilt up` looks healthy.
	@if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:9443 -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "ERROR: something is already listening on :9443 — stop it before starting the hub."; \
		lsof -nP -iTCP:9443 -sTCP:LISTEN; \
		exit 1; \
	fi
	@mkdir -p "$(TILT_TMPDIR)"
	@# External providers run as pods in railgrid-kro. Create it before
	@# `tilt up` for the same reason tilt-cluster does: Tilt caches its
	@# kube client at startup. kro-mgmt-up later finds and reuses it.
ifneq ($(EXTERNAL_PROVIDERS_DIR),)
	@kind get clusters 2>/dev/null | grep -qx "$(KRO_KIND_NAME)" || kind create cluster --name "$(KRO_KIND_NAME)" --kubeconfig "$(KRO_KIND_KUBECONFIG)"
	@kind get kubeconfig --name "$(KRO_KIND_NAME)" > "$(KRO_KIND_KUBECONFIG)"
	KUBECONFIG="$(KRO_KIND_KUBECONFIG):$${KUBECONFIG:-$$HOME/.kube/config}" TMPDIR="$(TILT_TMPDIR)" \
		tilt up -f Tiltfile --context "kind-$(KRO_KIND_NAME)" -- \
		--external-providers-dir="$(EXTERNAL_PROVIDERS_DIR)" --external-providers="$(EXTERNAL_PROVIDERS)" \
		--kcp-image="$(KCP_IMAGE)"
else
	TMPDIR="$(TILT_TMPDIR)" tilt up -f Tiltfile -- --kcp-image="$(KCP_IMAGE)"
endif

## Full multi-shard kcp in a kind cluster + railgrid-hub in-cluster, against a local kcp checkout
tilt-cluster: ## Run Tiltfile.cluster against a local kcp tree (override with TILT_KCP_DIR=... or KCP_DIR=...)
	@# Create the kind cluster + context BEFORE `tilt up`. If the cluster is
	@# created from inside the Tiltfile, Tilt initializes its deploy client
	@# before the kind-kcp-tilt context exists and caches an empty config,
	@# leaving every native k8s_yaml resource stuck on "could not set up
	@# kubernetes client: no configuration has been provided". Guaranteeing the
	@# cluster+context up front avoids that race.
	@kind get clusters 2>/dev/null | grep -qx kcp-tilt || kind create cluster --name kcp-tilt
	@kind export kubeconfig --name kcp-tilt
	@mkdir -p "$(TILT_TMPDIR)"
	TMPDIR="$(TILT_TMPDIR)" tilt up -f Tiltfile.cluster -- --kcp-dir="$(TILT_KCP_DIR)" --replicas="$(REPLICA_COUNT)" \
		--external-providers-dir="$(EXTERNAL_PROVIDERS_DIR)" --external-providers="$(EXTERNAL_PROVIDERS)"

# --- Provider quickstart (local dev) ---
# The quickstart provider is a small standalone HTTP server that registers
# itself with the hub via a CatalogEntry. To exercise the full provider
# flow locally:
#
#   Terminal 1: make run-hub-embedded-static
#   Terminal 2: make install-provider-quickstart   # admin: register the entry
#   Terminal 3: make run-provider-quickstart       # tenant: run the binary
#
# The hub proxies /ui/providers/quickstart and /services/providers/quickstart
# to the binary in Terminal 3; the quickstart heartbeats every 30s so the
# hub's TTL-driven readiness stays True.

QUICKSTART_PORT ?= 8081
QUICKSTART_HUB_URL ?= $(DEV_HUB_URL)
QUICKSTART_TOKEN ?= $(STATIC_AUTH_TOKEN)
# kcp admin kubeconfig produced by embedded-kcp mode (see HUB_FLAGS_KCP_EMBEDDED).
QUICKSTART_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
# kcp apiserver URL for `kubectl apply` of the CatalogEntry. Embedded-kcp
# binds to :6443; Tiltfile.cluster (operator-deployed kcp) uses the envoy
# gateway at kcp.localhost:8443 and overrides this from the Tilt resource.
QUICKSTART_KCP_SERVER ?= https://localhost:6443
QUICKSTART_MANIFEST ?= providers/quickstart/manifest.yaml
# Declarative provisioning record: the hub's Provider controller creates the
# sub-workspace + ServiceAccount + kubeconfig Secret from this.
QUICKSTART_PROVIDER_MANIFEST ?= providers/quickstart/provider.yaml
QUICKSTART_WORKSPACE_PATH ?= root:railgrid:providers:quickstart
QUICKSTART_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/quickstart-runtime.kubeconfig

# --- edges provider (single provider, both kinds) --------------------------
# Runs SINGLE-REPLICA (revdial global dialer map). Reads a provider kubeconfig at
# runtime (token validation + cross-tenant controllers), so the run target passes
# RAILGRID_PROVIDER_KUBECONFIG unlike the broker providers.
EDGES_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
EDGES_KCP_SERVER ?= https://localhost:6443
EDGES_HUB_URL ?= $(DEV_HUB_URL)
EDGES_HUB_EXTERNAL_URL ?= $(EDGES_HUB_URL)
EDGES_TOKEN ?= $(STATIC_AUTH_TOKEN)
EDGES_PORT ?= 8088
EDGES_MANIFEST ?= providers/edges/manifest.yaml
EDGES_PROVIDER_MANIFEST ?= providers/edges/provider.yaml
EDGES_WORKSPACE_PATH ?= root:railgrid:providers:edges
EDGES_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/edges-runtime.kubeconfig
EDGES_KCP_DIR ?= $(CURDIR)/providers/edges/deploy/chart/files

## Run the quickstart provider binary locally. Heartbeats to the hub on
## $(QUICKSTART_HUB_URL); TLS verification skipped (dev cert is self-signed).
run-provider-quickstart: build-quickstart-provider ## Run the quickstart provider (requires: make run-hub-embedded-static + make install-provider-quickstart)
	@echo "Starting quickstart provider on :$(QUICKSTART_PORT)"
	@echo "  hub:   $(QUICKSTART_HUB_URL)"
	@echo "  token: $(QUICKSTART_TOKEN)"
	PORT=$(QUICKSTART_PORT) \
	RAILGRID_HUB_URL=$(QUICKSTART_HUB_URL) \
	RAILGRID_HUB_INSECURE=true \
	RAILGRID_PROVIDER_NAME=quickstart \
	RAILGRID_CATALOGENTRY_FILE=$(CURDIR)/providers/quickstart/manifest.yaml \
	RAILGRID_PROVIDER_KUBECONFIG=$(QUICKSTART_RUNTIME_KUBECONFIG) \
		$(BINDIR)/quickstart-provider

## Apply the quickstart CatalogEntry into root:railgrid:providers. Idempotent.
## Requires the hub to be running so the admin kubeconfig exists.
install-provider-quickstart: ## Apply quickstart Provider + CatalogEntry into root:railgrid:providers
	@test -f $(QUICKSTART_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(QUICKSTART_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=$(QUICKSTART_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(QUICKSTART_PROVIDER_MANIFEST) -f $(QUICKSTART_MANIFEST)

## Run provider e2e suite (embedded kcp + quickstart-provider subprocess).
## Lightweight — no kind/Helm, just two host binaries the suite drives over
## HTTP + kcp dynamic clients. RAILGRID_E2E_KEEP_DATA=true preserves logs/data.
## RAILGRID_E2E_KCP_IMAGE=<image> runs kcp from a published image instead
## (the same way `make tilt KCP_IMAGE=…` does), so a kcp PR build is tested by
## the whole suite without a source checkout; RAILGRID_E2E_HUB_VERBOSITY=<n>
## raises the hub's (and that kcp's) log level — 4 makes kcp's authorizers say
## why a virtual-workspace request was refused; RAILGRID_E2E_HUB_GOFLAGS passes
## extra flags to the hub build (e.g. -modfile=… to embed a kcp checkout).
E2E_PROVIDER_TIMEOUT ?= 10m
e2e-provider: build-hub build-quickstart-provider ## Run provider e2e suite
	@test -z "$$(lsof -ti :19443 :16443 :18081 :2380 2>/dev/null)" || { \
		echo "ports 19443/16443/18081/2380 are in use; stop any running railgrid-hub/quickstart-provider first"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/provider/... -v -timeout $(E2E_PROVIDER_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Run --providers flag mechanics suite (dep validation, unknown name,
## filtered enable). Each test spawns its own hub on the standard
## 19443/16443/2380 ports, so this MUST NOT run concurrently with
## `e2e-provider` — the Makefile checks the port up-front.
E2E_PROVIDER_FLAGS_TIMEOUT ?= 10m
e2e-provider-flags: build-hub ## Run --providers flag mechanics suite
	@test -z "$$(lsof -ti :19443 :16443 :2380 2>/dev/null)" || { \
		echo "ports 19443/16443/2380 are in use; stop any running railgrid-hub first (e.g. pkill railgrid-hub)"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/providerflags/... -v -timeout $(E2E_PROVIDER_FLAGS_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Run both provider suites back-to-back (sequential — they share port 2380).
e2e-provider-all: e2e-provider e2e-provider-flags ## Run provider + provider-flags suites sequentially

## Infrastructure provider e2e (embedded kcp + infrastructure-provider
## init/serve subprocesses). Covers the kcp-side surface the kind/kro
## template e2e can't: provisioning, init bootstrap + template seeding, the
## Template controller chain via the stub backend, retired-template
## enforcement, and the tenant catalog read through an APIBinding. Shares
## the embedded-kcp etcd port 2380 with the other subprocess suites — do
## not run them concurrently.
E2E_INFRA_PROVIDER_TIMEOUT ?= 15m
e2e-infra-provider: build-hub build-infrastructure-provider ## Run infrastructure provider e2e suite
	@test -z "$$(lsof -ti :19453 :16453 :18086 :2380 2>/dev/null)" || { \
		echo "ports 19453/16453/18086/2380 are in use; stop any running railgrid-hub/infrastructure-provider first"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/infraprovider/... -v -timeout $(E2E_INFRA_PROVIDER_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Hub scoped-identity e2e (embedded kcp + quickstart-provider as the
## REQUESTING provider). Covers pkg/hub/identity end to end: minting for a
## Greeting owner under clause A and using the token against /clusters/{id},
## the refusal codes for rules outside policy, refresh returning a new token
## for the same ServiceAccount, clause E composition (declared-but-unaccepted
## is refused, accepted at Enable is admitted, an undeclared verb stays
## refused), and the sweep collecting an identity whose owner was deleted.
## The suite registers its own synthetic dependency provider ("fixture") for
## the composition half — no second provider binary is built. It runs the hub
## with --provider-hub-access-platform-default=false so "refused until
## accepted" is a real assertion. The GC test is paced by the reconciler's
## 2-minute sweep, which is most of the wall time. Shares the embedded-kcp
## etcd port 2380 with the other subprocess suites — do not run them
## concurrently.
E2E_IDENTITY_TIMEOUT ?= 20m
.PHONY: e2e-identity
e2e-identity: build-hub build-quickstart-provider ## Run hub scoped-identity e2e suite
	@test -z "$$(lsof -ti :19503 :16503 :18128 :2380 2>/dev/null)" || { \
		echo "ports 19503/16503/18128/2380 are in use; stop any running railgrid-hub/quickstart-provider first"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/identity/... -v -timeout $(E2E_IDENTITY_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Kuery provider e2e (embedded kcp + kuery-provider init/serve subprocesses).
## Covers the provider-plumbing half of kuery: provisioning, init bootstrap
## (APIExport + SavedView schema + bind grant), the /api/providers DTO, the
## hub backend proxy, tenant Enable/Disable with SavedView round-trip, and the
## query surfaces that answer without a connected edge (/api/query-schema,
## /api/edges, /api/status). Fleet queries over real objects need the
## kind-based harness and are out of scope here. Shares the embedded-kcp etcd
## port 2380 with the other subprocess suites — do not run them concurrently.
E2E_KUERY_PROVIDER_TIMEOUT ?= 15m
e2e-kuery-provider: build-hub build-kuery-provider ## Run kuery provider e2e suite
	@test -z "$$(lsof -ti :19493 :16493 :18118 :2380 2>/dev/null)" || { \
		echo "ports 19493/16493/18118/2380 are in use; stop any running railgrid-hub/kuery-provider first"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/kueryprovider/... -v -timeout $(E2E_KUERY_PROVIDER_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Edges provider e2e (embedded kcp + edges-provider init/serve subprocesses).
## Covers the control-plane + auth surface of the decoupled edges provider:
## provisioning + CatalogEntry Ready, the /api/providers DTO, tenant Enable via
## APIBinding + KubernetesCluster/LinuxServer CR CRUD, and the edge-proxy
## authorization boundary (403 without grant -> 502 with grant, through the hub
## backend proxy). The data-plane tunnel (kubectl/ssh streaming) needs a live
## agent + edge target and is out of scope here. Shares the embedded-kcp etcd
## port 2380 with the other subprocess suites — do not run them concurrently.
E2E_EDGES_TIMEOUT ?= 15m
e2e-edges: build-hub build-edges-provider ## Run edges provider e2e suite
	@test -z "$$(lsof -ti :19463 :16463 :18088 :2380 2>/dev/null)" || { \
		echo "ports 19463/16463/18088/2380 are in use; stop any running railgrid-hub/edges-provider first"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/edges/... -v -timeout $(E2E_EDGES_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Edges DATA-PLANE connectivity e2e (embedded kcp over HTTPS + edges-provider
## + a real railgrid agent against a kind cluster). Proves the reverse tunnel:
## registers a KubernetesCluster, enables edges + the edge-proxy grant, runs the
## agent, and streams `kubectl get nodes` down the tunnel (agent -> hub backend
## proxy -> out-of-process edges provider -> agent -> kind API server). Needs
## kind + docker + kubectl on PATH. Also runs kuery-provider against the same
## engaged edge to prove fleet aggregation (TestKueryAggregatesEdgeObjects).
## Shares embedded-kcp etcd port 2380 — do not run concurrently with the other
## subprocess suites.
E2E_EDGES_CONN_TIMEOUT ?= 15m
e2e-edges-connectivity: build-hub build-edges-provider build-kuery-provider build-railgrid certs ## Run edges data-plane connectivity e2e (needs kind)
	@test -z "$$(lsof -ti :19473 :16473 :18098 :18099 :2380 2>/dev/null)" || { \
		echo "ports 19473/16473/18098/18099/2380 are in use; stop any running railgrid-hub/edges-provider first"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/edgesconn/... -v -timeout $(E2E_EDGES_CONN_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## CLI suite: every user-facing `railgrid` command as a real subprocess against a
## live hub (embedded kcp over HTTPS, two static-token users so membership
## commands can be exercised, plus the edges-provider for edge/connect/ssh).
## The server-edge path uses the in-process test sshd; the Kubernetes-edge
## path needs kind and skips without it. Shares embedded-kcp etcd port 2380 —
## do not run concurrently with the other subprocess suites.
E2E_CLI_TIMEOUT ?= 20m
e2e-cli: build-hub build-edges-provider build-railgrid certs ## Run the railgrid CLI e2e suite (kind optional)
	@test -z "$$(lsof -ti :19483 :16483 :18108 :2380 2>/dev/null)" || { \
		echo "ports 19483/16483/18108/2380 are in use; stop any running railgrid-hub/edges-provider first"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/cli/... -v -timeout $(E2E_CLI_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Tilt-cluster suite: runs against an ALREADY-RUNNING operator-deployed,
## multi-shard Tilt stack (start it in another terminal with `make tilt-cluster`).
## Unlike the other e2e suites it does NOT spawn its own processes — it connects
## to the live stack (kcp front-proxy via tilt-frontproxy.kubeconfig, the
## in-cluster hub, the host-run providers) and verifies the providers end-to-end:
## provider registration, the templates catalog/projection, MCP tool federation,
## and the per-tenant identity gate. Endpoints override via RAILGRID_E2E_* env.
E2E_TILT_TIMEOUT ?= 10m
E2E_TILT_HUB_URL ?= $(DEV_HUB_URL)
E2E_TILT_INFRA_URL ?= http://localhost:8082
E2E_TILT_KCP_KUBECONFIG ?= $(CURDIR)/tilt-frontproxy.kubeconfig
E2E_TILT_RUNTIME_KUBECONFIG ?= $(CURDIR)/.railgrid-cluster.kubeconfig
E2E_TILT_OPERATOR_NAMESPACE ?= railgrid-infrastructure-operator
E2E_TILT_CONFIG_CONNECTOR_TIMEOUT ?= 30m
E2E_TILT_TERRAFORM_TIMEOUT ?= 30m
KCC_INSTALL_SCRIPT ?= providers/infrastructure/contrib/config-connector/install.sh
KCC_ENABLE_SCRIPT ?= providers/infrastructure/contrib/config-connector/enable.sh
KCC_TEMPLATE_FILE ?= providers/infrastructure/contrib/config-connector/pubsub-template.yaml
KCC_PROVIDER_WORKSPACE ?= root:railgrid:providers:infrastructure
TERRAFORM_INSTALL_SCRIPT ?= providers/infrastructure/contrib/terraform/install.sh
TERRAFORM_ENABLE_SCRIPT ?= providers/infrastructure/contrib/terraform/enable.sh
TERRAFORM_TEMPLATE_FILE ?= providers/infrastructure/contrib/terraform/terraform-stack-template.yaml
TERRAFORM_PROVIDER_WORKSPACE ?= root:railgrid:providers:infrastructure
TERRAFORM_KIND_CLUSTER_NAME ?= kcp-tilt
INFRAKUBE_POC_COMMIT := 2fed999fb3c30e8415da5489eb8cf1eec8b765f0
INFRAKUBE_POC_IMAGE_TAG := $(shell printf '%s' $(INFRAKUBE_POC_COMMIT) | cut -c1-12)
INFRAKUBE_POC_CONTROLLER_IMAGE ?= railgrid/infrakube:$(INFRAKUBE_POC_IMAGE_TAG)
INFRAKUBE_POC_TASK_IMAGE ?= railgrid/infrakube-task:$(INFRAKUBE_POC_IMAGE_TAG)
# Public dev configuration uses RAILGRID_CONFIG_CONNECTOR_GCP_*; keep the older
# RAILGRID_E2E_GCP_* names as an explicit compatibility fallback for callers that
# invoke these targets from an exported environment.
KCC_GCP_PROJECT ?= $(or $(RAILGRID_CONFIG_CONNECTOR_GCP_PROJECT),$(RAILGRID_E2E_GCP_PROJECT))
KCC_GCP_CREDENTIALS_FILE ?= $(or $(RAILGRID_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE),$(RAILGRID_E2E_GCP_CREDENTIALS_FILE))
.PHONY: e2e-tilt-cluster
e2e-tilt-cluster: ## Run Tilt-cluster provider e2e (requires `make tilt-cluster` running)
	@curl -sk --max-time 5 -o /dev/null "$(E2E_TILT_HUB_URL)/healthz" || { \
		echo "hub not reachable at $(E2E_TILT_HUB_URL); bring the stack up first in another terminal: make tilt-cluster"; \
		exit 1; \
	}
	@curl -s --max-time 5 -o /dev/null "$(E2E_TILT_INFRA_URL)/healthz" || { \
		echo "infrastructure provider not reachable at $(E2E_TILT_INFRA_URL); is 'make tilt-cluster' fully up?"; \
		exit 1; \
	}
	go test -count=1 ./test/e2e/suites/tiltcluster/... -v -timeout $(E2E_TILT_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Opt-in demonstration of using Config Connector CRDs with the infrastructure
## operator's KRO runtime. The test creates only a minimal StorageBucket CRD; it
## does not install Config Connector or contact GCP. Override the runtime
## kubeconfig or operator namespace when using a non-default Tilt stack.
.PHONY: e2e-tilt-cluster-config-connector
e2e-tilt-cluster-config-connector: ## Run the opt-in Config Connector composition e2e
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { \
		echo "kcp front-proxy kubeconfig not found at $(E2E_TILT_KCP_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { \
		echo "runtime kubeconfig not found at $(E2E_TILT_RUNTIME_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	@curl -sk --max-time 5 -o /dev/null "$(E2E_TILT_HUB_URL)/healthz" || { \
		echo "hub not reachable at $(E2E_TILT_HUB_URL); bring the stack up first in another terminal: make tilt-cluster"; \
		exit 1; \
	}
	@curl -s --max-time 5 -o /dev/null "$(E2E_TILT_INFRA_URL)/healthz" || { \
		echo "infrastructure provider not reachable at $(E2E_TILT_INFRA_URL); is 'make tilt-cluster' fully up?"; \
		exit 1; \
	}
	RAILGRID_E2E_CONFIG_CONNECTOR_COMPOSITION=1 \
	RAILGRID_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
		go test -count=1 ./test/e2e/suites/tiltcluster/... -run '^TestConfigConnectorComposition$$' -v -timeout $(E2E_TILT_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Real-cloud extension of the infrastructure-operator Config Connector
## demonstration. Installation imports the caller-supplied service-account JSON
## into the runtime cluster, installs a checksum-pinned Config Connector
## operator, and waits for the PubSubTopic API. The run target then proves
## Pub/Sub creation and deletion independently via Google's REST API. Both
## targets are deliberately separate from the fake-CRD composition test above.
.PHONY: e2e-tilt-cluster-config-connector-gcp-install e2e-tilt-cluster-config-connector-gcp-run e2e-tilt-cluster-config-connector-gcp
e2e-tilt-cluster-config-connector-gcp-install: ## Install pinned Config Connector for the real Pub/Sub E2E
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { \
		echo "runtime kubeconfig not found at $(E2E_TILT_RUNTIME_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	@test -n "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "RAILGRID_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy RAILGRID_E2E_GCP_CREDENTIALS_FILE) is required"; exit 1; }
	@test -f "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "Config Connector credentials file does not exist"; exit 1; }
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_E2E_GCP_CREDENTIALS_FILE="$(KCC_GCP_CREDENTIALS_FILE)" \
		$(KCC_INSTALL_SCRIPT)

## Enable the checked-in Pub/Sub Template in the infrastructure provider
## workspace. This does not install Config Connector and is intentionally a
## separate manual action so ordinary Tilt startup remains cloud-neutral.
.PHONY: e2e-tilt-cluster-config-connector-enable
e2e-tilt-cluster-config-connector-enable: ## Apply and wait for the opt-in Pub/Sub Template/APIExport/KRO graph
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { \
		echo "kcp front-proxy kubeconfig not found at $(E2E_TILT_KCP_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { \
		echo "runtime kubeconfig not found at $(E2E_TILT_RUNTIME_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	RAILGRID_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_KCC_KCP_SERVER="$(KCC_KCP_SERVER)" \
	RAILGRID_KCC_PROVIDER_WORKSPACE="$(KCC_PROVIDER_WORKSPACE)" \
	RAILGRID_KCC_TEMPLATE_FILE="$(KCC_TEMPLATE_FILE)" \
		$(KCC_ENABLE_SCRIPT)

e2e-tilt-cluster-config-connector-gcp-run: ## Create then delete a real Pub/Sub topic through KRO and Config Connector
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { \
		echo "kcp front-proxy kubeconfig not found at $(E2E_TILT_KCP_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { \
		echo "runtime kubeconfig not found at $(E2E_TILT_RUNTIME_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	@test -n "$(KCC_GCP_PROJECT)" || { echo "RAILGRID_CONFIG_CONNECTOR_GCP_PROJECT (or legacy RAILGRID_E2E_GCP_PROJECT) is required"; exit 1; }
	@test -n "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "RAILGRID_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy RAILGRID_E2E_GCP_CREDENTIALS_FILE) is required"; exit 1; }
	@test -f "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "Config Connector credentials file does not exist"; exit 1; }
	RAILGRID_E2E_CONFIG_CONNECTOR_GCP=1 \
	RAILGRID_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
	RAILGRID_E2E_GCP_PROJECT="$(KCC_GCP_PROJECT)" \
	RAILGRID_E2E_GCP_CREDENTIALS_FILE="$(KCC_GCP_CREDENTIALS_FILE)" \
		go test -count=1 ./test/e2e/suites/tiltcluster/... -run '^TestConfigConnectorGCPPubSubLifecycle$$' -v -timeout $(E2E_TILT_CONFIG_CONNECTOR_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Smoke only the already-enabled stable Pub/Sub Template. Installation and
## Template enablement are separate manual actions; this target creates and
## deletes only the test-owned cloud topic and its tenant/KRO objects.
.PHONY: e2e-tilt-cluster-config-connector-smoke e2e-tilt-cluster-config-connector-gcp-smoke
e2e-tilt-cluster-config-connector-smoke: ## Create then delete one real Pub/Sub topic using the enabled Template
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { \
		echo "kcp front-proxy kubeconfig not found at $(E2E_TILT_KCP_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { \
		echo "runtime kubeconfig not found at $(E2E_TILT_RUNTIME_KUBECONFIG); bring the stack up first with: make tilt-cluster"; \
		exit 1; \
	}
	@test -n "$(KCC_GCP_PROJECT)" || { echo "RAILGRID_CONFIG_CONNECTOR_GCP_PROJECT (or legacy RAILGRID_E2E_GCP_PROJECT) is required"; exit 1; }
	@test -n "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "RAILGRID_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy RAILGRID_E2E_GCP_CREDENTIALS_FILE) is required"; exit 1; }
	@test -f "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "Config Connector credentials file does not exist"; exit 1; }
	RAILGRID_E2E_CONFIG_CONNECTOR_GCP=1 \
	RAILGRID_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
	RAILGRID_E2E_GCP_PROJECT="$(KCC_GCP_PROJECT)" \
	RAILGRID_E2E_GCP_CREDENTIALS_FILE="$(KCC_GCP_CREDENTIALS_FILE)" \
		go test -count=1 ./test/e2e/suites/tiltcluster/... -run '^TestConfigConnectorGCPPubSubSmoke$$' -v -timeout $(E2E_TILT_CONFIG_CONNECTOR_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-tilt-cluster-config-connector-gcp-smoke: e2e-tilt-cluster-config-connector-smoke

.PHONY: config-connector-install config-connector-enable config-connector-smoke
config-connector-install: e2e-tilt-cluster-config-connector-gcp-install
config-connector-enable: e2e-tilt-cluster-config-connector-enable
config-connector-smoke: e2e-tilt-cluster-config-connector-smoke

e2e-tilt-cluster-config-connector-gcp: ## Install Config Connector and run the real Pub/Sub create/delete E2E
	@test -n "$(KCC_GCP_PROJECT)" || { echo "RAILGRID_CONFIG_CONNECTOR_GCP_PROJECT (or legacy RAILGRID_E2E_GCP_PROJECT) is required"; exit 1; }
	@test -n "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "RAILGRID_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy RAILGRID_E2E_GCP_CREDENTIALS_FILE) is required"; exit 1; }
	@test -f "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "Config Connector credentials file does not exist"; exit 1; }
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { echo "kcp front-proxy kubeconfig is required before Config Connector is installed"; exit 1; }
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { echo "runtime kubeconfig is required before Config Connector is installed"; exit 1; }
	@curl -sk --max-time 5 -o /dev/null "$(E2E_TILT_HUB_URL)/healthz" || { echo "hub must be healthy before Config Connector is installed"; exit 1; }
	@curl -s --max-time 5 -o /dev/null "$(E2E_TILT_INFRA_URL)/healthz" || { echo "infrastructure provider must be healthy before Config Connector is installed"; exit 1; }
	$(MAKE) e2e-tilt-cluster-config-connector-gcp-install
	$(MAKE) e2e-tilt-cluster-config-connector-gcp-run

## Opt-in Terraform composition check. It installs only a minimal test-owned
## Infrakube Terraform CRD and proves the infrastructure operator publishes a
## tenant instance that KRO composes into the expected runtime child.
.PHONY: e2e-tilt-cluster-terraform
e2e-tilt-cluster-terraform: ## Run the credential-free Terraform composition e2e
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { echo "kcp front-proxy kubeconfig is required; run make tilt-cluster first"; exit 1; }
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { echo "runtime kubeconfig is required; run make tilt-cluster first"; exit 1; }
	@curl -sk --max-time 5 -o /dev/null "$(E2E_TILT_HUB_URL)/healthz" || { echo "hub must be healthy before the Terraform composition test"; exit 1; }
	@curl -s --max-time 5 -o /dev/null "$(E2E_TILT_INFRA_URL)/healthz" || { echo "infrastructure provider must be healthy before the Terraform composition test"; exit 1; }
	RAILGRID_E2E_TERRAFORM_COMPOSITION=1 \
	RAILGRID_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
		go test -count=1 ./test/e2e/suites/tiltcluster/... -run '^TestTerraformComposition$$' -v -timeout $(E2E_TILT_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

.PHONY: e2e-tilt-cluster-terraform-install e2e-tilt-cluster-terraform-enable e2e-tilt-cluster-terraform-smoke e2e-tilt-cluster-terraform-infrakube
e2e-tilt-cluster-terraform-install: ## Install pinned Infrakube into the operator-managed runtime
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { echo "runtime kubeconfig is required; run make tilt-cluster first"; exit 1; }
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_TERRAFORM_KIND_CLUSTER_NAME="$(TERRAFORM_KIND_CLUSTER_NAME)" \
	INFRAKUBE_COMMIT="$(INFRAKUBE_POC_COMMIT)" \
	CONTROLLER_IMAGE="$(INFRAKUBE_POC_CONTROLLER_IMAGE)" \
	TASK_IMAGE="$(INFRAKUBE_POC_TASK_IMAGE)" \
		$(TERRAFORM_INSTALL_SCRIPT)

e2e-tilt-cluster-terraform-enable: ## Enable and wait for the opt-in Terraform Template
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { echo "kcp front-proxy kubeconfig is required; run make tilt-cluster first"; exit 1; }
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { echo "runtime kubeconfig is required; run make tilt-cluster first"; exit 1; }
	RAILGRID_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_TERRAFORM_KCP_SERVER="$(TERRAFORM_KCP_SERVER)" \
	RAILGRID_TERRAFORM_PROVIDER_WORKSPACE="$(TERRAFORM_PROVIDER_WORKSPACE)" \
	RAILGRID_TERRAFORM_TEMPLATE_FILE="$(TERRAFORM_TEMPLATE_FILE)" \
		$(TERRAFORM_ENABLE_SCRIPT)

e2e-tilt-cluster-terraform-smoke: ## Apply and destroy Terraform through the enabled Template
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { echo "kcp front-proxy kubeconfig is required; run make tilt-cluster first"; exit 1; }
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { echo "runtime kubeconfig is required; run make tilt-cluster first"; exit 1; }
	RAILGRID_E2E_TERRAFORM=1 \
	RAILGRID_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	RAILGRID_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	RAILGRID_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
		go test -count=1 ./test/e2e/suites/tiltcluster/... -run '^TestTerraformInfrakubeSmoke$$' -v -timeout $(E2E_TILT_TERRAFORM_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

.PHONY: terraform-install terraform-enable terraform-smoke
terraform-install: e2e-tilt-cluster-terraform-install
terraform-enable: e2e-tilt-cluster-terraform-enable
terraform-smoke: e2e-tilt-cluster-terraform-smoke

e2e-tilt-cluster-terraform-infrakube: ## Install, enable, and smoke Terraform through Infrakube
	$(MAKE) terraform-install
	$(MAKE) terraform-enable
	$(MAKE) terraform-smoke

## Create quickstart's APIExport (+ endpoint slice + bind grant) inside its
## provider workspace, so tenants can Enable it. The Provider controller already
## minted the provider-token Secret on register; we read it, write a dev runtime
## kubeconfig targeting the sub-workspace, and run the provider's `init`
## (sdkinstall.Bootstrap). Idempotent. Order: install-provider-quickstart →
## this → Enable works. Mirrors init-provider-kuery/code/infrastructure.
init-provider-quickstart: build-quickstart-provider ## Bootstrap quickstart APIExport + write dev runtime kubeconfig
	@test -f $(QUICKSTART_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(QUICKSTART_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Reading provider-token from $(QUICKSTART_WORKSPACE_PATH) and writing $(QUICKSTART_RUNTIME_KUBECONFIG)"
	@TOKEN=$$(kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=$(QUICKSTART_KCP_SERVER)/clusters/$(QUICKSTART_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: railgrid\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: railgrid\n  context:\n    cluster: railgrid\n    user: railgrid\ncurrent-context: railgrid\nusers:\n- name: railgrid\n  user:\n    token: %s\n' \
		"$(QUICKSTART_KCP_SERVER)/clusters/$(QUICKSTART_WORKSPACE_PATH)" "$$TOKEN" \
		> $(QUICKSTART_RUNTIME_KUBECONFIG)
	@echo "Running quickstart-provider init (creates APIExport + endpoint slice + bind grant)"
	RAILGRID_PROVIDER_KUBECONFIG=$(QUICKSTART_RUNTIME_KUBECONFIG) \
	QUICKSTART_WORKSPACE_PATH=$(QUICKSTART_WORKSPACE_PATH) \
	RAILGRID_KCP_DIR=providers/quickstart/deploy/chart/files \
	RAILGRID_DATAPLANE_URL=http://localhost:$(QUICKSTART_PORT) \
		$(BINDIR)/quickstart-provider init

## Delete the quickstart CatalogEntry + Provider. Deleting the Provider triggers
## full teardown of root:railgrid:providers:quickstart (workspace, SA, APIExport)
## via the controller's finalizer.
uninstall-provider-quickstart: ## Delete quickstart CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=$(QUICKSTART_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(QUICKSTART_MANIFEST) -f $(QUICKSTART_PROVIDER_MANIFEST)

# --- Provider kuery (local dev) ---
# Mirror of the quickstart pattern above. Distinct port (8084) so the demo
# providers can run side-by-side. Phase 1 skeleton — see
# docs/kuery-provider-architecture.md for the phasing.

KUERY_PORT ?= 8084
KUERY_HUB_URL ?= $(DEV_HUB_URL)
KUERY_TOKEN ?= $(STATIC_AUTH_TOKEN)
KUERY_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
KUERY_KCP_SERVER ?= https://localhost:6443
KUERY_MANIFEST ?= providers/kuery/manifest.yaml
KUERY_PROVIDER_MANIFEST ?= providers/kuery/provider.yaml
KUERY_WORKSPACE_PATH ?= root:railgrid:providers:kuery
KUERY_KCP_DIR ?= providers/kuery/deploy/chart/files
# Dev runtime kubeconfig for the engagement controller, written by
# init-provider-kuery from the provider SA token the hub mints.
KUERY_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/kuery-runtime.kubeconfig

# Local store backend. Dev always runs Postgres — the same backend as
# production — because Postgres-only SQL (jsonb_array_elements, uuid columns,
# …) diverges from SQLite and passing on SQLite has shipped real query bugs.
# kuery-db-up starts a throwaway container matching KUERY_DEV_DATABASE_URL.
KUERY_POSTGRES_CONTAINER ?= railgrid-kuery-postgres
# Pulled from Google's Docker Hub mirror (same official image, more reliable
# pulls — Docker Hub has been dropping them with "unexpected EOF").
KUERY_POSTGRES_IMAGE ?= mirror.gcr.io/library/postgres:16-alpine
KUERY_POSTGRES_PORT ?= 55433
KUERY_POSTGRES_DATA_DIR ?= $(KCP_DATA_DIR)/kuery-postgres
KUERY_POSTGRES_USER ?= kuery
KUERY_POSTGRES_PASSWORD ?= kuery
KUERY_POSTGRES_DB ?= kuery
KUERY_DEV_DATABASE_URL ?= postgres://$(KUERY_POSTGRES_USER):$(KUERY_POSTGRES_PASSWORD)@localhost:$(KUERY_POSTGRES_PORT)/$(KUERY_POSTGRES_DB)?sslmode=disable
# Connection string the provider uses. Defaults to the local dev container
# above; point it at any external Postgres to override.
KUERY_STORE_DSN ?=

kuery-db-up: ## Start/reuse local Postgres for the kuery store (no-op when KUERY_STORE_DSN points at an external DB)
	@if [ -n "$(KUERY_STORE_DSN)" ]; then \
		echo "Using externally configured KUERY_STORE_DSN; not starting local kuery Postgres"; \
		exit 0; \
	fi; \
	mkdir -p "$(KUERY_POSTGRES_DATA_DIR)"; \
	if docker ps --format '{{.Names}}' | grep -qx "$(KUERY_POSTGRES_CONTAINER)"; then \
		echo "kuery Postgres already running ($(KUERY_POSTGRES_CONTAINER))"; \
	elif docker ps -a --format '{{.Names}}' | grep -qx "$(KUERY_POSTGRES_CONTAINER)"; then \
		echo "Starting existing kuery Postgres container ($(KUERY_POSTGRES_CONTAINER))"; \
		docker start "$(KUERY_POSTGRES_CONTAINER)" >/dev/null; \
	else \
		echo "Creating kuery Postgres container ($(KUERY_POSTGRES_CONTAINER))"; \
		docker run -d \
			--name "$(KUERY_POSTGRES_CONTAINER)" \
			-e POSTGRES_USER="$(KUERY_POSTGRES_USER)" \
			-e POSTGRES_PASSWORD="$(KUERY_POSTGRES_PASSWORD)" \
			-e POSTGRES_DB="$(KUERY_POSTGRES_DB)" \
			-p 127.0.0.1:$(KUERY_POSTGRES_PORT):5432 \
			-v "$(abspath $(KUERY_POSTGRES_DATA_DIR)):/var/lib/postgresql/data" \
			"$(KUERY_POSTGRES_IMAGE)" >/dev/null; \
	fi; \
	echo "Waiting for kuery Postgres..."; \
	for _ in $$(seq 1 30); do \
		if docker exec "$(KUERY_POSTGRES_CONTAINER)" pg_isready -U "$(KUERY_POSTGRES_USER)" -d "$(KUERY_POSTGRES_DB)" >/dev/null 2>&1; then \
			echo "  database: $(KUERY_DEV_DATABASE_URL)"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "ERROR: kuery Postgres did not become ready"; \
	docker logs "$(KUERY_POSTGRES_CONTAINER)" --tail=50; \
	exit 1

kuery-db-down: ## Stop and remove the local kuery Postgres container (data remains in KUERY_POSTGRES_DATA_DIR)
	@if docker ps -a --format '{{.Names}}' | grep -qx "$(KUERY_POSTGRES_CONTAINER)"; then \
		echo "Removing kuery Postgres container ($(KUERY_POSTGRES_CONTAINER))"; \
		docker rm -f "$(KUERY_POSTGRES_CONTAINER)" >/dev/null; \
	else \
		echo "No kuery Postgres container to remove ($(KUERY_POSTGRES_CONTAINER))"; \
	fi

run-provider-kuery: build-kuery-provider kuery-db-up ## Run the kuery provider (requires: make run-hub-embedded-static + make install-provider-kuery; engagement needs init-provider-kuery)
	@echo "Starting kuery provider on :$(KUERY_PORT)"
	@echo "  hub:   $(KUERY_HUB_URL)"
	@echo "  token: $(KUERY_TOKEN)"
	@test -f $(KUERY_RUNTIME_KUBECONFIG) || { \
		echo "provider kubeconfig not found at $(KUERY_RUNTIME_KUBECONFIG)"; \
		echo "serve refuses to start without it: run 'make init-provider-kuery' after install-provider-kuery"; \
		exit 1; \
	}
	@echo "  kubeconfig: $(KUERY_RUNTIME_KUBECONFIG)"
	@# Dev always runs Postgres. Fall back to the local dev container DSN when
	@# KUERY_STORE_DSN is unset (external Postgres overrides it).
	STORE_DSN="$${KUERY_STORE_DSN:-$(KUERY_STORE_DSN)}"; \
	if [ -z "$$STORE_DSN" ]; then \
		STORE_DSN="$(KUERY_DEV_DATABASE_URL)"; \
	fi; \
	echo "  store: postgres ($$STORE_DSN)"; \
	PORT=$(KUERY_PORT) \
	RAILGRID_HUB_URL=$(KUERY_HUB_URL) \
	RAILGRID_HUB_INSECURE=true \
	RAILGRID_PROVIDER_NAME=kuery \
	RAILGRID_CATALOGENTRY_FILE=$(CURDIR)/providers/kuery/manifest.yaml \
	RAILGRID_PROVIDER_KUBECONFIG=$(KUERY_RUNTIME_KUBECONFIG) \
	KUERY_STORE_DRIVER=postgres \
	KUERY_STORE_DSN="$$STORE_DSN" \
		$(BINDIR)/kuery-provider

install-provider-kuery: ## Apply kuery Provider + CatalogEntry into root:railgrid:providers
	@test -f $(KUERY_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KUERY_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KUERY_KCP_KUBECONFIG) \
		--server=$(KUERY_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(KUERY_PROVIDER_MANIFEST) -f $(KUERY_MANIFEST)

## Dev bootstrap for the engagement controller. The Provider controller writes
## the minted kubeconfig into a Secret in root:railgrid:providers, but host-binary
## dev needs a host-reachable server URL — so we read the provider SA token from
## the sub-workspace (the same token the Provider controller minted) and write a
## dev kubeconfig with the local server URL, plus the APIExportEndpointSlice the
## engagement watcher discovers VW URLs from.
## Order: install-provider-kuery (Provider CR applied → controller provisions
## the sub-workspace + provider-token Secret) → this → the kuery Tilt resource
## restarts on the kubeconfig file appearing.
init-provider-kuery: build-kuery-provider ## Bootstrap kuery APIExport (schemas+slice+bind grant) + write dev runtime kubeconfig
	@test -f $(KUERY_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KUERY_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Reading provider-token from $(KUERY_WORKSPACE_PATH) and writing $(KUERY_RUNTIME_KUBECONFIG)"
	@TOKEN=$$(kubectl --kubeconfig=$(KUERY_KCP_KUBECONFIG) \
		--server=$(KUERY_KCP_SERVER)/clusters/$(KUERY_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: railgrid\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: railgrid\n  context:\n    cluster: railgrid\n    user: railgrid\ncurrent-context: railgrid\nusers:\n- name: railgrid\n  user:\n    token: %s\n' \
		"$(KUERY_KCP_SERVER)/clusters/$(KUERY_WORKSPACE_PATH)" "$$TOKEN" \
		> $(KUERY_RUNTIME_KUBECONFIG)
	@# No identity hashes: kuery claims no first-party resources — edge
	@# discovery acts as a per-workspace ServiceAccount through each tenant's
	@# own edges binding. The slice + bind grant are created inside
	@# install.Bootstrap using the provider SA (cluster-admin → has `bind`),
	@# not the admin kubeconfig.
	@echo "Running kuery-provider init (schemas + APIExport + endpoint slice + bind grant)"
	RAILGRID_PROVIDER_KUBECONFIG=$(KUERY_RUNTIME_KUBECONFIG) \
	KUERY_WORKSPACE_PATH=$(KUERY_WORKSPACE_PATH) \
	RAILGRID_KCP_DIR=$(KUERY_KCP_DIR) \
	RAILGRID_DATAPLANE_URL=http://localhost:$(KUERY_PORT) \
		$(BINDIR)/kuery-provider init

uninstall-provider-kuery: ## Delete kuery CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KUERY_KCP_KUBECONFIG) \
		--server=$(KUERY_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(KUERY_MANIFEST) -f $(KUERY_PROVIDER_MANIFEST)

# --- Provider infrastructure (local dev) ---
# Mirror of the quickstart pattern above. Distinct port (8082) so both
# providers can run side-by-side under Tilt. Iteration loop:
#
#   Terminal 1: make run-hub-embedded-static
#   Terminal 2: make install-provider-infrastructure    # admin: register entry
#   Terminal 3: make run-provider-infrastructure        # tenant: run binary
#
KROMC_PORT ?= 8082
KROMC_HUB_URL ?= $(DEV_HUB_URL)
KROMC_TOKEN ?= $(STATIC_AUTH_TOKEN)
KROMC_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
# Same override story as QUICKSTART_KCP_SERVER above — Tiltfile.cluster
# repoints this at the envoy gateway.
KROMC_KCP_SERVER ?= https://localhost:6443
KROMC_MANIFEST ?= providers/infrastructure/manifest.yaml
KROMC_PROVIDER_MANIFEST ?= providers/infrastructure/provider.yaml

# --- App Studio provider (local dev) ---
# Same pattern as quickstart/infrastructure/code: local dev applies the
# checked-in manifest.yaml; the Helm chart's CatalogEntry is for in-cluster
# self-registration via ConfigMap.
APP_STUDIO_PORT ?= 8085
APP_STUDIO_HUB_URL ?= $(DEV_HUB_URL)
# Browser-reachable hub origin for private preview authorization. Normal local
# development uses the same localhost origin; deployments with an internal hub
# route should override this independently.
APP_STUDIO_HUB_PUBLIC_URL ?= $(APP_STUDIO_HUB_URL)
APP_STUDIO_TOKEN ?= $(STATIC_AUTH_TOKEN)
# Optional external HTTPS hub origin for generated development runtimes. Keep
# unset unless the operator has configured a pod-reachable, certificate-valid
# URL; the App Studio launcher must not invent an insecure localhost default.
RAILGRID_ACTIONS_EXTERNAL_URL ?=
APP_STUDIO_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
APP_STUDIO_KCP_SERVER ?= https://localhost:6443
APP_STUDIO_WORKSPACE_PATH ?= root:railgrid:providers:app-studio
APP_STUDIO_PROVIDER_KUBECONFIG ?= $(KCP_DATA_DIR)/app-studio-provider.kubeconfig
APP_STUDIO_KCP_DIR ?= providers/app-studio/deploy/chart/files
APP_STUDIO_MANIFEST ?= providers/app-studio/manifest.yaml
APP_STUDIO_PROVIDER_MANIFEST ?= providers/app-studio/provider.yaml
APP_STUDIO_DATABASE_URL ?=
APP_STUDIO_IN_MEMORY_MESSAGE_STORE ?=
APP_STUDIO_DEV_DATABASE_URL ?= postgres://appstudio:appstudio@localhost:55432/appstudio?sslmode=disable
APP_STUDIO_POSTGRES_CONTAINER ?= railgrid-app-studio-postgres
APP_STUDIO_POSTGRES_IMAGE ?= mirror.gcr.io/library/postgres:16-alpine
APP_STUDIO_POSTGRES_PORT ?= 55432
APP_STUDIO_POSTGRES_DATA_DIR ?= $(KCP_DATA_DIR)/app-studio-postgres
APP_STUDIO_POSTGRES_USER ?= appstudio
APP_STUDIO_POSTGRES_PASSWORD ?= appstudio
APP_STUDIO_POSTGRES_DB ?= appstudio
APP_STUDIO_PREVIEW_BRIDGE_DEV_KEY_DIR ?= $(KCP_DATA_DIR)/app-studio-preview-bridge
APP_STUDIO_PREVIEW_BRIDGE_DEV_PRIVATE_KEY ?= $(APP_STUDIO_PREVIEW_BRIDGE_DEV_KEY_DIR)/private-key.pem
APP_STUDIO_PREVIEW_BRIDGE_DEV_JWKS ?= $(APP_STUDIO_PREVIEW_BRIDGE_DEV_KEY_DIR)/verification-jwks.json
APP_STUDIO_PREVIEW_BRIDGE_DEV_KEY_ID ?= $(APP_STUDIO_PREVIEW_BRIDGE_DEV_KEY_DIR)/key-id

# --- agents provider (long-running personal AI agents) ---
AGENTS_PORT ?= 8087
AGENTS_HUB_URL ?= $(DEV_HUB_URL)
AGENTS_TOKEN ?= $(STATIC_AUTH_TOKEN)
AGENTS_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
AGENTS_KCP_SERVER ?= https://localhost:6443
AGENTS_WORKSPACE_PATH ?= root:railgrid:providers:agents
AGENTS_PROVIDER_KUBECONFIG ?= $(KCP_DATA_DIR)/agents-provider.kubeconfig
AGENTS_KCP_DIR ?= providers/agents/deploy/chart/files
AGENTS_MANIFEST ?= providers/agents/manifest.yaml
AGENTS_PROVIDER_MANIFEST ?= providers/agents/provider.yaml
# Durable store: dev runs use a local Postgres container by default (mirrors
# app-studio). Set AGENTS_IN_MEMORY_STORE=true for a non-durable quick run.
AGENTS_IN_MEMORY_STORE ?=
AGENTS_DEV_DATABASE_URL ?= postgres://agents:agents@localhost:55434/agents?sslmode=disable
AGENTS_POSTGRES_CONTAINER ?= railgrid-agents-postgres
AGENTS_POSTGRES_IMAGE ?= mirror.gcr.io/library/postgres:16-alpine
AGENTS_POSTGRES_PORT ?= 55434
AGENTS_POSTGRES_DATA_DIR ?= $(KCP_DATA_DIR)/agents-postgres

## Run the infrastructure provider binary locally. Heartbeats to the hub on
## $(KROMC_HUB_URL); TLS verification skipped (dev cert is self-signed).
## KRO_KUBECONFIG is left unset by default → provider serves the baked-in
## stub catalog so the UI is demoable without standing up a real central
## kro cluster. Point KRO_KUBECONFIG at a real kubeconfig to use the
## real client + your own ResourceGraphDefinitions.
run-provider-infrastructure: build-infrastructure-provider app-studio-preview-bridge-dev-key ## Run the infrastructure provider (requires: make run-hub-embedded-static + make install-provider-infrastructure)
	@echo "Starting infrastructure provider on :$(KROMC_PORT)"
	@echo "  hub:   $(KROMC_HUB_URL)"
	@echo "  token: $(KROMC_TOKEN)"
	@# Prefer an explicit KRO_KUBECONFIG from the caller's env, then
	@# fall back to the dev-kro management cluster's kubeconfig when
	@# present, and finally to stub mode if neither exists.
	@if [ -n "$$KRO_KUBECONFIG" ]; then \
		echo "  kro:   $$KRO_KUBECONFIG (from env)"; \
	elif [ -f "$(KRO_KIND_KUBECONFIG)" ]; then \
		echo "  kro:   $(KRO_KIND_KUBECONFIG) (dev-kro management cluster)"; \
	else \
		echo "  kro:   <unset → stub catalog; run 'make dev-kro-up' for real RGDs>"; \
	fi
	PORT=$(KROMC_PORT) \
	RAILGRID_HUB_URL=$(KROMC_HUB_URL) \
	RAILGRID_HUB_INSECURE=true \
	RAILGRID_PROVIDER_NAME=infrastructure \
	RAILGRID_CATALOGENTRY_FILE=$(CURDIR)/providers/infrastructure/manifest.yaml \
	KRO_KUBECONFIG=$${KRO_KUBECONFIG:-$$( [ -f "$(KRO_KIND_KUBECONFIG)" ] && echo "$(KRO_KIND_KUBECONFIG)" )} \
	RAILGRID_PROVIDER_KUBECONFIG=$${RAILGRID_PROVIDER_KUBECONFIG:-$(INFRASTRUCTURE_RUNTIME_KUBECONFIG)} \
	RAILGRID_APP_BASE_DOMAIN=$${RAILGRID_APP_BASE_DOMAIN:-apps.127.0.0.1.sslip.io} \
	RAILGRID_GATEWAY_NAME=$${RAILGRID_GATEWAY_NAME:-cloudflare-tunnel} \
	RAILGRID_GATEWAY_NAMESPACE=$${RAILGRID_GATEWAY_NAMESPACE:-cfgate-system} \
	RAILGRID_APP_PUBLIC_PORT=$${RAILGRID_APP_PUBLIC_PORT-10443} \
	RAILGRID_PREVIEW_BRIDGE_VERIFICATION_JWKS="$$(cat "$(APP_STUDIO_PREVIEW_BRIDGE_DEV_JWKS)")" \
		$(BINDIR)/infrastructure-provider

run-provider-infrastructure-operator: build-infrastructure-provider app-studio-preview-bridge-dev-key ## Run the infrastructure provider in OPERATOR mode (bootstrap reconcile + serve from a provider + runtime kubeconfig)
	@echo "Starting infrastructure provider (operator) on :$(KROMC_PORT)"
	@echo "  hub:      $(KROMC_HUB_URL)"
	@echo "  provider: $${INFRASTRUCTURE_PROVIDER_KUBECONFIG:-$(KROMC_KCP_KUBECONFIG)} (kcp)"
	@echo "  runtime:  $${INFRASTRUCTURE_RUNTIME_KUBECONFIG:-$(KRO_KIND_KUBECONFIG)} (kro cluster)"
	@echo "  ws:       $(INFRASTRUCTURE_WORKSPACE_PATH)"
	@# The operator needs the provider workspace to already exist — run
	@# `make install-provider-infrastructure` (admin-portal onboarding in prod)
	@# first. It then reconciles the in-workspace bootstrap and seeds kro itself.
	PORT=$(KROMC_PORT) \
	RAILGRID_HUB_URL=$(KROMC_HUB_URL) \
	RAILGRID_HUB_INSECURE=true \
	RAILGRID_PROVIDER_NAME=infrastructure \
	INFRASTRUCTURE_WORKSPACE_PATH=$(INFRASTRUCTURE_WORKSPACE_PATH) \
	INFRASTRUCTURE_PROVIDER_KUBECONFIG=$${INFRASTRUCTURE_PROVIDER_KUBECONFIG:-$(KROMC_KCP_KUBECONFIG)} \
	INFRASTRUCTURE_RUNTIME_KUBECONFIG=$${INFRASTRUCTURE_RUNTIME_KUBECONFIG:-$$( [ -f "$(KRO_KIND_KUBECONFIG)" ] && echo "$(KRO_KIND_KUBECONFIG)" )} \
	RAILGRID_PREVIEW_BRIDGE_VERIFICATION_JWKS="$$(cat "$(APP_STUDIO_PREVIEW_BRIDGE_DEV_JWKS)")" \
		$(BINDIR)/infrastructure-provider operator

# ── CRD-driven operator (controller) dev flow ───────────────────────────────
# Replaces kro-mgmt-up + infrastructure-init: one host-binary controller that
# bootstraps the workspace, helm-installs kro (with the kind hostAliases +
# self-cluster patches), and (skip-serve in dev) leaves serve to the host binary.
INFRA_OPERATOR_NS ?= railgrid-infrastructure-operator
INFRA_OPERATOR_PROVIDER_KC ?= $(KROMC_KCP_KUBECONFIG)
INFRA_OPERATOR_RUNTIME_KC ?= $(KRO_KIND_KUBECONFIG)
INFRA_OPERATOR_KIND_NAME ?= $(KRO_KIND_NAME)
INFRA_OPERATOR_CRD ?= providers/infrastructure/config/crds/infrastructure.railgrid.ai_infrastructureproviders.yaml

run-provider-infrastructure-controller: build-infrastructure-provider app-studio-preview-bridge-dev-key ## Apply the operator CRD/Secrets/CR into the runtime cluster and run the controller (dev)
	@echo "Applying operator CRD + Secrets + CR into runtime cluster ($(INFRA_OPERATOR_RUNTIME_KC))"
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f $(INFRA_OPERATOR_CRD)
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl create namespace $(INFRA_OPERATOR_NS) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl -n $(INFRA_OPERATOR_NS) create secret generic provider-kubeconfig --from-file=kubeconfig=$(INFRA_OPERATOR_PROVIDER_KC) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl -n $(INFRA_OPERATOR_NS) create secret generic runtime-kubeconfig --from-file=kubeconfig=$(INFRA_OPERATOR_RUNTIME_KC) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	@printf 'apiVersion: infrastructure.railgrid.ai/v1alpha1\nkind: InfrastructureProvider\nmetadata:\n  name: infrastructure\n  namespace: %s\nspec:\n  providerWorkspace: %s\n  providerKubeconfigSecret:\n    name: provider-kubeconfig\n  runtimeKubeconfigSecret:\n    name: runtime-kubeconfig\n  kro:\n    chart: %s\n    version: %s\n  provider:\n    image:\n      repository: ghcr.io/railgrid/railgrid-infrastructure-provider\n      tag: dev\n' "$(INFRA_OPERATOR_NS)" "$(INFRASTRUCTURE_WORKSPACE_PATH)" "$(KRO_CHART)" "$(KRO_CHART_VERSION)" | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	@echo "Running infrastructure operator controller (KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC), skip-serve)"
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) \
	INFRASTRUCTURE_WORKSPACE_PATH=$(INFRASTRUCTURE_WORKSPACE_PATH) \
	INFRASTRUCTURE_OPERATOR_SKIP_SERVE=true \
	RAILGRID_PREVIEW_BRIDGE_VERIFICATION_JWKS="$$(cat "$(APP_STUDIO_PREVIEW_BRIDGE_DEV_JWKS)")" \
		$(BINDIR)/infrastructure-provider controller

## Run the App Studio provider binary locally. Mirrors the other external
## providers so the heartbeat path is consistent and the UI runs on :8085.
app-studio-preview-bridge-dev-key: ## Generate/reuse the local preview-bridge signing key and public JWKS under .kcp
	@node providers/app-studio/hack/preview-bridge-dev-keys.mjs \
		--output-dir "$(abspath $(APP_STUDIO_PREVIEW_BRIDGE_DEV_KEY_DIR))"

verify-app-studio-preview-bridge-dev-key: ## Verify local preview-bridge key generation, repair, and concurrency
	node --test providers/app-studio/hack/preview-bridge-dev-keys.test.mjs

verify-app-studio-eval: ## Verify the terminal-aware assistant evaluation harness
	@output="$$(node providers/app-studio/hack/eval/terminal-turn.test.mjs 2>&1)"; \
	status=$$?; \
	printf '%s\n' "$$output"; \
	if [ "$$status" -ne 0 ]; then exit "$$status"; fi; \
	declared="$$(grep -c '^test(' providers/app-studio/hack/eval/terminal-turn.test.mjs || true)"; \
	reported="$$(printf '%s\n' "$$output" | awk '$$1 == "#" && $$2 == "tests" { print $$3 }' | tail -n 1)"; \
	if [ "$$reported" != "$$declared" ]; then \
		echo "App Studio eval expected $$declared TAP tests, got $${reported:-0}" >&2; \
		exit 1; \
	fi

app-studio-db-up: ## Start/reuse local Postgres for App Studio message history (skips when APP_STUDIO_DATABASE_URL or in-memory mode is set)
	@set -a; [ -f providers/app-studio/.env ] && . ./providers/app-studio/.env || true; set +a; \
	APP_STUDIO_DATABASE_URL="$${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DATABASE_URL)}"; \
	APP_STUDIO_IN_MEMORY_MESSAGE_STORE="$${APP_STUDIO_IN_MEMORY_MESSAGE_STORE:-$(APP_STUDIO_IN_MEMORY_MESSAGE_STORE)}"; \
	if [ "$${APP_STUDIO_IN_MEMORY_MESSAGE_STORE:-}" = "true" ]; then \
		echo "Skipping App Studio Postgres because APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true"; \
		exit 0; \
	fi; \
	if [ -n "$${APP_STUDIO_DATABASE_URL:-}" ]; then \
		echo "Using externally configured APP_STUDIO_DATABASE_URL; not starting local App Studio Postgres"; \
		exit 0; \
	fi; \
	mkdir -p "$(APP_STUDIO_POSTGRES_DATA_DIR)"; \
	if docker ps --format '{{.Names}}' | grep -qx "$(APP_STUDIO_POSTGRES_CONTAINER)"; then \
		echo "App Studio Postgres already running ($(APP_STUDIO_POSTGRES_CONTAINER))"; \
	elif docker ps -a --format '{{.Names}}' | grep -qx "$(APP_STUDIO_POSTGRES_CONTAINER)"; then \
		echo "Starting existing App Studio Postgres container ($(APP_STUDIO_POSTGRES_CONTAINER))"; \
		docker start "$(APP_STUDIO_POSTGRES_CONTAINER)" >/dev/null; \
	else \
		echo "Creating App Studio Postgres container ($(APP_STUDIO_POSTGRES_CONTAINER))"; \
		docker run -d \
			--name "$(APP_STUDIO_POSTGRES_CONTAINER)" \
			-e POSTGRES_USER="$(APP_STUDIO_POSTGRES_USER)" \
			-e POSTGRES_PASSWORD="$(APP_STUDIO_POSTGRES_PASSWORD)" \
			-e POSTGRES_DB="$(APP_STUDIO_POSTGRES_DB)" \
			-p 127.0.0.1:$(APP_STUDIO_POSTGRES_PORT):5432 \
			-v "$(abspath $(APP_STUDIO_POSTGRES_DATA_DIR)):/var/lib/postgresql/data" \
			"$(APP_STUDIO_POSTGRES_IMAGE)" >/dev/null; \
	fi; \
	echo "Waiting for App Studio Postgres..."; \
	for _ in $$(seq 1 30); do \
		if docker exec "$(APP_STUDIO_POSTGRES_CONTAINER)" pg_isready -U "$(APP_STUDIO_POSTGRES_USER)" -d "$(APP_STUDIO_POSTGRES_DB)" >/dev/null 2>&1; then \
			echo "  database: $(APP_STUDIO_DEV_DATABASE_URL)"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "ERROR: App Studio Postgres did not become ready"; \
	docker logs "$(APP_STUDIO_POSTGRES_CONTAINER)" --tail=50; \
	exit 1

app-studio-db-down: ## Stop and remove the local App Studio Postgres container (data remains in APP_STUDIO_POSTGRES_DATA_DIR)
	@if docker ps -a --format '{{.Names}}' | grep -qx "$(APP_STUDIO_POSTGRES_CONTAINER)"; then \
		docker rm -f "$(APP_STUDIO_POSTGRES_CONTAINER)" >/dev/null; \
		echo "Removed App Studio Postgres container ($(APP_STUDIO_POSTGRES_CONTAINER)); data remains in $(APP_STUDIO_POSTGRES_DATA_DIR)"; \
	else \
		echo "App Studio Postgres container not found ($(APP_STUDIO_POSTGRES_CONTAINER))"; \
	fi

run-provider-app-studio: build-app-studio-provider app-studio-db-up app-studio-preview-bridge-dev-key ## Run the App Studio provider (requires: make run-hub-embedded-static + make install-provider-app-studio)
	@echo "Starting App Studio provider on :$(APP_STUDIO_PORT)"
	@echo "  hub:   $(APP_STUDIO_HUB_URL)"
	@echo "  token: $(APP_STUDIO_TOKEN)"
	@# Auto-source providers/app-studio/.env (gitignored) so local store/LLM
	@# overrides reach Tilt and make without a manual export. See .env.example.
	@# The kubeconfig path is pinned (not picked from existing files): the
	@# controller manager watches the APIExportEndpointSlice in the PROVIDER
	@# workspace, so only the workspace-scoped kubeconfig init writes works —
	@# an admin /clusters/root kubeconfig makes the reconcilers watch an empty
	@# workspace and silently never engage. The retry loop tolerates the file
	@# being absent until init writes it.
	@# A new dev bundle must change the heartbeat version to refresh the hub SRI pin.
	set -a; [ -f providers/app-studio/.env ] && . ./providers/app-studio/.env || true; set +a; \
	RAILGRID_PROVIDER_VERSION="$${RAILGRID_PROVIDER_VERSION:-dev-$$(sha256sum providers/app-studio/portal/dist/main.js | cut -c1-16)}"; export RAILGRID_PROVIDER_VERSION; \
	RAILGRID_ACTIONS_EXTERNAL_URL="$${RAILGRID_ACTIONS_EXTERNAL_URL:-$(RAILGRID_ACTIONS_EXTERNAL_URL)}"; \
	RAILGRID_HUB_PUBLIC_URL="$${RAILGRID_HUB_PUBLIC_URL:-$(APP_STUDIO_HUB_PUBLIC_URL)}"; \
	APP_STUDIO_DATABASE_URL="$${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DATABASE_URL)}"; \
	APP_STUDIO_IN_MEMORY_MESSAGE_STORE="$${APP_STUDIO_IN_MEMORY_MESSAGE_STORE:-$(APP_STUDIO_IN_MEMORY_MESSAGE_STORE)}"; \
	APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY="$$(cat "$(APP_STUDIO_PREVIEW_BRIDGE_DEV_PRIVATE_KEY)")"; \
	APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID="$$(cat "$(APP_STUDIO_PREVIEW_BRIDGE_DEV_KEY_ID)")"; \
	if [ "$${APP_STUDIO_IN_MEMORY_MESSAGE_STORE:-}" = "true" ]; then \
		echo "  store: in-memory (non-durable)"; \
		APP_STUDIO_DATABASE_URL= \
		PORT=$(APP_STUDIO_PORT) \
		RAILGRID_HUB_URL=$(APP_STUDIO_HUB_URL) \
		RAILGRID_HUB_PUBLIC_URL="$${RAILGRID_HUB_PUBLIC_URL}" \
		RAILGRID_ACTIONS_EXTERNAL_URL="$${RAILGRID_ACTIONS_EXTERNAL_URL}" \
		RAILGRID_HUB_INSECURE=true \
		RAILGRID_PROVIDER_NAME=app-studio \
		RAILGRID_CATALOGENTRY_FILE=$(CURDIR)/providers/app-studio/manifest.yaml \
		RAILGRID_PROVIDER_KUBECONFIG=$${RAILGRID_PROVIDER_KUBECONFIG:-$(APP_STUDIO_PROVIDER_KUBECONFIG)} \
		APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true \
		APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY=true \
		APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY=true \
		APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY="$${APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY}" \
		APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID="$${APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID}" \
			$(BINDIR)/app-studio-provider; \
	else \
		echo "  store: $${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DEV_DATABASE_URL)}"; \
		PORT=$(APP_STUDIO_PORT) \
		RAILGRID_HUB_URL=$(APP_STUDIO_HUB_URL) \
		RAILGRID_HUB_PUBLIC_URL="$${RAILGRID_HUB_PUBLIC_URL}" \
		RAILGRID_ACTIONS_EXTERNAL_URL="$${RAILGRID_ACTIONS_EXTERNAL_URL}" \
		RAILGRID_HUB_INSECURE=true \
		RAILGRID_PROVIDER_NAME=app-studio \
		RAILGRID_CATALOGENTRY_FILE=$(CURDIR)/providers/app-studio/manifest.yaml \
		RAILGRID_PROVIDER_KUBECONFIG=$${RAILGRID_PROVIDER_KUBECONFIG:-$(APP_STUDIO_PROVIDER_KUBECONFIG)} \
		APP_STUDIO_DATABASE_URL="$${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DEV_DATABASE_URL)}" \
		APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY=true \
		APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY=true \
		APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY="$${APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY}" \
		APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID="$${APP_STUDIO_PREVIEW_BRIDGE_SIGNING_KEY_ID}" \
			$(BINDIR)/app-studio-provider; \
	fi

## Apply the App Studio CatalogEntry into root:railgrid:providers. Idempotent.
install-provider-app-studio: ## Apply App Studio Provider + CatalogEntry into root:railgrid:providers
	@test -f $(APP_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(APP_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(APP_STUDIO_PROVIDER_MANIFEST) -f $(APP_STUDIO_MANIFEST)

## Create App Studio's APIExport (+ schemas + endpoint slice + bind grant) in
## its provider workspace so tenants can Enable it. Reads the provider-token the
## Provider controller minted on register, writes a dev provider kubeconfig, and
## runs the provider's `init` (sdkinstall.Bootstrap) with the shipped schemas.
## RAILGRID_CATALOGENTRY_FILE is intentionally unset — the dev install target
## already applied the CatalogEntry to system:providers. Idempotent.
init-provider-app-studio: build-app-studio-provider ## Bootstrap App Studio APIExport + write dev provider kubeconfig
	@test -f $(APP_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(APP_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Reading provider-token from $(APP_STUDIO_WORKSPACE_PATH) and writing $(APP_STUDIO_PROVIDER_KUBECONFIG)"
	@TOKEN=$$(kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/$(APP_STUDIO_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: railgrid\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: railgrid\n  context:\n    cluster: railgrid\n    user: railgrid\ncurrent-context: railgrid\nusers:\n- name: railgrid\n  user:\n    token: %s\n' \
		"$(APP_STUDIO_KCP_SERVER)/clusters/$(APP_STUDIO_WORKSPACE_PATH)" "$$TOKEN" \
		> $(APP_STUDIO_PROVIDER_KUBECONFIG)
	@echo "Running app-studio-provider init (creates APIExport + schemas + endpoint slice + bind grant)"
	@# app-studio claims no first-party resources: its reconcilers act in each
	@# tenant workspace through hub-minted scoped identities (tenant-workspace
	@# RBAC through the workspace's own bindings), which is what keeps a
	@# workspace free to bind an org-owned infrastructure or code provider.
	RAILGRID_PROVIDER_KUBECONFIG=$(APP_STUDIO_PROVIDER_KUBECONFIG) \
	APP_STUDIO_WORKSPACE_PATH=$(APP_STUDIO_WORKSPACE_PATH) \
	RAILGRID_KCP_DIR=$(APP_STUDIO_KCP_DIR) \
	RAILGRID_DATAPLANE_URL=http://localhost:$(APP_STUDIO_PORT) \
		$(BINDIR)/app-studio-provider init

## Delete the App Studio CatalogEntry. Useful while iterating on the chart.
uninstall-provider-app-studio: ## Delete App Studio CatalogEntry
	@test -f $(APP_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(APP_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	-kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(APP_STUDIO_MANIFEST) -f $(APP_STUDIO_PROVIDER_MANIFEST)

## --- agents provider dev targets (mirror app-studio) ---

## Start/reuse a local Postgres container for the agents durable store. Skips
## when AGENTS_IN_MEMORY_STORE=true or AGENTS_DATABASE_URL points elsewhere.
agents-db-up:
	@set -a; [ -f providers/agents/.env ] && . ./providers/agents/.env || true; set +a; \
	if [ "$${AGENTS_IN_MEMORY_STORE:-$(AGENTS_IN_MEMORY_STORE)}" = "true" ]; then \
		echo "agents: in-memory store — skipping Postgres"; exit 0; fi; \
	if [ -n "$${AGENTS_DATABASE_URL:-}" ]; then \
		echo "agents: external AGENTS_DATABASE_URL set — skipping dev Postgres"; exit 0; fi; \
	if docker ps --format '{{.Names}}' | grep -q '^$(AGENTS_POSTGRES_CONTAINER)$$'; then \
		echo "agents postgres already running on :$(AGENTS_POSTGRES_PORT)"; exit 0; fi; \
	if docker ps -a --format '{{.Names}}' | grep -q '^$(AGENTS_POSTGRES_CONTAINER)$$'; then \
		docker start $(AGENTS_POSTGRES_CONTAINER); exit 0; fi; \
	mkdir -p $(AGENTS_POSTGRES_DATA_DIR); \
	docker run -d --name $(AGENTS_POSTGRES_CONTAINER) \
		-e POSTGRES_USER=agents -e POSTGRES_PASSWORD=agents -e POSTGRES_DB=agents \
		-p $(AGENTS_POSTGRES_PORT):5432 \
		-v "$(abspath $(AGENTS_POSTGRES_DATA_DIR)):/var/lib/postgresql/data" \
		$(AGENTS_POSTGRES_IMAGE)

agents-db-down: ## Stop and remove the agents dev Postgres container
	-docker rm -f $(AGENTS_POSTGRES_CONTAINER)

run-provider-agents: build-agents-provider agents-db-up ## Run the agents provider (requires: make run-hub-embedded-static + make install-provider-agents + make init-provider-agents)
	@echo "Starting agents provider on :$(AGENTS_PORT)"
	@echo "  hub:   $(AGENTS_HUB_URL)"
	@# Auto-source providers/agents/.env (gitignored) for local overrides.
	set -a; [ -f providers/agents/.env ] && . ./providers/agents/.env || true; set +a; \
	AGENTS_IN_MEMORY_STORE="$${AGENTS_IN_MEMORY_STORE:-$(AGENTS_IN_MEMORY_STORE)}"; \
	if [ "$$AGENTS_IN_MEMORY_STORE" = "true" ]; then \
		echo "  store: in-memory (non-durable)"; AGENTS_DATABASE_URL=; \
	else \
		AGENTS_DATABASE_URL="$${AGENTS_DATABASE_URL:-$(AGENTS_DEV_DATABASE_URL)}"; \
		echo "  store: $$AGENTS_DATABASE_URL"; \
	fi; \
	PORT=$(AGENTS_PORT) \
	RAILGRID_HUB_URL=$(AGENTS_HUB_URL) \
	RAILGRID_HUB_INSECURE=true \
	RAILGRID_PROVIDER_NAME=agents \
	RAILGRID_CATALOGENTRY_FILE=$(CURDIR)/providers/agents/manifest.yaml \
	RAILGRID_PROVIDER_KUBECONFIG=$${RAILGRID_PROVIDER_KUBECONFIG:-$$( for f in "$(AGENTS_PROVIDER_KUBECONFIG)" "$(AGENTS_KCP_KUBECONFIG)" "$(CURDIR)/tilt-frontproxy.kubeconfig"; do [ -f "$$f" ] && echo "$$f" && break; done )} \
	AGENTS_DATABASE_URL="$$AGENTS_DATABASE_URL" \
	AGENTS_IN_MEMORY_STORE="$$AGENTS_IN_MEMORY_STORE" \
		$(BINDIR)/agents-provider

install-provider-agents: ## Apply agents Provider + CatalogEntry into root:railgrid:providers
	@test -f $(AGENTS_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(AGENTS_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(AGENTS_KCP_KUBECONFIG) \
		--server=$(AGENTS_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(AGENTS_PROVIDER_MANIFEST) -f $(AGENTS_MANIFEST)

init-provider-agents: build-agents-provider ## Bootstrap agents APIExport + write dev provider kubeconfig
	@test -f $(AGENTS_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(AGENTS_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Reading provider-token from $(AGENTS_WORKSPACE_PATH) and writing $(AGENTS_PROVIDER_KUBECONFIG)"
	@TOKEN=$$(kubectl --kubeconfig=$(AGENTS_KCP_KUBECONFIG) \
		--server=$(AGENTS_KCP_SERVER)/clusters/$(AGENTS_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: railgrid\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: railgrid\n  context:\n    cluster: railgrid\n    user: railgrid\ncurrent-context: railgrid\nusers:\n- name: railgrid\n  user:\n    token: %s\n' \
		"$(AGENTS_KCP_SERVER)/clusters/$(AGENTS_WORKSPACE_PATH)" "$$TOKEN" \
		> $(AGENTS_PROVIDER_KUBECONFIG)
	@echo "Running agents-provider init (creates APIExport + schemas + endpoint slice + bind grant)"
	RAILGRID_PROVIDER_KUBECONFIG=$(AGENTS_PROVIDER_KUBECONFIG) \
	AGENTS_WORKSPACE_PATH=$(AGENTS_WORKSPACE_PATH) \
	RAILGRID_KCP_DIR=$(AGENTS_KCP_DIR) \
	RAILGRID_DATAPLANE_URL=http://localhost:$(AGENTS_PORT) \
		$(BINDIR)/agents-provider init

uninstall-provider-agents: ## Delete the agents CatalogEntry + Provider
	@test -f $(AGENTS_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(AGENTS_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	-kubectl --kubeconfig=$(AGENTS_KCP_KUBECONFIG) \
		--server=$(AGENTS_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(AGENTS_MANIFEST) -f $(AGENTS_PROVIDER_MANIFEST)

# --- Dev agent image (template-native development mode) ---
# The static control binary an init container injects into any dev-mode
# component (docs/app-studio-template-sandboxes.md §2). Scratch image with a
# stdlib-only module; application dependencies are installed by each component
# from its declared package manifest, not projected by this image.
DEV_AGENT_DIR ?= providers/infrastructure/dev-agent
DEV_AGENT_IMAGE ?= ghcr.io/railgrid/railgrid-dev-agent:latest
DEV_AGENT_PLATFORM ?= linux/$(ARCH)
UNIVERSAL_DEV_IMAGE ?= ghcr.io/railgrid/railgrid-universal-dev:latest
UNIVERSAL_DEV_BASE_IMAGE ?= docker.io/library/node:22-bookworm

docker-build-dev-agent: ## Build the railgrid-dev-agent injector image used by dev-mode components
	docker build -f $(DEV_AGENT_DIR)/Dockerfile \
		--platform $(DEV_AGENT_PLATFORM) \
		--provenance=false \
		-t $(DEV_AGENT_IMAGE) $(DEV_AGENT_DIR)

load-dev-agent-image: docker-build-dev-agent ## Load the dev agent image into the local kind runtime cluster
	@echo ">>> loading $(DEV_AGENT_IMAGE) into kind cluster $(KRO_KIND_NAME)"
	kind load docker-image $(DEV_AGENT_IMAGE) --name $(KRO_KIND_NAME)

docker-build-universal-dev-image: ## Build the universal coding image; pin UNIVERSAL_DEV_BASE_IMAGE to a digest in production
	docker build -f $(DEV_AGENT_DIR)/Dockerfile.universal \
		--platform $(DEV_AGENT_PLATFORM) \
		--provenance=false \
		--build-arg BASE_IMAGE=$(UNIVERSAL_DEV_BASE_IMAGE) \
		-t $(UNIVERSAL_DEV_IMAGE) $(DEV_AGENT_DIR)

load-universal-dev-image: docker-build-universal-dev-image ## Load the universal coding image into the local kind runtime cluster
	@echo ">>> loading $(UNIVERSAL_DEV_IMAGE) into kind cluster $(KRO_KIND_NAME)"
	kind load docker-image $(UNIVERSAL_DEV_IMAGE) --name $(KRO_KIND_NAME)

## Apply the infrastructure CatalogEntry into root:railgrid:providers. Idempotent.
## Requires the hub to be running so the admin kubeconfig exists.
install-provider-infrastructure: ## Apply infrastructure Provider + CatalogEntry into root:railgrid:providers
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		apply --validate=false -f $(KROMC_PROVIDER_MANIFEST) -f $(KROMC_MANIFEST)

## Delete the infrastructure CatalogEntry + Provider (Provider delete triggers
## full teardown of the sub-workspace via the controller's finalizer).
uninstall-provider-infrastructure: ## Delete infrastructure CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(KROMC_MANIFEST) -f $(KROMC_PROVIDER_MANIFEST)

## One-shot bootstrap for the infrastructure provider's workspace.
## Runs as the provider ServiceAccount: it reads the provider-token Secret the
## Provider controller minted in the workspace, writes it to
## INFRASTRUCTURE_PROVIDER_KUBECONFIG and hands that to init as its admin
## kubeconfig — the same identity the chart's init container has. That identity
## is load-bearing: the PermissionClaimPolicy reserves infrastructure.railgrid.ai
## for the platform's providers, and kcp's APIExport admission refuses an export
## (or an entry upsert) of that group from any other user, the hub admin
## included. init then installs CRDs, registers APIExport schemas, applies the
## Templates CachedResource, mints the low-privilege runtime ServiceAccount +
## token, and writes the runtime kubeconfig that run-provider-infrastructure
## passes to serve as RAILGRID_PROVIDER_KUBECONFIG.
##
## When KRO_KUBECONFIG is set, also seeds the kro cluster with a
## kro.run/cluster=true Secret pointing at this workspace's VW.
INFRASTRUCTURE_WORKSPACE_PATH ?= root:railgrid:providers:infrastructure
INFRASTRUCTURE_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/infrastructure-runtime.kubeconfig
INFRASTRUCTURE_PROVIDER_KUBECONFIG ?= $(KCP_DATA_DIR)/infrastructure-provider.kubeconfig
init-provider-infrastructure: build-infrastructure-provider ## Bootstrap infrastructure provider workspace (CRDs, APIExport, SA, kubeconfig)
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Reading provider-token from $(INFRASTRUCTURE_WORKSPACE_PATH) and writing $(INFRASTRUCTURE_PROVIDER_KUBECONFIG)"
	@TOKEN=$$(kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/$(INFRASTRUCTURE_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: railgrid\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: railgrid\n  context:\n    cluster: railgrid\n    user: railgrid\ncurrent-context: railgrid\nusers:\n- name: railgrid\n  user:\n    token: %s\n' \
		"$(KROMC_KCP_SERVER)/clusters/$(INFRASTRUCTURE_WORKSPACE_PATH)" "$$TOKEN" \
		> $(INFRASTRUCTURE_PROVIDER_KUBECONFIG)
	@echo "Bootstrapping infrastructure provider workspace $(INFRASTRUCTURE_WORKSPACE_PATH)"
	@echo "  provider: $(INFRASTRUCTURE_PROVIDER_KUBECONFIG)"
	@echo "  runtime:  $(INFRASTRUCTURE_RUNTIME_KUBECONFIG)"
	INFRASTRUCTURE_ADMIN_KUBECONFIG=$(INFRASTRUCTURE_PROVIDER_KUBECONFIG) \
	RAILGRID_DATAPLANE_URL=http://localhost:$(KROMC_PORT) \
	INFRASTRUCTURE_WORKSPACE_PATH=$(INFRASTRUCTURE_WORKSPACE_PATH) \
	RAILGRID_KCP_DIR=$(CURDIR)/providers/infrastructure/deploy/chart/files \
	INFRASTRUCTURE_KUBECONFIG=$(INFRASTRUCTURE_RUNTIME_KUBECONFIG) \
	KRO_KUBECONFIG=$${KRO_KUBECONFIG:-$$( [ -f "$(KRO_KIND_KUBECONFIG)" ] && echo "$(KRO_KIND_KUBECONFIG)" )} \
		$(BINDIR)/infrastructure-provider init

# ── code provider (git repository management) ──────────────────────────────
# Local-dev flow mirrors the infrastructure provider:
#   Terminal 1: make run-hub-embedded-static          # hub + embedded kcp
#   Terminal 2: make install-provider-code            # admin: register entry
#   Terminal 3: make run-provider-code                # tenant: run binary
CODE_PORT ?= 8083
CODE_MANIFEST ?= providers/code/manifest.yaml
CODE_PROVIDER_MANIFEST ?= providers/code/provider.yaml
CODE_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/code-runtime.kubeconfig

.PHONY: serve-provider-code
run-provider-code: build-code-provider ## Build and run the code provider
	@$(MAKE) serve-provider-code

# Tilt already built and initialized the exact binary in its update phase.
# Keep serving separate so startup cannot rebuild past the initialized version.
serve-provider-code: ## Run the already-built code provider
	@echo "Starting code provider on :$(CODE_PORT) (hub $(KROMC_HUB_URL))"
	@# Auto-source providers/code/.env (gitignored) so GitHub OAuth + other dev
	@# env reach the provider without a manual export. See .env.example.
	set -a; [ -f providers/code/.env ] && . ./providers/code/.env || true; set +a; \
	PORT=$(CODE_PORT) \
	RAILGRID_HUB_URL=$(KROMC_HUB_URL) \
	RAILGRID_HUB_INSECURE=true \
	RAILGRID_PROVIDER_NAME=code \
	RAILGRID_CATALOGENTRY_FILE=$(CURDIR)/providers/code/manifest.yaml \
	CODE_COMMIT_BUNDLE_DIR=$${CODE_COMMIT_BUNDLE_DIR:-$(KCP_DATA_DIR)/code-commit-bundles} \
	RAILGRID_PROVIDER_KUBECONFIG=$${RAILGRID_PROVIDER_KUBECONFIG:-$$( [ -f "$(CODE_RUNTIME_KUBECONFIG)" ] && echo "$(CODE_RUNTIME_KUBECONFIG)" )} \
	GITHUB_OAUTH_CLIENT_ID=$${GITHUB_OAUTH_CLIENT_ID:-} \
	GITHUB_OAUTH_CLIENT_SECRET=$${GITHUB_OAUTH_CLIENT_SECRET:-} \
	GITHUB_OAUTH_REDIRECT_URL=$${GITHUB_OAUTH_REDIRECT_URL:-http://localhost:$(CODE_PORT)/oauth/github/callback} \
	GITHUB_OAUTH_PORTAL_ORIGIN=$${GITHUB_OAUTH_PORTAL_ORIGIN:-$(KROMC_HUB_URL)} \
		$(BINDIR)/code-provider serve

install-provider-code: ## Apply the code Provider + CatalogEntry into root:railgrid:providers
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(CODE_PROVIDER_MANIFEST) -f $(CODE_MANIFEST)

uninstall-provider-code: ## Delete the code CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:railgrid:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(CODE_MANIFEST) -f $(CODE_PROVIDER_MANIFEST)

CODE_WORKSPACE_PATH ?= root:railgrid:providers:code
## Dev bootstrap for the code provider. Reads the provider-token Secret the
## Provider controller minted in the code workspace and writes a runtime
## kubeconfig carrying it, so init acts as the provider ServiceAccount
## (system:serviceaccount:default:provider). That identity is load-bearing: the
## PermissionClaimPolicy (config/kcp/permissionclaimpolicy.yaml) reserves
## code.railgrid.ai for the platform's providers, and kcp's APIExport admission
## refuses an export of that group from any other user — the admin included.
## run-provider-code reads the same file via RAILGRID_PROVIDER_KUBECONFIG.
## Order: install-provider-code (creates the workspace) → init-provider-code →
## run-provider-code. Re-runnable. The Tiltfile.cluster flow reuses this target
## verbatim, overriding KROMC_KCP_KUBECONFIG / KROMC_KCP_SERVER.
init-provider-code: build-code-provider ## Write the dev provider kubeconfig + bootstrap the code APIExport
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Reading provider-token from $(CODE_WORKSPACE_PATH) and writing $(CODE_RUNTIME_KUBECONFIG)"
	@TOKEN=$$(kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/$(CODE_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: railgrid\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: railgrid\n  context:\n    cluster: railgrid\n    user: railgrid\ncurrent-context: railgrid\nusers:\n- name: railgrid\n  user:\n    token: %s\n' \
		"$(KROMC_KCP_SERVER)/clusters/$(CODE_WORKSPACE_PATH)" "$$TOKEN" \
		> $(CODE_RUNTIME_KUBECONFIG)
	@echo "Running code-provider init (creates APIExport + schemas + endpoint slice + bind grant)"
	RAILGRID_PROVIDER_KUBECONFIG=$(CODE_RUNTIME_KUBECONFIG) \
	CODE_WORKSPACE_PATH=$(CODE_WORKSPACE_PATH) \
	RAILGRID_KCP_DIR=$(CURDIR)/providers/code/deploy/chart/files \
	RAILGRID_DATAPLANE_URL=http://localhost:$(CODE_PORT) \
		$(BINDIR)/code-provider init

# --- Provider Databricks (local dev) ---

# --- Experimental: run the infrastructure provider as a POD (init-container
#     bootstrap) instead of a host binary. Exercises the full hub-minted
#     flow: CatalogEntry -> hub mints + delivers railgrid-provider-kubeconfig
#     (HostSecretWriter) -> init container bootstraps with it -> serve runs.
#     Reuses the railgrid-kro kind cluster as the host cluster. Requires the hub
#     to run with --kubeconfig=$(KRO_KIND_KUBECONFIG) and
#     --hub-internal-url=$(HUB_INTERNAL_URL) (the Tiltfile sets
#     both). Apply the CatalogEntry first: make install-provider-infrastructure
INFRASTRUCTURE_NAMESPACE ?= infrastructure
INFRASTRUCTURE_IMAGE ?= railgrid-infrastructure-provider:dev
INFRASTRUCTURE_CHART ?= providers/infrastructure/deploy/chart
# Address provider pods in the kind cluster use to reach the hub front-proxy
# (browsers use https://console.127.0.0.1.sslip.io:9443; host.docker.internal resolves to the
# host from inside kind on Docker Desktop / Colima / OrbStack).
HUB_INTERNAL_URL ?= https://host.docker.internal:9443
helm-deploy-provider-infrastructure: ## (experimental) Build+load image, helm install the provider as a pod into railgrid-kro (hub-minted bootstrap)
	@command -v kind >/dev/null || { echo "kind not found; brew install kind"; exit 1; }
	@test -f $(KRO_KIND_KUBECONFIG) || { echo "railgrid-kro cluster missing; run 'make dev-kro-up' first"; exit 1; }
	@echo ">>> building $(INFRASTRUCTURE_IMAGE)"
	docker build -t $(INFRASTRUCTURE_IMAGE) -f providers/infrastructure/Dockerfile .
	@echo ">>> loading image into kind cluster $(KRO_KIND_NAME)"
	kind load docker-image $(INFRASTRUCTURE_IMAGE) --name $(KRO_KIND_NAME)
	@echo ">>> ensuring namespace + heartbeat token Secret in $(INFRASTRUCTURE_NAMESPACE)"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl create namespace $(INFRASTRUCTURE_NAMESPACE) \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(INFRASTRUCTURE_NAMESPACE) create secret generic railgrid-infrastructure-hub-token \
		--from-literal=token=$(STATIC_AUTH_TOKEN) \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	@echo ">>> helm install (bootstrap.enabled=true, kubeconfigSource=hubMinted)"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) helm upgrade --install infrastructure $(INFRASTRUCTURE_CHART) \
		--namespace $(INFRASTRUCTURE_NAMESPACE) \
		--set image.repository=railgrid-infrastructure-provider \
		--set image.tag=dev \
		--set image.pullPolicy=Never \
		--set replicaCount=2 \
		--set bootstrap.enabled=true \
		--set hub.url=$(HUB_INTERNAL_URL) \
		--set hub.tokenSecretRef.name=railgrid-infrastructure-hub-token \
		--set hub.insecure=true \
		--set catalogEntry.enabled=false
	@echo ">>> deployed. The pod stays in ContainerCreating until the hub delivers"
	@echo "    the railgrid-provider-kubeconfig Secret (apply the CatalogEntry first:"
	@echo "    make install-provider-infrastructure). Watch:"
	@echo "    KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(INFRASTRUCTURE_NAMESPACE) get pods -w"

helm-undeploy-provider-infrastructure: ## (experimental) helm uninstall the infrastructure provider pod
	-KUBECONFIG=$(KRO_KIND_KUBECONFIG) helm uninstall infrastructure -n $(INFRASTRUCTURE_NAMESPACE)

# --- Management kro cluster (backend for the infrastructure provider) ---
# Brings up a dedicated kind cluster running UPSTREAM kro
# (oci://registry.k8s.io/kro/charts/kro), single-cluster: with the
# flattened Instance kind, tenants author instances in kcp and the
# infrastructure provider's instance controller materializes the
# per-template kro CRs on this cluster — kro never talks to kcp, so
# the retired railgrid/kro-multicluster fork is no longer used.
#
# The railgrid infrastructure provider points at this cluster via
# KRO_KUBECONFIG so provisioning materializes real Deployments /
# Services.
#
KRO_KIND_NAME ?= railgrid-kro
KRO_KIND_KUBECONFIG ?= $(CURDIR)/.railgrid-kro.kubeconfig
KRO_CHART ?= oci://registry.k8s.io/kro/charts/kro
KRO_CHART_VERSION ?= 0.9.3
KRO_NAMESPACE ?= kro-system
KRO_SEED_DIR ?= providers/infrastructure/examples/rgds

# --- Local application-preview Gateway --------------------------------------
# The base Tiltfile uses the railgrid-kro kind cluster for application-template
# runtimes. Keep the Envoy Gateway install separate from the kro release so it
# can be reconciled independently and so `dev-kro-down` remains the one command
# that owns cluster teardown.
ENVOY_GATEWAY_CHART ?= oci://docker.io/envoyproxy/gateway-helm
# Gateway API CRDs are pinned to v1.5.1 below. Envoy Gateway v1.8.3 is the
# matching upstream release line for the Kubernetes 1.33 kind cluster.
ENVOY_GATEWAY_VERSION ?= v1.8.3
ENVOY_GATEWAY_RELEASE ?= envoy-gateway
ENVOY_GATEWAY_CRDS_CHART ?= oci://docker.io/envoyproxy/gateway-crds-helm
PREVIEW_GATEWAY_KUBECONFIG ?= $(KRO_KIND_KUBECONFIG)
PREVIEW_GATEWAY_CONTEXT ?= kind-$(KRO_KIND_NAME)
PREVIEW_GATEWAY_NAMESPACE ?= envoy-gateway-system
PREVIEW_GATEWAY_NAME ?= app-studio-preview
PREVIEW_GATEWAY_CLASS ?= eg
PREVIEW_GATEWAY_CONTROLLER ?= gateway.envoyproxy.io/gatewayclass-controller
PREVIEW_GATEWAY_PROXY_CONFIG ?= app-studio-preview-proxy
PREVIEW_GATEWAY_HOSTNAME ?= *.apps.127.0.0.1.sslip.io
PREVIEW_GATEWAY_LISTENER_NAME ?= https
PREVIEW_GATEWAY_TLS_SECRET ?= app-studio-preview-tls
PREVIEW_GATEWAY_PORT ?= 10443
PREVIEW_GATEWAY_SERVICE_PORT ?= $(PREVIEW_GATEWAY_PORT)
PREVIEW_GATEWAY_STATE_DIR ?= $(KCP_DATA_DIR)/tilt-preview-gateway-tls
PREVIEW_GATEWAY_TIMEOUT ?= 5m
PREVIEW_GATEWAY_UNINSTALL ?= false
PREVIEW_GATEWAY_SCRIPT ?= hack/scripts/configure-tilt-preview-gateway.sh

.PHONY: dev-preview-gateway-up dev-preview-gateway-down

dev-preview-gateway-up: ## Install Envoy Gateway + local HTTPS parent for app previews
	@test -f "$(PREVIEW_GATEWAY_KUBECONFIG)" || { \
		echo "no kubeconfig at $(PREVIEW_GATEWAY_KUBECONFIG); run 'make dev-kro-up' first"; \
		exit 1; \
	}
	@test -x "$(PREVIEW_GATEWAY_SCRIPT)" || { \
		echo "preview Gateway helper is not executable: $(PREVIEW_GATEWAY_SCRIPT)"; \
		exit 1; \
	}
	KUBECONFIG="$(PREVIEW_GATEWAY_KUBECONFIG)" \
	PREVIEW_GATEWAY_CONTEXT="$(PREVIEW_GATEWAY_CONTEXT)" \
	ENVOY_GATEWAY_CHART="$(ENVOY_GATEWAY_CHART)" \
	ENVOY_GATEWAY_CRDS_CHART="$(ENVOY_GATEWAY_CRDS_CHART)" \
	ENVOY_GATEWAY_VERSION="$(ENVOY_GATEWAY_VERSION)" \
	ENVOY_GATEWAY_RELEASE="$(ENVOY_GATEWAY_RELEASE)" \
	PREVIEW_GATEWAY_NAMESPACE="$(PREVIEW_GATEWAY_NAMESPACE)" \
	PREVIEW_GATEWAY_NAME="$(PREVIEW_GATEWAY_NAME)" \
	PREVIEW_GATEWAY_CLASS="$(PREVIEW_GATEWAY_CLASS)" \
	PREVIEW_GATEWAY_CONTROLLER="$(PREVIEW_GATEWAY_CONTROLLER)" \
	PREVIEW_GATEWAY_PROXY_CONFIG="$(PREVIEW_GATEWAY_PROXY_CONFIG)" \
	PREVIEW_GATEWAY_HOSTNAME="$(PREVIEW_GATEWAY_HOSTNAME)" \
	PREVIEW_GATEWAY_LISTENER_NAME="$(PREVIEW_GATEWAY_LISTENER_NAME)" \
	PREVIEW_GATEWAY_TLS_SECRET="$(PREVIEW_GATEWAY_TLS_SECRET)" \
	PREVIEW_GATEWAY_PORT="$(PREVIEW_GATEWAY_PORT)" \
	PREVIEW_GATEWAY_SERVICE_PORT="$(PREVIEW_GATEWAY_SERVICE_PORT)" \
	PREVIEW_GATEWAY_STATE_DIR="$(PREVIEW_GATEWAY_STATE_DIR)" \
	PREVIEW_GATEWAY_TIMEOUT="$(PREVIEW_GATEWAY_TIMEOUT)" \
		"$(PREVIEW_GATEWAY_SCRIPT)" apply

dev-preview-gateway-down: ## Remove the local preview Gateway and Secret (keep controller by default)
	@test -x "$(PREVIEW_GATEWAY_SCRIPT)" || { \
		echo "preview Gateway helper is not executable: $(PREVIEW_GATEWAY_SCRIPT)"; \
		exit 1; \
	}
	KUBECONFIG="$(PREVIEW_GATEWAY_KUBECONFIG)" \
	PREVIEW_GATEWAY_CONTEXT="$(PREVIEW_GATEWAY_CONTEXT)" \
	PREVIEW_GATEWAY_RELEASE="$(ENVOY_GATEWAY_RELEASE)" \
	PREVIEW_GATEWAY_NAMESPACE="$(PREVIEW_GATEWAY_NAMESPACE)" \
	PREVIEW_GATEWAY_NAME="$(PREVIEW_GATEWAY_NAME)" \
	PREVIEW_GATEWAY_PROXY_CONFIG="$(PREVIEW_GATEWAY_PROXY_CONFIG)" \
	PREVIEW_GATEWAY_TLS_SECRET="$(PREVIEW_GATEWAY_TLS_SECRET)" \
	PREVIEW_GATEWAY_STATE_DIR="$(PREVIEW_GATEWAY_STATE_DIR)" \
	PREVIEW_GATEWAY_UNINSTALL="$(PREVIEW_GATEWAY_UNINSTALL)" \
		"$(PREVIEW_GATEWAY_SCRIPT)" cleanup

# --- Infrastructure template e2e (RGD acceptance against a real kro) ---
# A throwaway kind cluster running STANDALONE kro (no kcp) — enough to validate
# that every seeded Template authors a kro graph kro accepts. See
# providers/infrastructure/backend/kro/e2e_test.go.
E2E_KRO_KIND_NAME ?= railgrid-kro-e2e
E2E_KRO_KUBECONFIG ?= $(CURDIR)/.railgrid-kro-e2e.kubeconfig
# Envoy Gateway v1.8.x supports Gateway API v1.5.1 on Kubernetes 1.32–1.35.
GATEWAY_API_VERSION ?= v1.5.1

## Bring up the management kro cluster + install upstream kro. Idempotent:
## re-running just helm-upgrades the chart (with an explicit CRD apply —
## helm never upgrades crds/-dir CRDs).
dev-kro-up: ## Bring up the railgrid-kro kind cluster + install upstream kro
	@command -v kind >/dev/null || { echo "kind not found; install: brew install kind"; exit 1; }
	@command -v helm >/dev/null || { echo "helm not found; install: brew install helm"; exit 1; }
	@if ! kind get clusters | grep -qx "$(KRO_KIND_NAME)"; then \
		echo ">>> creating kind cluster $(KRO_KIND_NAME)"; \
		kind create cluster --name $(KRO_KIND_NAME) --kubeconfig $(KRO_KIND_KUBECONFIG); \
	else \
		echo ">>> kind cluster $(KRO_KIND_NAME) already exists"; \
		kind get kubeconfig --name $(KRO_KIND_NAME) > $(KRO_KIND_KUBECONFIG); \
	fi
	@# Standard Gateway API CRDs (GatewayClass/Gateway/HTTPRoute/ReferenceGrant).
	@# This target is the owner the preview-gateway script relies on: it renders
	@# Envoy's CRD chart with crds.gatewayAPI.enabled=false, so without this
	@# apply a fresh cluster has no gateway.networking.k8s.io kinds and
	@# envoy-gateway crashloops on the ReferenceGrant restmapping.
	@echo ">>> installing Gateway API CRDs ($(GATEWAY_API_VERSION))"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/standard-install.yaml
	@# helm only installs crds/-dir CRDs on FIRST install; apply them
	@# explicitly so chart upgrades (fork → upstream, version bumps) carry
	@# CRD schema changes too. Pull+untar rather than `helm show crds`: some
	@# helm versions print OCI pull chatter on stdout, corrupting the stream.
	@echo ">>> applying kro chart CRDs ($(KRO_CHART_VERSION))"
	@rm -rf .kro-chart-tmp && mkdir -p .kro-chart-tmp
	helm pull $(KRO_CHART) --version $(KRO_CHART_VERSION) --untar --untardir .kro-chart-tmp
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f .kro-chart-tmp/kro/crds/
	@rm -rf .kro-chart-tmp
	@echo ">>> installing upstream kro into $(KRO_NAMESPACE)"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) helm upgrade --install kro $(KRO_CHART) \
		--version $(KRO_CHART_VERSION) \
		--namespace $(KRO_NAMESPACE) --create-namespace \
		--wait --timeout 5m
	@# dev-kro-seed intentionally NOT run here. The legacy RGDs under
	@# providers/infrastructure/examples/rgds/ use group "kro.run" (the
	@# pre-Template prototype) and get watched by kro on every engaged
	@# cluster including the kcp tenant VW — which doesn't expose
	@# kro.run resources, so the watches 403 in a loop. Catalog content
	@# is now driven by Templates seeded by `init-provider-infrastructure`
	@# (see providers/infrastructure/install/templates/). Run dev-kro-seed
	@# by hand only if you need the legacy fixtures for some specific
	@# diagnostic.
	@echo ">>> kro management cluster ready"
	@echo "    kubeconfig: $(KRO_KIND_KUBECONFIG)"
	@echo "    point the provider at it: export KRO_KUBECONFIG=$(KRO_KIND_KUBECONFIG)"

## Apply / re-apply the sample RGDs under $(KRO_SEED_DIR) to the
## management cluster. Useful while iterating on RGD authoring without
## restarting the full stack.
dev-kro-seed: ## Apply seed RGDs (providers/infrastructure/examples/rgds/) into the kro cluster
	@test -f $(KRO_KIND_KUBECONFIG) || { \
		echo "no kubeconfig at $(KRO_KIND_KUBECONFIG); run 'make dev-kro-up' first"; \
		exit 1; \
	}
	@for f in $(KRO_SEED_DIR)/*.yaml; do \
		echo ">>> applying $$f"; \
		KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f $$f; \
	done

## Tear down the management cluster + delete the kubeconfig file.
dev-kro-down: ## Delete the railgrid-kro kind cluster + kubeconfig file
	-kind delete cluster --name $(KRO_KIND_NAME)
	-rm -f $(KRO_KIND_KUBECONFIG)

e2e-infrastructure-up: ## Bring up a throwaway kind cluster with Gateway API CRDs + standalone kro for the template e2e
	@command -v kind >/dev/null || { echo "kind not found; install: brew install kind"; exit 1; }
	@command -v helm >/dev/null || { echo "helm not found; install: brew install helm"; exit 1; }
	@if ! kind get clusters | grep -qx "$(E2E_KRO_KIND_NAME)"; then \
		echo ">>> creating kind cluster $(E2E_KRO_KIND_NAME)"; \
		kind create cluster --name $(E2E_KRO_KIND_NAME) --kubeconfig $(E2E_KRO_KUBECONFIG); \
	else \
		kind get kubeconfig --name $(E2E_KRO_KIND_NAME) > $(E2E_KRO_KUBECONFIG); \
	fi
	@echo ">>> installing Gateway API CRDs ($(GATEWAY_API_VERSION)) — templates declare HTTPRoute/ReferenceGrant"
	KUBECONFIG=$(E2E_KRO_KUBECONFIG) kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/standard-install.yaml
	@echo ">>> installing upstream kro into $(KRO_NAMESPACE)"
	@rm -rf .kro-chart-tmp && mkdir -p .kro-chart-tmp
	helm pull $(KRO_CHART) --version $(KRO_CHART_VERSION) --untar --untardir .kro-chart-tmp
	KUBECONFIG=$(E2E_KRO_KUBECONFIG) kubectl apply -f .kro-chart-tmp/kro/crds/
	@rm -rf .kro-chart-tmp
	KUBECONFIG=$(E2E_KRO_KUBECONFIG) helm upgrade --install kro $(KRO_CHART) \
		--version $(KRO_CHART_VERSION) -n $(KRO_NAMESPACE) --create-namespace \
		--wait --timeout 5m

e2e-infrastructure-down: ## Delete the infra-template e2e kind cluster + kubeconfig
	-kind delete cluster --name $(E2E_KRO_KIND_NAME)
	-rm -f $(E2E_KRO_KUBECONFIG)

e2e-infrastructure-run: ## Run the infra template RGD-acceptance e2e against $(E2E_KRO_KUBECONFIG)
	cd providers/infrastructure && KUBECONFIG=$(E2E_KRO_KUBECONFIG) \
		go test -tags e2e -count=1 -timeout 15m ./backend/kro/ -run TestE2E -v

e2e-infrastructure: e2e-infrastructure-up e2e-infrastructure-run ## Bring up kro + run the infra template e2e (then `make e2e-infrastructure-down`)

# --- In-cluster variants (no separate kind cluster) ---
# Used by Tiltfile.cluster, which already manages a kind cluster for kcp.
# Installing kro INTO that same cluster lets kro reach kcp via in-cluster
# Service DNS (front-proxy-front-proxy.default.svc.cluster.local) — no
# cross-kind networking gymnastics. The infrastructure provider on the
# host still reaches kro via the host-published apiserver of the same cluster.
#
# Args (must be set by caller — Tiltfile.cluster injects these):
#   KRO_TARGET_KUBECONFIG   host-accessible kubeconfig of the target cluster
#                           (e.g. `kind get kubeconfig --name kcp-tilt`)
#   KRO_TARGET_KIND_NAME    kind cluster name; used for the --internal
#                           kubeconfig that kro writes into self-cluster Secret
dev-kro-up-into: ## Install upstream kro into an existing cluster ($KRO_TARGET_KUBECONFIG)
	@command -v helm >/dev/null || { echo "helm not found; install: brew install helm"; exit 1; }
	@test -n "$(KRO_TARGET_KUBECONFIG)" || { echo "KRO_TARGET_KUBECONFIG not set"; exit 1; }
	@test -f "$(KRO_TARGET_KUBECONFIG)" || { echo "no kubeconfig at $(KRO_TARGET_KUBECONFIG)"; exit 1; }
	@# kro runs single-cluster: the infrastructure provider's instance
	@# controller bridges kcp → this cluster, so no kcp kubeconfig, no
	@# multicluster values, no self-member Secret, no hostAliases.
	@# helm only installs crds/-dir CRDs on FIRST install; apply them
	@# explicitly so chart upgrades carry CRD schema changes too.
	@echo ">>> applying kro chart CRDs ($(KRO_CHART_VERSION))"
	helm show crds $(KRO_CHART) --version $(KRO_CHART_VERSION) | \
		KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl apply -f -
	@echo ">>> installing upstream kro into $(KRO_NAMESPACE)"
	KUBECONFIG=$(KRO_TARGET_KUBECONFIG) helm upgrade --install kro $(KRO_CHART) \
		--version $(KRO_CHART_VERSION) \
		--namespace $(KRO_NAMESPACE) --create-namespace \
		--wait --timeout 5m
	@echo ">>> kro ready in $(KRO_NAMESPACE)"
	@echo "    point the provider: export KRO_KUBECONFIG=$(KRO_TARGET_KUBECONFIG)"

dev-kro-down-into: ## Helm-uninstall kro from $KRO_TARGET_KUBECONFIG cluster
	@test -n "$(KRO_TARGET_KUBECONFIG)" || { echo "KRO_TARGET_KUBECONFIG not set"; exit 1; }
	-KUBECONFIG=$(KRO_TARGET_KUBECONFIG) helm uninstall kro -n $(KRO_NAMESPACE)
	-KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl delete namespace $(KRO_NAMESPACE) --wait=false

dev-status: ## Show status of dev services (dex, kcp)
	@source $(SERVICE_HOOKS) && list_services

dev-clean-hooks: ## Clean up stale service hooks
	@source $(SERVICE_HOOKS) && cleanup_stale_hooks
	@echo "Hooks cleaned."

# --- Dev environment quick start ---
# These targets help you get started quickly

help-dev: ## Show development environment options
	@echo ""
	@echo "=== Railgrid Hub Development Modes ==="
	@echo ""
	@echo "TILT (recommended — one command, portal + hub + providers):"
	@echo "  make tilt                       - Hub binary, embedded kcp, host-run providers"
	@echo "  make tilt-cluster               - Multi-shard kcp in kind, hub deployed in-cluster"
	@echo "                                    Run one or the other, not both."
	@echo ""
	@echo "STANDALONE (no external dependencies):"
	@echo "  make run-hub-standalone         - Embedded kcp + static token"
	@echo "                                    Just run this and use: make dev-login-static"
	@echo "  make run-hub-embedded-static    - Embedded kcp + static token + portal dev proxy"
	@echo ""
	@echo "WITH DEX (OIDC authentication):"
	@echo "  Terminal 1: make run-dex"
	@echo "  Terminal 2: make run-hub-embedded          - Embedded kcp + OIDC"
	@echo "              make dev-login                 - Login via browser"
	@echo ""
	@echo "WITH EXTERNAL KCP:"
	@echo "  Terminal 1: make dev-run-kcp"
	@echo "  Terminal 2: make run-dex             - (optional, for OIDC)"
	@echo "  Terminal 3: make run-hub             - External kcp + OIDC"
	@echo "          or: make run-hub-static      - External kcp + static token"
	@echo ""
	@echo "PROVIDER QUICKSTART (after the hub is running):"
	@echo "  Terminal A: make install-provider-quickstart   - Apply CatalogEntry"
	@echo "  Terminal B: make run-provider-quickstart       - Run the provider"
	@echo "  Then open $(DEV_HUB_URL)/ui/providers (Enable the provider)"
	@echo ""
	@echo "APP STUDIO PROVIDER (after the hub is running):"
	@echo "  Terminal A: make install-provider-app-studio   - Apply CatalogEntry"
	@echo "  Terminal B: make run-provider-app-studio       - Run the provider"
	@echo "  Then open $(DEV_HUB_URL)/ui/providers (Enable the provider)"
	@echo ""
	@echo "ENVIRONMENT VARIABLES:"
	@echo "  STATIC_AUTH_TOKEN  - Token for static auth (default: dev-token)"
	@echo "  KCP_DATA_DIR       - Directory for kcp data (default: .kcp)"
	@echo "  QUICKSTART_PORT    - Port the quickstart provider listens on (default: 8081)"
	@echo "  QUICKSTART_HUB_URL - Hub URL the provider heartbeats to (default: $(DEV_HUB_URL))"
	@echo "  APP_STUDIO_PORT    - Port the App Studio provider listens on (default: 8085)"
	@echo "  APP_STUDIO_HUB_URL - Hub URL the provider heartbeats to (default: $(DEV_HUB_URL))"
	@echo "  APP_STUDIO_HUB_PUBLIC_URL - Browser-reachable hub origin for private previews (default: APP_STUDIO_HUB_URL)"
	@echo "  APP_STUDIO_DEV_DATABASE_URL - Local App Studio Postgres DSN (default: postgres://appstudio:appstudio@localhost:55432/appstudio?sslmode=disable)"
	@echo "  APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true - Force non-durable App Studio message store"
	@echo ""

DOCKER_PLATFORM ?= linux/amd64

docker-build: docker-build-hub docker-build-agent ## Build all container images

docker-build-hub: ## Build railgrid-hub container image
	docker build -f deploy/Dockerfile.hub \
		--platform $(DOCKER_PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t ghcr.io/railgrid/railgrid-hub:$(VERSION) .

docker-build-agent: ## Build railgrid-agent container image
	docker build -f deploy/Dockerfile.agent \
		--platform $(DOCKER_PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t ghcr.io/railgrid/railgrid-agent:$(VERSION) .

docker-build-access-proxy: ## Build published-app access-proxy container image (infrastructure module)
	docker build -f providers/infrastructure/Dockerfile.access-proxy \
		--platform $(DOCKER_PLATFORM) \
		-t ghcr.io/railgrid/railgrid-access-proxy:$(VERSION) providers/infrastructure

docker-push-hub: docker-build-hub ## Build and push railgrid-hub container image
	docker push ghcr.io/railgrid/railgrid-hub:$(VERSION)

docker-push-agent: docker-build-agent ## Build and push railgrid-agent container image
	docker push ghcr.io/railgrid/railgrid-agent:$(VERSION)

docker-build-dex: ## Build railgrid-dex container image (custom dex with branded web overlay)
	cd hack/dex && docker build \
		--platform $(DOCKER_PLATFORM) \
		-f Dockerfile \
		-t ghcr.io/railgrid/railgrid-dex:$(VERSION) .

docker-push-dex: docker-build-dex ## Build and push railgrid-dex container image
	docker push ghcr.io/railgrid/railgrid-dex:$(VERSION)

docker-push: docker-push-hub docker-push-agent ## Build and push all container images

clean:
	rm -rf $(BINDIR)
	rm -rf $(TOOLSDIR)
	rm -rf tmp
	-kind delete cluster --name railgrid-agent 2>/dev/null

path: ## Print export command to add bin/ to PATH
	@echo 'export PATH=$(CURDIR)/$(BINDIR):$$PATH'

verify: verify-ci-selection verify-workflows verify-boilerplate verify-codegen verify-docs-cli verify-portalkit verify-provider-contract verify-design-docs verify-ui-conformance verify-tilt-browser-deployment verify-app-studio-preview-bridge-dev-key verify-app-studio-eval build-portal vet lint lint-provider-sdk lint-providers build test ## Run all checks

# --- Helm chart packaging ---

helm-build-local: ## Build and package Helm charts locally for testing
	@hack/helm-build.sh

helm-push-local: ## Push Helm charts to IMAGE_REPO registry
	@hack/helm-push.sh

helm-clean: ## Clean up built helm charts
	rm -f ./bin/*.tgz

# --- E2E Tests ---

E2E_FLAGS ?=
E2E_TIMEOUT ?= 20m

e2e: e2e-standalone ## Run default e2e suite (standalone)

e2e-standalone: build ## Run standalone e2e suite (embedded kcp + static token, no Dex)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/railgrid/railgrid-hub:test .
	docker build -f deploy/Dockerfile.agent -t ghcr.io/railgrid/railgrid-agent:test .
	RAILGRID_HUB_IMAGE=ghcr.io/railgrid/railgrid-hub \
	RAILGRID_HUB_IMAGE_TAG=test \
	RAILGRID_HUB_IMAGE_PULL_POLICY=Never \
	RAILGRID_AGENT_IMAGE=ghcr.io/railgrid/railgrid-agent \
	RAILGRID_AGENT_IMAGE_TAG=test \
	RAILGRID_AGENT_IMAGE_PULL_POLICY=Never \
	go test -count=1 ./test/e2e/suites/standalone/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-ssh: build ## Run SSH server-mode e2e suite (hub-only cluster)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/railgrid/railgrid-hub:test .
	RAILGRID_HUB_IMAGE=ghcr.io/railgrid/railgrid-hub \
	RAILGRID_HUB_IMAGE_TAG=test \
	RAILGRID_HUB_IMAGE_PULL_POLICY=Never \
	go test -count=1 ./test/e2e/suites/ssh/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-oidc: build ## Run OIDC e2e suite (Dex OIDC provider, requires --with-dex cluster)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/railgrid/railgrid-hub:test .
	RAILGRID_HUB_IMAGE=ghcr.io/railgrid/railgrid-hub \
	RAILGRID_HUB_IMAGE_TAG=test \
	RAILGRID_HUB_IMAGE_PULL_POLICY=Never \
	go test -count=1 ./test/e2e/suites/oidc/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-external-kcp: build ## Run external KCP e2e suite (kcp via Helm in kind, push-to-main only in CI)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/railgrid/railgrid-hub:test .
	RAILGRID_HUB_IMAGE=ghcr.io/railgrid/railgrid-hub \
	RAILGRID_HUB_IMAGE_TAG=test \
	RAILGRID_HUB_IMAGE_PULL_POLICY=Never \
	go test -count=1 ./test/e2e/suites/external_kcp/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Docs-install e2e. These suites execute the hack/install/ scripts that
## docs/install-external-kcp.md and docs/install-embedded-kcp.md quote,
## keeping the installation guides honest. They create their own kind
## cluster (railgrid-e2e-install) and port-forward 8443/9443 — don't run them
## concurrently with each other or with suites using those ports.
E2E_INSTALL_TIMEOUT ?= 45m

.PHONY: e2e-install-external e2e-install-embedded
e2e-install-external: build ## Run docs install e2e (two-shard kcp via kcp-operator + gateway)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/railgrid/railgrid-hub:test .
	RAILGRID_E2E_INSTALL=true \
	RAILGRID_HUB_IMAGE=ghcr.io/railgrid/railgrid-hub \
	RAILGRID_HUB_IMAGE_TAG=test \
	RAILGRID_HUB_IMAGE_PULL_POLICY=Never \
	go test -count=1 ./test/e2e/suites/installexternal/... -v -timeout $(E2E_INSTALL_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-install-embedded: build ## Run docs install e2e (embedded kcp + gateway)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/railgrid/railgrid-hub:test .
	RAILGRID_E2E_INSTALL=true \
	RAILGRID_HUB_IMAGE=ghcr.io/railgrid/railgrid-hub \
	RAILGRID_HUB_IMAGE_TAG=test \
	RAILGRID_HUB_IMAGE_PULL_POLICY=Never \
	go test -count=1 ./test/e2e/suites/installembedded/... -v -timeout $(E2E_INSTALL_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-all: build ## Run all e2e suites
	docker build -f deploy/Dockerfile.hub -t ghcr.io/railgrid/railgrid-hub:test .
	docker build -f deploy/Dockerfile.agent -t ghcr.io/railgrid/railgrid-agent:test .
	RAILGRID_HUB_IMAGE=ghcr.io/railgrid/railgrid-hub \
	RAILGRID_HUB_IMAGE_TAG=test \
	RAILGRID_HUB_IMAGE_PULL_POLICY=Never \
	RAILGRID_AGENT_IMAGE=ghcr.io/railgrid/railgrid-agent \
	RAILGRID_AGENT_IMAGE_TAG=test \
	RAILGRID_AGENT_IMAGE_PULL_POLICY=Never \
	go test -count=1 ./test/e2e/suites/... -v -timeout 30m $(E2E_FLAGS)

e2e-keep: ## Run standalone e2e, keep clusters on failure for debugging
	$(MAKE) e2e-standalone E2E_FLAGS="--keep-clusters"

.PHONY: test-model-connections test-model-connections-api lint-model-connections fix-lint-model-connections
# The providers remain standalone; run each contract in its owning module.
test-model-connections: ## Verify the shared Models UX and provider adapters
	cd providers/agents/portal && npm run typecheck && npm test
	cd providers/app-studio/portal && npm run typecheck && node --test --test-concurrency=1 src/modelsSettings.test.mjs src/modelIDSelector.test.mjs src/modelIDSelection.test.mjs src/llmSettingsValidation.test.mjs

test-model-connections-api: ## Verify model connection APIs and catalog behavior
	cd providers/agents && go test -count=1 ./api ./llm
	cd providers/app-studio && go test -count=1 ./api
	cd provider-sdk && go test -count=1 ./modelcatalog

lint-model-connections: $(GOLANGCI_LINT) ## Lint the model connection implementation in its owning modules
	cd providers/agents && $(abspath $(GOLANGCI_LINT)) run $(ARGS) ./api/... ./llm/...
	cd providers/app-studio && $(abspath $(GOLANGCI_LINT)) run $(ARGS) ./api/...
	cd provider-sdk && $(abspath $(GOLANGCI_LINT)) run $(ARGS) ./modelcatalog/...

fix-lint-model-connections: $(GOLANGCI_LINT) ## Format model connection changes with the pinned formatter
	cd providers/agents && $(abspath $(GOLANGCI_LINT)) fmt api/model_connection.go api/settings.go llm/catalog.go api/model_connection_test.go
	cd providers/app-studio && $(abspath $(GOLANGCI_LINT)) fmt api/llm_registry.go api/model_connection_test.go
	cd provider-sdk && $(abspath $(GOLANGCI_LINT)) fmt modelcatalog/catalog.go

.PHONY: test-app-studio-portal
test-app-studio-portal: ## Run the App Studio portal regression suite
	cd providers/app-studio/portal && npm test

.PHONY: package-runner-darwin
package-runner-darwin: build-runner-darwin ## Package MacOS runner binaries and the local upgrade manager
	python3 hack/runner-install/package.py $(BINDIR) darwin

.PHONY: package-runner-linux
package-runner-linux: build-runner-linux ## Package Linux runner binaries and the local upgrade manager
	python3 hack/runner-install/package.py $(BINDIR) linux

.PHONY: test-provider-action-identity
test-provider-action-identity:
	go test -race -count=1 ./pkg/hub/serviceaccounts -run 'ProviderAction|Workload'
	go test -race -count=1 ./pkg/hub -run 'ProviderAction|TenantResolver|Workload'
