.PHONY: sync-portalkit verify-portalkit
.PHONY: build-access-proxy docker-build-access-proxy
.PHONY: dev-edge-create dev-run-edge build test lint fix-lint codegen crds clean certs dev-setup run-dex run-hub run-hub-static run-hub-embedded run-hub-embedded-static run-hub-standalone run-hub-embedded-graphql run-kcp dev-login dev-login-static dev-create-workload dev dev-infra dev-run-kcp path boilerplate verify-boilerplate verify-codegen ldflags tools docker-build docker-build-hub docker-build-agent docker-build-dex docker-build-dev-agent load-dev-agent-image docker-push-dex verify help-dev dev-status dev-clean-hooks helm-build-local helm-push-local helm-clean build-quickstart-provider build-quickstart-provider-portal build-kuery-provider build-kuery-provider-portal run-provider-kuery kuery-db-up kuery-db-down install-provider-kuery init-provider-kuery uninstall-provider-kuery run-provider-quickstart install-provider-quickstart init-provider-quickstart uninstall-provider-quickstart build-infrastructure-provider build-infrastructure-provider-portal codegen-infrastructure-provider run-provider-infrastructure install-provider-infrastructure init-provider-infrastructure uninstall-provider-infrastructure build-app-studio-provider build-app-studio-provider-portal codegen-app-studio-provider app-studio-preview-console-dev-key verify-app-studio-preview-console-dev-key verify-app-studio-eval app-studio-db-up app-studio-db-down run-provider-app-studio install-provider-app-studio init-provider-app-studio uninstall-provider-app-studio build-agents-provider build-agents-provider-portal codegen-agents-provider agents-db-up agents-db-down run-provider-agents install-provider-agents init-provider-agents uninstall-provider-agents build-vibe-studio-provider build-vibe-studio-provider-portal vibe-studio-db-up vibe-studio-db-down run-provider-vibe-studio install-provider-vibe-studio init-provider-vibe-studio uninstall-provider-vibe-studio build-code-provider build-code-provider-portal codegen-code-provider run-provider-code install-provider-code init-provider-code uninstall-provider-code build-databricks-provider build-databricks-provider-portal codegen-databricks-provider run-provider-databricks install-provider-databricks init-provider-databricks uninstall-provider-databricks test-databricks-provider-chart dev-kro-up dev-kro-down dev-kro-seed e2e-infrastructure e2e-provider e2e-provider-flags e2e-provider-all

BINDIR ?= bin
GOFLAGS ?=
TOOLSDIR := hack/tools
TOOLS_GOBIN_DIR := $(abspath $(TOOLSDIR))
GO_INSTALL := ./hack/go-install.sh

# --- Tool versions ---
DEX_VER := v2.41.1
DEX := $(TOOLSDIR)/dex-$(DEX_VER)

KCP_VER := v0.30.0
KCP := $(TOOLSDIR)/kcp-$(KCP_VER)
KCP_DATA_DIR := .kcp

CONTROLLER_GEN_VER := v0.16.5
CONTROLLER_GEN_BIN := controller-gen
CONTROLLER_GEN := $(TOOLSDIR)/$(CONTROLLER_GEN_BIN)-$(CONTROLLER_GEN_VER)
export CONTROLLER_GEN

KCP_APIGEN_VER := v0.30.0
KCP_APIGEN_BIN := apigen
KCP_APIGEN_GEN := $(TOOLSDIR)/$(KCP_APIGEN_BIN)-$(KCP_APIGEN_VER)
export KCP_APIGEN_GEN

GOLANGCI_LINT_VER := v2.11.4
GOLANGCI_LINT_BIN := golangci-lint
GOLANGCI_LINT := $(TOOLSDIR)/$(GOLANGCI_LINT_BIN)-$(GOLANGCI_LINT_VER)

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
# version a real faros release tag (or a bare SHA when none is reachable).
VERSION ?= $(shell git describe --tags --always --dirty --match 'v*' 2>/dev/null || echo dev)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS_PKG := github.com/faroshq/faros/pkg/version
LDFLAGS := -s -w -X $(LDFLAGS_PKG).Version=$(VERSION) -X $(LDFLAGS_PKG).GitCommit=$(GIT_COMMIT) -X $(LDFLAGS_PKG).BuildDate=$(BUILD_DATE)

ldflags: ## Print ldflags for goreleaser
	@echo "$(LDFLAGS)"

all: build

build: build-faros build-hub build-graphql

build-faros:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/faros ./cmd/faros/

build-release: ## Build the release-tagging helper (release <component|all>)
	go build $(GOFLAGS) -o $(BINDIR)/release ./cmd/release/

build-hub:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/faros-hub ./cmd/faros-hub/

build-hub-portal: build-portal ## Build hub with embedded portal
	mkdir -p pkg/hub/portal
	rm -rf pkg/hub/portal/dist
	cp -r portal/dist pkg/hub/portal/dist
	go build $(GOFLAGS) -tags portal_embed -ldflags "$(LDFLAGS)" -o $(BINDIR)/faros-hub ./cmd/faros-hub/

build-portal: ## Build the portal Vue.js SPA
	cd portal && npm ci && npm run build

dev-portal: ## Run the portal dev server
	cd portal && npm run dev

build-graphql: ## Build the GraphQL gateway binary (listener + gateway subcommands)
	go build $(GOFLAGS) -o $(BINDIR)/faros-graphql ./cmd/graphql/

build-access-proxy: ## Build the published-app access-proxy binary (infrastructure module)
	cd providers/infrastructure && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/faros-access-proxy ./cmd/access-proxy/

# build-agent is an alias for build-faros: the agent container image now ships
# the faros CLI binary (cmd/faros/) with ENTRYPOINT [/faros, agent, run].
build-agent: build-faros

build-quickstart-provider-portal: ## Build the quickstart provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/quickstart/portal && npm install --no-audit --no-fund && npm run build

build-quickstart-provider: build-quickstart-provider-portal ## Build the quickstart reference provider binary (portal embedded)
	cd providers/quickstart && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/quickstart-provider .

build-kuery-provider-portal: ## Build the kuery provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/kuery/portal && npm install --no-audit --no-fund && npm run build

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
## API (KubernetesCluster + LinuxServer, both in edges.faros.sh), then sync
## the schema bodies into the Helm chart's files/schemas/ directory. Provider
## init applies them at runtime so tenants that bind the APIExport get both kinds.
codegen-edges-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the edges provider's local API (+ chart schemas)
	@mkdir -p providers/edges/config/crds providers/edges/config/kcp providers/edges/deploy/chart/files/schemas
	cd providers/edges && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/edges/config/crds
	./hack/apigen.sh --input-dir providers/edges/config/crds --output-dir providers/edges/config/kcp
	@for r in kubernetesclusters linuxservers workloads placements services; do \
		cp providers/edges/config/kcp/apiresourceschema-$$r.edges.faros.sh.yaml \
		   providers/edges/deploy/chart/files/schemas/$$r.edges.faros.sh.yaml; \
	done
	./hack/ensure-boilerplate.sh

.PHONY: codegen-edges-provider build-edges-provider build-edges-provider-portal \
	install-provider-edges init-provider-edges run-provider-edges uninstall-provider-edges docker-build-edges-provider

## --- edges provider dev lifecycle (install → init → run) --------------------
install-provider-edges: ## Apply edges Provider + CatalogEntry into root:faros:providers
	@test -f $(EDGES_KCP_KUBECONFIG) || { echo "kubeconfig not found at $(EDGES_KCP_KUBECONFIG); start the hub first (make run-hub-embedded-static)"; exit 1; }
	kubectl --kubeconfig=$(EDGES_KCP_KUBECONFIG) \
		--server=$(EDGES_KCP_SERVER)/clusters/root:faros:system:providers \
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
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: faros\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: faros\n  context:\n    cluster: faros\n    user: faros\ncurrent-context: faros\nusers:\n- name: faros\n  user:\n    token: %s\n' \
		"$(EDGES_KCP_SERVER)/clusters/$(EDGES_WORKSPACE_PATH)" "$$TOKEN" \
		> $(EDGES_RUNTIME_KUBECONFIG)
	FAROS_PROVIDER_KUBECONFIG=$(EDGES_RUNTIME_KUBECONFIG) \
	FAROS_SCHEMAS_DIR=$(EDGES_SCHEMAS_DIR) \
	EDGES_WORKSPACE_PATH=$(EDGES_WORKSPACE_PATH) \
		$(BINDIR)/edges-provider init

run-provider-edges: build-edges-provider ## Run the edges provider (needs: hub + install + init)
	@echo "Starting edges provider on :$(EDGES_PORT) (SINGLE-REPLICA)"
	PORT=$(EDGES_PORT) \
	FAROS_HUB_URL=$(EDGES_HUB_URL) \
	FAROS_HUB_EXTERNAL_URL=$(EDGES_HUB_EXTERNAL_URL) \
	FAROS_HUB_TOKEN=$(EDGES_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=edges \
	FAROS_PROVIDER_KUBECONFIG=$(EDGES_RUNTIME_KUBECONFIG) \
	FAROS_STATIC_TOKENS=$(EDGES_TOKEN) \
	FAROS_DEV_MODE=true \
		$(BINDIR)/edges-provider serve

uninstall-provider-edges: ## Delete edges CatalogEntry + Provider
	-kubectl --kubeconfig=$(EDGES_KCP_KUBECONFIG) \
		--server=$(EDGES_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(EDGES_MANIFEST) -f $(EDGES_PROVIDER_MANIFEST)

docker-build-edges-provider: ## Build the edges provider image (context = providers/edges)
	docker build \
		--platform $(DOCKER_PLATFORM) \
		-t ghcr.io/faroshq/faros-edges-provider:$(VERSION) \
		providers/edges

build-app-studio-provider-portal: ## Build the App Studio provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/app-studio/portal && npm install --no-audit --no-fund && npm run build

build-app-studio-provider: build-app-studio-provider-portal ## Build the App Studio provider binary (portal embedded)
	cd providers/app-studio && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/app-studio-provider .

build-agents-provider-portal: ## Build the agents provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/agents/portal && npm install --no-audit --no-fund && npm run build

build-agents-provider: build-agents-provider-portal ## Build the agents provider binary (portal embedded)
	cd providers/agents && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/agents-provider .

build-vibe-studio-provider-portal: ## Build the vibe-studio provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/vibe-studio/portal && npm install --no-audit --no-fund && npm run build

build-vibe-studio-provider: build-vibe-studio-provider-portal ## Build the vibe-studio provider binary (portal embedded)
	cd providers/vibe-studio && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/vibe-studio-provider .

build-code-provider-portal: ## Build the code provider's micro-frontend (Vite + Vue → portal/dist)
	cd providers/code/portal && npm install --no-audit --no-fund && npm run build

build-code-provider: build-code-provider-portal ## Build the code provider binary (portal embedded)
	cd providers/code && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/code-provider .

build-databricks-provider-portal: ## Build the Databricks provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/databricks/portal && npm install --no-audit --no-fund && npm run test:tableRefs && npm run typecheck && npm run build

build-databricks-provider: build-databricks-provider-portal ## Build the Databricks provider binary (portal embedded)
	cd providers/databricks && go build $(GOFLAGS) -ldflags "-X main.buildVersion=$(VERSION)" -o $(CURDIR)/$(BINDIR)/databricks-provider .

test-databricks-provider-chart: ## Lint and render both supported Databricks bootstrap modes
	@set -eu; \
		tmp_parent="$${CODEX_BUILD_CACHE_ROOT:-/var/tmp/codex-build}"; \
		mkdir -p "$$tmp_parent"; \
		tmp_dir="$$(mktemp -d "$$tmp_parent/faros-databricks-chart.XXXXXX")"; \
		cleanup() { rm -rf -- "$$tmp_dir"; }; \
		trap cleanup EXIT HUP INT TERM; \
		helm lint providers/databricks/deploy/chart; \
		helm template databricks providers/databricks/deploy/chart >"$$tmp_dir/default.yaml"; \
		grep -q 'path: /healthz' "$$tmp_dir/default.yaml"; \
		grep -q 'path: /readyz' "$$tmp_dir/default.yaml"; \
		grep -q 'healthPath: "/readyz"' "$$tmp_dir/default.yaml"; \
		helm template databricks providers/databricks/deploy/chart --set mcp.enabled=false --set mcp.disableLocalhostProtection=true --set-string 'allowedHostSuffixes[0]=workspace.private.example' >"$$tmp_dir/config.yaml"; \
		grep -q 'name: DATABRICKS_MCP_ENABLED' "$$tmp_dir/config.yaml"; \
		grep -q 'value: "false"' "$$tmp_dir/config.yaml"; \
		grep -q 'name: DATABRICKS_MCP_DISABLE_LOCALHOST_PROTECTION' "$$tmp_dir/config.yaml"; \
		grep -q 'name: DATABRICKS_ALLOWED_HOST_SUFFIXES' "$$tmp_dir/config.yaml"; \
		grep -q 'workspace.private.example' "$$tmp_dir/config.yaml"; \
		helm template databricks providers/databricks/deploy/chart --set bootstrap.enabled=false --set catalogEntry.enabled=false >"$$tmp_dir/external.yaml"; \
		if grep -q 'initContainers:' "$$tmp_dir/external.yaml" || grep -q 'kind: CatalogEntry' "$$tmp_dir/external.yaml"; then \
			echo "external bootstrap rendered an init container or CatalogEntry"; exit 1; \
		fi; \
		helm template databricks providers/databricks/deploy/chart --set bootstrap.mode=external --set catalogEntry.enabled=false >"$$tmp_dir/explicit-external.yaml"; \
		if grep -q 'initContainers:' "$$tmp_dir/explicit-external.yaml" || grep -q 'kind: CatalogEntry' "$$tmp_dir/explicit-external.yaml"; then \
			echo "explicit external bootstrap rendered an init container or CatalogEntry"; exit 1; \
		fi; \
		if helm template databricks providers/databricks/deploy/chart --set bootstrap.mode=invalid >/dev/null 2>&1; then \
			echo "invalid bootstrap mode unexpectedly rendered"; exit 1; \
		fi; \
		if helm template databricks providers/databricks/deploy/chart --set bootstrap.enabled=false --set catalogEntry.enabled=true >/dev/null 2>&1; then \
			echo "external bootstrap unexpectedly rendered a CatalogEntry"; exit 1; \
		fi

## Generate deepcopy methods + CRD YAML for the infrastructure provider's
## own API types (providers/infrastructure/apis/v1alpha1/...). The CRDs land
## under providers/infrastructure/config/crds/ and are embedded into the
## binary via go:embed — the hub does not install them, the provider does
## (one of the deliberate self-contained-system properties).
codegen-infrastructure-provider: $(CONTROLLER_GEN) ## Codegen for the infrastructure provider's local API
	@mkdir -p providers/infrastructure/config/crds
	cd providers/infrastructure && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/infrastructure/config/crds
	# The provider embeds its platform-facing CRDs from install/crds/ (//go:embed
	# in install/crds.go) and applies them into the kcp provider workspace at
	# init. Keep the embed copy in lockstep with generated schemas — otherwise
	# the operator installs stale CRDs and kcp silently prunes new fields.
	# InfrastructureProvider is intentionally NOT embedded (it is applied to the
	# host cluster by the chart), so it stays in config/ only. Remove stale embed
	# files first so deleted platform APIs cannot remain installed accidentally.
	find providers/infrastructure/install/crds -maxdepth 1 -type f -name '*.yaml' -delete
	cp providers/infrastructure/config/crds/infrastructure.faros.sh_templates.yaml \
	   providers/infrastructure/config/crds/infrastructure.faros.sh_instances.yaml \
	   providers/infrastructure/install/crds/
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
		cp providers/code/config/kcp/apiresourceschema-$$r.code.faros.sh.yaml \
		   providers/code/deploy/chart/files/schemas/$$r.code.faros.sh.yaml; \
	done
	./hack/ensure-boilerplate.sh

codegen-agents-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the agents provider's local API (+ chart schemas)
	@mkdir -p providers/agents/config/crds providers/agents/config/kcp providers/agents/deploy/chart/files/schemas
	cd providers/agents && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/agents/config/crds
	./hack/apigen.sh --input-dir providers/agents/config/crds --output-dir providers/agents/config/kcp
	@for r in agents connections schedules triggers toolsets; do \
		cp providers/agents/config/kcp/apiresourceschema-$$r.agents.faros.sh.yaml \
		   providers/agents/deploy/chart/files/schemas/$$r.agents.faros.sh.yaml; \
	done
	./hack/ensure-boilerplate.sh

codegen-app-studio-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the App Studio provider's local API (+ manifest + chart schema)
	@mkdir -p providers/app-studio/config/crds providers/app-studio/config/kcp providers/app-studio/deploy/chart/files/schemas
	cd providers/app-studio && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/app-studio/config/crds
	./hack/apigen.sh --input-dir providers/app-studio/config/crds --output-dir providers/app-studio/config/kcp
	cp providers/app-studio/config/kcp/apiresourceschema-projects.ai.faros.sh.yaml \
	   providers/app-studio/deploy/chart/files/schemas/projects.ai.faros.sh.yaml
	cp providers/app-studio/config/kcp/apiresourceschema-sessions.ai.faros.sh.yaml \
	   providers/app-studio/deploy/chart/files/schemas/sessions.ai.faros.sh.yaml
	cp providers/app-studio/config/kcp/apiresourceschema-studios.ai.faros.sh.yaml \
	   providers/app-studio/deploy/chart/files/schemas/studios.ai.faros.sh.yaml
	./hack/ensure-boilerplate.sh

codegen-databricks-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the Databricks provider's local API (+ manifest + chart schemas)
	@mkdir -p providers/databricks/config/crds providers/databricks/config/kcp providers/databricks/deploy/chart/files/schemas
	cd providers/databricks && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/databricks/config/crds
	./hack/apigen.sh --input-dir providers/databricks/config/crds --output-dir providers/databricks/config/kcp
	@for r in connections warehouses tables; do \
		cp providers/databricks/config/kcp/apiresourceschema-$$r.databricks.faros.sh.yaml \
		   providers/databricks/deploy/chart/files/schemas/$$r.databricks.faros.sh.yaml; \
	done
	./hack/ensure-boilerplate.sh

test:
	go test $(shell go list ./... | grep -v '/test/e2e')

test-util:
	go test ./pkg/util/...

lint: $(GOLANGCI_LINT) ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

fix-lint: $(GOLANGCI_LINT) ## Run golangci-lint with auto-fix
	$(GOLANGCI_LINT) run --fix ./...

vet:
	go vet ./...

# --- Code generation ---

boilerplate: ## Ensure license boilerplate on all Go files
	./hack/ensure-boilerplate.sh

verify-boilerplate: ## Verify license boilerplate on all Go files
	./hack/ensure-boilerplate.sh --verify

crds: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Generate CRDs and kcp APIResourceSchemas
	./hack/update-codegen-crds.sh

codegen: crds codegen-code-provider codegen-app-studio-provider codegen-databricks-provider codegen-infrastructure-provider boilerplate ## Generate all (CRDs + kcp resources + provider schemas + boilerplate)

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

# --- Tool installation ---

tools: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) $(GOLANGCI_LINT) ## Install all dev tools

$(CONTROLLER_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/controller-tools/cmd/controller-gen $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

$(KCP_APIGEN_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/kcp-dev/sdk/cmd/apigen $(KCP_APIGEN_BIN) $(KCP_APIGEN_VER)

$(GOLANGCI_LINT):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint $(GOLANGCI_LINT_BIN) $(GOLANGCI_LINT_VER)

# --- Dev environment ---

certs: certs/apiserver.crt

certs/apiserver.crt:
	@mkdir -p certs
	openssl req -x509 -newkey rsa:2048 -nodes \
		-keyout certs/apiserver.key -out certs/apiserver.crt \
		-days 365 -subj "/CN=localhost" \
		-addext "subjectAltName=DNS:localhost,IP:127.0.0.1"

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

dev-login: build-faros
	PATH=$(CURDIR)/$(BINDIR):$$PATH $(BINDIR)/faros login --hub-url https://localhost:9443 --insecure-skip-tls-verify

dev-login-static: build-faros ## Login using static token auth (for use with run-hub-static)
	PATH=$(CURDIR)/$(BINDIR):$$PATH $(BINDIR)/faros login --hub-url https://localhost:9443 --insecure-skip-tls-verify --token=$(STATIC_AUTH_TOKEN)

# TYPE selects the Edge type for dev-edge-create and dev-run-edge.
# Values: kubernetes (default) | server
TYPE ?= kubernetes
# Default DEV_EDGE_NAME is per-type so kubernetes and server edges can coexist.
DEV_EDGE_NAME ?= $(if $(filter server,$(TYPE)),dev-edge-server-1,dev-edge-kube-1)

dev-edge-create: build-faros ## Create an Edge resource: TYPE=kubernetes (default) or TYPE=server
	PATH=$(CURDIR)/$(BINDIR):$$PATH BINDIR=$(CURDIR)/$(BINDIR) hack/scripts/dev-edge-setup.sh $(DEV_EDGE_NAME) $(TYPE) "env=dev,provider=local"

dev-run-edge: build-faros ## Run the edge agent: TYPE=kubernetes (default) or TYPE=server
	@test -f .env.edge.$(TYPE) || (echo "Run 'make dev-edge-create TYPE=$(TYPE)' first (expected .env.edge.$(TYPE))"; exit 1)
ifeq ($(TYPE),server)
	$(BINDIR)/faros agent run \
		--hub-url=https://localhost:9443 \
		--hub-insecure-skip-tls-verify \
		--token=$(FAROS_EDGE_JOIN_TOKEN) \
		--tunnel-url=https://localhost:9443 \
		--edge-name=$(FAROS_EDGE_NAME) \
		--cluster=$(FAROS_EDGE_CLUSTER) \
		--type=server \
		--ssh-proxy-port=2222 \
		--ssh-user=faros \
		--ssh-password=password
else
	hack/scripts/ensure-kind-cluster.sh
	$(BINDIR)/faros agent run \
		--hub-url=https://localhost:9443 \
		--hub-insecure-skip-tls-verify \
		--token=$(FAROS_EDGE_JOIN_TOKEN) \
		--tunnel-url=https://localhost:9443 \
		--edge-name=$(FAROS_EDGE_NAME) \
		--kubeconfig=.kubeconfig-faros-agent \
		--cluster=$(FAROS_EDGE_CLUSTER) \
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
  -e USER_NAME=faros \
  -p 2222:2222 \
  --restart unless-stopped \
  lscr.io/linuxserver/openssh-server:latest

## --- kube edge agent, in-cluster (dev) --------------------------------------
# Runs the agent as a Deployment INSIDE the edge's kind cluster, the way the
# faros-agent chart does in production — instead of `dev-run-edge`, which runs it
# on the host against the cluster's kubeconfig.
#
# This matters for the Service kind: a host-run agent can serve the k8s
# subresource (the apiserver is reachable from the host) but NOT svc, which dials
# cluster DNS (home-assistant.home.svc). Only an in-cluster agent can resolve it.
#
# Networking: in Tiltfile.cluster the hub is a ClusterIP in the `kcp-tilt`
# cluster, reachable from the host only via Tilt's 127.0.0.1 port-forward — no
# use to a pod in the `faros-agent` cluster. Both clusters' nodes share the
# `kind` docker network, so we expose the hub via a NodePort and dial the
# kcp-tilt node IP directly.
DEV_AGENT_IMAGE_REPO ?= ghcr.io/faroshq/faros-agent
DEV_AGENT_NS         ?= faros-agent
DEV_AGENT_KIND       ?= faros-agent
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
	kubectl --context kind-$(DEV_HUB_KIND) apply -f hack/dev/faros-hub-nodeport.yaml
	@echo "==> Loading $(DEV_AGENT_IMAGE_REPO):$(VERSION) into kind/$(DEV_AGENT_KIND)"
	kind load docker-image $(DEV_AGENT_IMAGE_REPO):$(VERSION) --name $(DEV_AGENT_KIND)
	@# Source the edge env in-recipe rather than trusting the global
	@# `-include .env.edge.$$(TYPE)`: an exported TYPE=server would otherwise
	@# feed the server edge's name/cluster/token to the kubernetes agent.
	set -a; . ./.env.edge.kubernetes; set +a; \
	test -n "$$FAROS_EDGE_JOIN_TOKEN" || { echo "No FAROS_EDGE_JOIN_TOKEN in .env.edge.kubernetes — the token is cleared once an agent redeems it; re-run 'make dev-edge-create TYPE=kubernetes'"; exit 1; }; \
	helm --kubeconfig=.kubeconfig-$(DEV_AGENT_KIND) upgrade --install faros-agent deploy/charts/faros-agent \
		--namespace $(DEV_AGENT_NS) --create-namespace \
		--set image.repository=$(DEV_AGENT_IMAGE_REPO) \
		--set image.tag=$(VERSION) \
		--set image.pullPolicy=IfNotPresent \
		--set agent.edgeName=$$FAROS_EDGE_NAME \
		--set agent.cluster=$$FAROS_EDGE_CLUSTER \
		--set agent.hub.url=https://$(DEV_HUB_NODE_IP):$(DEV_HUB_NODEPORT) \
		--set agent.hub.token=$$FAROS_EDGE_JOIN_TOKEN \
		--set agent.hub.insecureSkipTLSVerify=true \
		--wait --timeout=120s
	@echo "==> Agent deployed. Logs: kubectl --kubeconfig=.kubeconfig-$(DEV_AGENT_KIND) -n $(DEV_AGENT_NS) logs -l app.kubernetes.io/name=faros-agent -f"

dev-edge-agent-incluster-logs: ## Tail the in-cluster edge agent
	kubectl --kubeconfig=.kubeconfig-$(DEV_AGENT_KIND) -n $(DEV_AGENT_NS) \
		logs -l app.kubernetes.io/name=faros-agent --tail=100 -f

dev-edge-agent-incluster-down: ## Remove the in-cluster edge agent
	helm --kubeconfig=.kubeconfig-$(DEV_AGENT_KIND) uninstall faros-agent -n $(DEV_AGENT_NS) --ignore-not-found

.PHONY: dev-edge-agent-incluster dev-edge-agent-incluster-logs dev-edge-agent-incluster-down

## --- Home Assistant on the kube edge (dev) ----------------------------------
# Deploys HA into the same kind cluster the kubernetes edge agent serves, so the
# edges provider's Service kind (spec.targetRef → home-assistant.home.svc:8123)
# has something real to proxy to. See
# providers/edges/contrib/manifests/homeassistant/README.md.
HA_MANIFESTS  ?= providers/edges/contrib/manifests/homeassistant
HA_NAMESPACE  ?= home
HA_KUBECONFIG ?= .kubeconfig-faros-agent

dev-deploy-homeassistant: ## Deploy Home Assistant into the faros-agent kind cluster
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

GRAPHQL_GRPC_ADDR ?= localhost:50051
GRAPHQL_APIEXPORT_SLICE ?= core.faros.sh
GRAPHQL_APIEXPORT_LOGICAL_CLUSTER ?= root:faros:providers

dev-run-graphql: build-graphql ## Run GraphQL (listener + gateway, kcp mode, gRPC transport, playground at :8080)
	$(BINDIR)/faros-graphql run \
		--kubeconfig=$(KCP_DATA_DIR)/admin.kubeconfig \
		--grpc-addr=$(GRAPHQL_GRPC_ADDR) \
		--apiexport-endpoint-slice-name=$(GRAPHQL_APIEXPORT_SLICE) \
		--apiexport-endpoint-slice-logicalcluster=$(GRAPHQL_APIEXPORT_LOGICAL_CLUSTER) \
		--workspace-schema-kubeconfig-override=$(KCP_DATA_DIR)/admin.kubeconfig \
		--enable-playground \
		--gateway-port=9090

# --- Hub configuration options ---
# These can be combined to create different run configurations.

STATIC_AUTH_TOKEN ?= dev-token

# Base hub flags (always needed)
HUB_FLAGS_BASE := \
	--serving-cert-file=certs/apiserver.crt \
	--serving-key-file=certs/apiserver.key \
	--hub-external-url=https://localhost:9443 \
	--dev-mode -v 4

# Auth: OIDC via Dex
HUB_FLAGS_OIDC := \
	--idp-issuer-url=https://localhost:5554/dex \
	--idp-client-id=faros \
	--idp-client-secret=ZXhhbXBsZS1hcHAtc2VjcmV0

# Auth: Static token
HUB_FLAGS_STATIC := \
	--static-auth-token=$(STATIC_AUTH_TOKEN) \
	--admin-users=$(ADMIN_USERS)

# Platform-admin identities allowed at /api/admin/* + the portal /bonkers area.
# The dev static token "$(STATIC_AUTH_TOKEN)" resolves (proxy.ensureStaticTokenUserOnce)
# to email static-<first8chars>@faros.local — for dev-token that's
# static-dev-toke@faros.local. Override for OIDC dev with your real email.
ADMIN_USERS ?= static-dev-toke@faros.local

# KCP: External (requires running kcp separately)
HUB_FLAGS_KCP_EXTERNAL := \
	--external-kcp-kubeconfig=.kcp/admin.kubeconfig

# KCP: Embedded (runs kcp in-process)
# --kcp-shard-external-url / --kcp-shard-virtual-workspace-url ARE NOT set
# by default — kcp defaults them to localhost which works for in-process
# consumers (hub's GraphQL listener, controllers, kcp proxy). Overriding
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

# GraphQL: Embedded (runs listener+gateway in-process alongside hub)
GRAPHQL_APIEXPORT_SLICE ?= core.faros.sh
GRAPHQL_APIEXPORT_LOGICAL_CLUSTER ?= root:faros:providers
GRAPHQL_GRPC_ADDR ?= localhost:50051

HUB_FLAGS_GRAPHQL_EMBEDDED := \
	--embedded-graphql \
	--graphql-apiexport-slice-name=$(GRAPHQL_APIEXPORT_SLICE) \
	--graphql-apiexport-logical-cluster=$(GRAPHQL_APIEXPORT_LOGICAL_CLUSTER) \
	--graphql-grpc-addr=$(GRAPHQL_GRPC_ADDR) \
	--graphql-playground

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
	$(BINDIR)/faros-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_OIDC) $(HUB_FLAGS_KCP_EXTERNAL)

## External KCP + static token auth (requires: make dev-run-kcp)
run-hub-static: build-hub certs
	@source $(SERVICE_HOOKS) && require_service kcp "make dev-run-kcp"
	$(BINDIR)/faros-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EXTERNAL)

## Embedded KCP + OIDC auth (requires: make run-dex)
run-hub-embedded: build-hub certs
	@source $(SERVICE_HOOKS) && require_service dex "make run-dex"
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/faros-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_OIDC) $(HUB_FLAGS_KCP_EMBEDDED)

## Embedded KCP + static token auth + embedded GraphQL + portal dev proxy (standalone - no external deps)
run-hub-embedded-static: build-hub certs
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/faros-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EMBEDDED) $(HUB_FLAGS_GRAPHQL_EMBEDDED) $(HUB_FLAGS_PORTAL_DEV)

## Embedded KCP + static token + embedded GraphQL (fully standalone)
run-hub-standalone: build-hub certs
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/faros-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EMBEDDED) $(HUB_FLAGS_GRAPHQL_EMBEDDED)

## Embedded KCP + OIDC + embedded GraphQL
run-hub-embedded-graphql: build-hub certs
	@source $(SERVICE_HOOKS) && require_service dex "make run-dex"
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/faros-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_OIDC) $(HUB_FLAGS_KCP_EMBEDDED) $(HUB_FLAGS_GRAPHQL_EMBEDDED)

# Local kcp checkout to iterate against. Defaults to the standard per-user Go
# workspace path. Override on the CLI or via env:
#   make tilt-cluster TILT_KCP_DIR=/path/to/kcp
#   make tilt-cluster KCP_DIR=/path/to/kcp
TILT_KCP_DIR ?= $(or $(KCP_DIR),$(HOME)/go/src/github.com/kcp-dev/kcp)
# Tilt's KIND image loader stages a full `docker save` tarball in TMPDIR. Keep
# that payload off capacity-limited /tmp tmpfs mounts used by development hosts.
TILT_TMPDIR ?= $(CURDIR)/.kcp/tmp/tilt

## Full multi-shard kcp in a kind cluster + faros-hub in-cluster, against a local kcp checkout
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
	TMPDIR="$(TILT_TMPDIR)" tilt up -f Tiltfile.cluster -- --kcp-dir="$(TILT_KCP_DIR)"

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
QUICKSTART_HUB_URL ?= https://localhost:9443
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
QUICKSTART_WORKSPACE_PATH ?= root:faros:providers:quickstart
QUICKSTART_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/quickstart-runtime.kubeconfig

# --- edges provider (single provider, both kinds) --------------------------
# Runs SINGLE-REPLICA (revdial global dialer map). Reads a provider kubeconfig at
# runtime (token validation + cross-tenant controllers), so the run target passes
# FAROS_PROVIDER_KUBECONFIG unlike the broker providers.
EDGES_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
EDGES_KCP_SERVER ?= https://localhost:6443
EDGES_HUB_URL ?= https://localhost:9443
EDGES_HUB_EXTERNAL_URL ?= $(EDGES_HUB_URL)
EDGES_TOKEN ?= $(STATIC_AUTH_TOKEN)
EDGES_PORT ?= 8088
EDGES_MANIFEST ?= providers/edges/manifest.yaml
EDGES_PROVIDER_MANIFEST ?= providers/edges/provider.yaml
EDGES_WORKSPACE_PATH ?= root:faros:providers:edges
EDGES_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/edges-runtime.kubeconfig
EDGES_SCHEMAS_DIR ?= $(CURDIR)/providers/edges/deploy/chart/files/schemas

## Run the quickstart provider binary locally. Heartbeats to the hub on
## $(QUICKSTART_HUB_URL); TLS verification skipped (dev cert is self-signed).
run-provider-quickstart: build-quickstart-provider ## Run the quickstart provider (requires: make run-hub-embedded-static + make install-provider-quickstart)
	@echo "Starting quickstart provider on :$(QUICKSTART_PORT)"
	@echo "  hub:   $(QUICKSTART_HUB_URL)"
	@echo "  token: $(QUICKSTART_TOKEN)"
	PORT=$(QUICKSTART_PORT) \
	FAROS_HUB_URL=$(QUICKSTART_HUB_URL) \
	FAROS_HUB_TOKEN=$(QUICKSTART_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=quickstart \
		$(BINDIR)/quickstart-provider

## Apply the quickstart CatalogEntry into root:faros:providers. Idempotent.
## Requires the hub to be running so the admin kubeconfig exists.
install-provider-quickstart: ## Apply quickstart Provider + CatalogEntry into root:faros:providers
	@test -f $(QUICKSTART_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(QUICKSTART_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=$(QUICKSTART_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(QUICKSTART_PROVIDER_MANIFEST) -f $(QUICKSTART_MANIFEST)

## Run provider e2e suite (embedded kcp + quickstart-provider subprocess).
## Lightweight — no kind/Helm, just two host binaries the suite drives over
## HTTP + kcp dynamic clients. FAROS_E2E_KEEP_DATA=true preserves logs/data.
E2E_PROVIDER_TIMEOUT ?= 10m
e2e-provider: build-hub build-quickstart-provider ## Run provider e2e suite
	@test -z "$$(lsof -ti :19443 :16443 :18081 :2380 2>/dev/null)" || { \
		echo "ports 19443/16443/18081/2380 are in use; stop any running faros-hub/quickstart-provider first"; \
		exit 1; \
	}
	go test ./test/e2e/suites/provider/... -v -timeout $(E2E_PROVIDER_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Run --providers flag mechanics suite (dep validation, unknown name,
## filtered enable). Each test spawns its own hub on the standard
## 19443/16443/2380 ports, so this MUST NOT run concurrently with
## `e2e-provider` — the Makefile checks the port up-front.
E2E_PROVIDER_FLAGS_TIMEOUT ?= 10m
e2e-provider-flags: build-hub ## Run --providers flag mechanics suite
	@test -z "$$(lsof -ti :19443 :16443 :2380 2>/dev/null)" || { \
		echo "ports 19443/16443/2380 are in use; stop any running faros-hub first (e.g. pkill faros-hub)"; \
		exit 1; \
	}
	go test ./test/e2e/suites/providerflags/... -v -timeout $(E2E_PROVIDER_FLAGS_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

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
		echo "ports 19453/16453/18086/2380 are in use; stop any running faros-hub/infrastructure-provider first"; \
		exit 1; \
	}
	go test ./test/e2e/suites/infraprovider/... -v -timeout $(E2E_INFRA_PROVIDER_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Provider-actions E2E: embedded hub plus host-process App Studio and
## Databricks providers, a local TLS fake upstream, and a generated Node app
## invoking the action through the hub. FAROS_E2E_KEEP_DATA=true preserves
## logs and source/readiness/interaction evidence under the suite temp dir.
E2E_PROVIDER_ACTIONS_TIMEOUT ?= 20m
.PHONY: e2e-provider-actions e2e-provider-actions-live e2e-provider-actions-npm
e2e-provider-actions: build-hub build-app-studio-provider build-databricks-provider ## Run local generated-app provider-actions E2E
	@test -z "$$(lsof -ti :19463 :16463 :18085 :18086 :25063 :2380 2>/dev/null)" || { \
		echo "ports 19463/16463/18085/18086/25063/2380 are in use; stop the provider-actions E2E processes first"; \
		exit 1; \
	}
	go test ./test/e2e/suites/provideractions/... -v -timeout $(E2E_PROVIDER_ACTIONS_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Optional bounded smoke against an already-running local hub/provider setup.
## Set FAROS_E2E_PROVIDER_ACTIONS_LIVE=true plus FAROS_LIVE_HUB_URL,
## FAROS_LIVE_PROJECT, and FAROS_LIVE_ACTIONS_TOKEN_FILE.
e2e-provider-actions-live: ## Run the opt-in live generated-app provider-actions smoke
	FAROS_E2E_PROVIDER_ACTIONS_LIVE_ONLY=true FAROS_E2E_PROVIDER_ACTIONS_LIVE=true \
		go test ./test/e2e/suites/provideractions/... -run '^TestOptionalLiveProviderActionSDK$$' -v -timeout $(E2E_PROVIDER_ACTIONS_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Optional registry-backed package smoke. The live-only flag keeps TestMain
## from starting the full hub/provider stack; set FAROS_E2E_PROVIDER_ACTIONS_NPM_REGISTRY
## to use a non-default registry mirror.
e2e-provider-actions-npm: ## Verify the published Actions SDK alias with a clean npm install
	FAROS_E2E_PROVIDER_ACTIONS_LIVE_ONLY=true FAROS_E2E_PROVIDER_ACTIONS_NPM_SMOKE=true \
		go test ./test/e2e/suites/provideractions/... -run '^TestOptionalPublishedActionsSDKCleanInstall$$' -v -timeout $(E2E_PROVIDER_ACTIONS_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

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
		echo "ports 19463/16463/18088/2380 are in use; stop any running faros-hub/edges-provider first"; \
		exit 1; \
	}
	go test ./test/e2e/suites/edges/... -v -timeout $(E2E_EDGES_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Edges DATA-PLANE connectivity e2e (embedded kcp over HTTPS + edges-provider
## + a real faros agent against a kind cluster). Proves the reverse tunnel:
## registers a KubernetesCluster, enables edges + the edge-proxy grant, runs the
## agent, and streams `kubectl get nodes` down the tunnel (agent -> hub backend
## proxy -> out-of-process edges provider -> agent -> kind API server). Needs
## kind + docker + kubectl on PATH. Shares embedded-kcp etcd port 2380 — do not
## run concurrently with the other subprocess suites.
E2E_EDGES_CONN_TIMEOUT ?= 15m
e2e-edges-connectivity: build-hub build-edges-provider build-faros certs ## Run edges data-plane connectivity e2e (needs kind)
	@test -z "$$(lsof -ti :19473 :16473 :18098 :2380 2>/dev/null)" || { \
		echo "ports 19473/16473/18098/2380 are in use; stop any running faros-hub/edges-provider first"; \
		exit 1; \
	}
	go test ./test/e2e/suites/edgesconn/... -v -timeout $(E2E_EDGES_CONN_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Tilt-cluster suite: runs against an ALREADY-RUNNING operator-deployed,
## multi-shard Tilt stack (start it in another terminal with `make tilt-cluster`).
## Unlike the other e2e suites it does NOT spawn its own processes — it connects
## to the live stack (kcp front-proxy via tilt-frontproxy.kubeconfig, the
## in-cluster hub, the host-run providers) and verifies the providers end-to-end:
## provider registration, the templates catalog/projection, MCP tool federation,
## and the per-tenant identity gate. Endpoints override via FAROS_E2E_* env.
E2E_TILT_TIMEOUT ?= 10m
E2E_TILT_HUB_URL ?= https://localhost:9443
E2E_TILT_INFRA_URL ?= http://localhost:8082
E2E_TILT_KCP_KUBECONFIG ?= $(CURDIR)/tilt-frontproxy.kubeconfig
E2E_TILT_RUNTIME_KUBECONFIG ?= $(CURDIR)/.faros-cluster.kubeconfig
E2E_TILT_OPERATOR_NAMESPACE ?= faros-infrastructure-operator
E2E_TILT_CONFIG_CONNECTOR_TIMEOUT ?= 30m
E2E_TILT_TERRAFORM_TIMEOUT ?= 30m
KCC_INSTALL_SCRIPT ?= providers/infrastructure/contrib/config-connector/install.sh
KCC_ENABLE_SCRIPT ?= providers/infrastructure/contrib/config-connector/enable.sh
KCC_TEMPLATE_FILE ?= providers/infrastructure/contrib/config-connector/pubsub-template.yaml
KCC_PROVIDER_WORKSPACE ?= root:faros:providers:infrastructure
TERRAFORM_INSTALL_SCRIPT ?= providers/infrastructure/contrib/terraform/install.sh
TERRAFORM_ENABLE_SCRIPT ?= providers/infrastructure/contrib/terraform/enable.sh
TERRAFORM_TEMPLATE_FILE ?= providers/infrastructure/contrib/terraform/terraform-stack-template.yaml
TERRAFORM_PROVIDER_WORKSPACE ?= root:faros:providers:infrastructure
TERRAFORM_KIND_CLUSTER_NAME ?= kcp-tilt
INFRAKUBE_POC_COMMIT := 2fed999fb3c30e8415da5489eb8cf1eec8b765f0
INFRAKUBE_POC_IMAGE_TAG := $(shell printf '%s' $(INFRAKUBE_POC_COMMIT) | cut -c1-12)
INFRAKUBE_POC_CONTROLLER_IMAGE ?= faros/infrakube:$(INFRAKUBE_POC_IMAGE_TAG)
INFRAKUBE_POC_TASK_IMAGE ?= faros/infrakube-task:$(INFRAKUBE_POC_IMAGE_TAG)
# Public dev configuration uses FAROS_CONFIG_CONNECTOR_GCP_*; keep the older
# FAROS_E2E_GCP_* names as an explicit compatibility fallback for callers that
# invoke these targets from an exported environment.
KCC_GCP_PROJECT ?= $(or $(FAROS_CONFIG_CONNECTOR_GCP_PROJECT),$(FAROS_E2E_GCP_PROJECT))
KCC_GCP_CREDENTIALS_FILE ?= $(or $(FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE),$(FAROS_E2E_GCP_CREDENTIALS_FILE))
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
	go test ./test/e2e/suites/tiltcluster/... -v -timeout $(E2E_TILT_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

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
	FAROS_E2E_CONFIG_CONNECTOR_COMPOSITION=1 \
	FAROS_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
		go test ./test/e2e/suites/tiltcluster/... -run '^TestConfigConnectorComposition$$' -v -timeout $(E2E_TILT_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

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
	@test -n "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy FAROS_E2E_GCP_CREDENTIALS_FILE) is required"; exit 1; }
	@test -f "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "Config Connector credentials file does not exist"; exit 1; }
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_E2E_GCP_CREDENTIALS_FILE="$(KCC_GCP_CREDENTIALS_FILE)" \
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
	FAROS_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_KCC_KCP_SERVER="$(KCC_KCP_SERVER)" \
	FAROS_KCC_PROVIDER_WORKSPACE="$(KCC_PROVIDER_WORKSPACE)" \
	FAROS_KCC_TEMPLATE_FILE="$(KCC_TEMPLATE_FILE)" \
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
	@test -n "$(KCC_GCP_PROJECT)" || { echo "FAROS_CONFIG_CONNECTOR_GCP_PROJECT (or legacy FAROS_E2E_GCP_PROJECT) is required"; exit 1; }
	@test -n "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy FAROS_E2E_GCP_CREDENTIALS_FILE) is required"; exit 1; }
	@test -f "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "Config Connector credentials file does not exist"; exit 1; }
	FAROS_E2E_CONFIG_CONNECTOR_GCP=1 \
	FAROS_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
	FAROS_E2E_GCP_PROJECT="$(KCC_GCP_PROJECT)" \
	FAROS_E2E_GCP_CREDENTIALS_FILE="$(KCC_GCP_CREDENTIALS_FILE)" \
		go test ./test/e2e/suites/tiltcluster/... -run '^TestConfigConnectorGCPPubSubLifecycle$$' -v -timeout $(E2E_TILT_CONFIG_CONNECTOR_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

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
	@test -n "$(KCC_GCP_PROJECT)" || { echo "FAROS_CONFIG_CONNECTOR_GCP_PROJECT (or legacy FAROS_E2E_GCP_PROJECT) is required"; exit 1; }
	@test -n "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy FAROS_E2E_GCP_CREDENTIALS_FILE) is required"; exit 1; }
	@test -f "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "Config Connector credentials file does not exist"; exit 1; }
	FAROS_E2E_CONFIG_CONNECTOR_GCP=1 \
	FAROS_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
	FAROS_E2E_GCP_PROJECT="$(KCC_GCP_PROJECT)" \
	FAROS_E2E_GCP_CREDENTIALS_FILE="$(KCC_GCP_CREDENTIALS_FILE)" \
		go test ./test/e2e/suites/tiltcluster/... -run '^TestConfigConnectorGCPPubSubSmoke$$' -v -timeout $(E2E_TILT_CONFIG_CONNECTOR_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-tilt-cluster-config-connector-gcp-smoke: e2e-tilt-cluster-config-connector-smoke

.PHONY: config-connector-install config-connector-enable config-connector-smoke
config-connector-install: e2e-tilt-cluster-config-connector-gcp-install
config-connector-enable: e2e-tilt-cluster-config-connector-enable
config-connector-smoke: e2e-tilt-cluster-config-connector-smoke

e2e-tilt-cluster-config-connector-gcp: ## Install Config Connector and run the real Pub/Sub create/delete E2E
	@test -n "$(KCC_GCP_PROJECT)" || { echo "FAROS_CONFIG_CONNECTOR_GCP_PROJECT (or legacy FAROS_E2E_GCP_PROJECT) is required"; exit 1; }
	@test -n "$(KCC_GCP_CREDENTIALS_FILE)" || { echo "FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE (or legacy FAROS_E2E_GCP_CREDENTIALS_FILE) is required"; exit 1; }
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
	FAROS_E2E_TERRAFORM_COMPOSITION=1 \
	FAROS_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
		go test -count=1 ./test/e2e/suites/tiltcluster/... -run '^TestTerraformComposition$$' -v -timeout $(E2E_TILT_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

.PHONY: e2e-tilt-cluster-terraform-install e2e-tilt-cluster-terraform-enable e2e-tilt-cluster-terraform-smoke e2e-tilt-cluster-terraform-infrakube
e2e-tilt-cluster-terraform-install: ## Install pinned Infrakube into the operator-managed runtime
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { echo "runtime kubeconfig is required; run make tilt-cluster first"; exit 1; }
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_TERRAFORM_KIND_CLUSTER_NAME="$(TERRAFORM_KIND_CLUSTER_NAME)" \
	INFRAKUBE_COMMIT="$(INFRAKUBE_POC_COMMIT)" \
	CONTROLLER_IMAGE="$(INFRAKUBE_POC_CONTROLLER_IMAGE)" \
	TASK_IMAGE="$(INFRAKUBE_POC_TASK_IMAGE)" \
		$(TERRAFORM_INSTALL_SCRIPT)

e2e-tilt-cluster-terraform-enable: ## Enable and wait for the opt-in Terraform Template
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { echo "kcp front-proxy kubeconfig is required; run make tilt-cluster first"; exit 1; }
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { echo "runtime kubeconfig is required; run make tilt-cluster first"; exit 1; }
	FAROS_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_TERRAFORM_KCP_SERVER="$(TERRAFORM_KCP_SERVER)" \
	FAROS_TERRAFORM_PROVIDER_WORKSPACE="$(TERRAFORM_PROVIDER_WORKSPACE)" \
	FAROS_TERRAFORM_TEMPLATE_FILE="$(TERRAFORM_TEMPLATE_FILE)" \
		$(TERRAFORM_ENABLE_SCRIPT)

e2e-tilt-cluster-terraform-smoke: ## Apply and destroy Terraform through the enabled Template
	@test -f "$(E2E_TILT_KCP_KUBECONFIG)" || { echo "kcp front-proxy kubeconfig is required; run make tilt-cluster first"; exit 1; }
	@test -f "$(E2E_TILT_RUNTIME_KUBECONFIG)" || { echo "runtime kubeconfig is required; run make tilt-cluster first"; exit 1; }
	FAROS_E2E_TERRAFORM=1 \
	FAROS_E2E_TILT_KUBECONFIG="$(E2E_TILT_KCP_KUBECONFIG)" \
	FAROS_E2E_TILT_RUNTIME_KUBECONFIG="$(E2E_TILT_RUNTIME_KUBECONFIG)" \
	FAROS_E2E_TILT_OPERATOR_NAMESPACE="$(E2E_TILT_OPERATOR_NAMESPACE)" \
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
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: faros\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: faros\n  context:\n    cluster: faros\n    user: faros\ncurrent-context: faros\nusers:\n- name: faros\n  user:\n    token: %s\n' \
		"$(QUICKSTART_KCP_SERVER)/clusters/$(QUICKSTART_WORKSPACE_PATH)" "$$TOKEN" \
		> $(QUICKSTART_RUNTIME_KUBECONFIG)
	@echo "Running quickstart-provider init (creates APIExport + endpoint slice + bind grant)"
	FAROS_PROVIDER_KUBECONFIG=$(QUICKSTART_RUNTIME_KUBECONFIG) \
	QUICKSTART_WORKSPACE_PATH=$(QUICKSTART_WORKSPACE_PATH) \
	FAROS_SCHEMAS_DIR=/nonexistent \
		$(BINDIR)/quickstart-provider init

## Delete the quickstart CatalogEntry + Provider. Deleting the Provider triggers
## full teardown of root:faros:providers:quickstart (workspace, SA, APIExport)
## via the controller's finalizer.
uninstall-provider-quickstart: ## Delete quickstart CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=$(QUICKSTART_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(QUICKSTART_MANIFEST) -f $(QUICKSTART_PROVIDER_MANIFEST)

# --- Provider kuery (local dev) ---
# Mirror of the quickstart pattern above. Distinct port (8084) so the demo
# providers can run side-by-side. Phase 1 skeleton — see
# docs/kuery-provider-architecture.md for the phasing.

KUERY_PORT ?= 8084
KUERY_HUB_URL ?= https://localhost:9443
KUERY_TOKEN ?= $(STATIC_AUTH_TOKEN)
KUERY_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
KUERY_KCP_SERVER ?= https://localhost:6443
KUERY_MANIFEST ?= providers/kuery/manifest.yaml
KUERY_PROVIDER_MANIFEST ?= providers/kuery/provider.yaml
KUERY_WORKSPACE_PATH ?= root:faros:providers:kuery
KUERY_SCHEMAS_DIR ?= providers/kuery/deploy/chart/files/schemas
# Optional: identityHash of the edges export for kuery's first-party edges
# permission claim (copy from /bonkers Root identities). Empty → APIExport is
# still created (Enable binds), but edge engagement won't activate until set.
KUERY_EDGES_IDENTITY_HASH ?=
# Dev runtime kubeconfig for the engagement controller, written by
# init-provider-kuery from the provider SA token the hub mints.
KUERY_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/kuery-runtime.kubeconfig

# Local store backend. Dev always runs Postgres — the same backend as
# production — because Postgres-only SQL (jsonb_array_elements, uuid columns,
# …) diverges from SQLite and passing on SQLite has shipped real query bugs.
# kuery-db-up starts a throwaway container matching KUERY_DEV_DATABASE_URL.
KUERY_POSTGRES_CONTAINER ?= faros-kuery-postgres
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
	@if [ -f $(KUERY_RUNTIME_KUBECONFIG) ]; then \
		echo "  engagement: $(KUERY_RUNTIME_KUBECONFIG)"; \
	else \
		echo "  engagement: DISABLED (run 'make init-provider-kuery' after install-provider-kuery)"; \
	fi
	@# Dev always runs Postgres. Fall back to the local dev container DSN when
	@# KUERY_STORE_DSN is unset (external Postgres overrides it).
	STORE_DSN="$${KUERY_STORE_DSN:-$(KUERY_STORE_DSN)}"; \
	if [ -z "$$STORE_DSN" ]; then \
		STORE_DSN="$(KUERY_DEV_DATABASE_URL)"; \
	fi; \
	echo "  store: postgres ($$STORE_DSN)"; \
	PORT=$(KUERY_PORT) \
	FAROS_HUB_URL=$(KUERY_HUB_URL) \
	FAROS_HUB_TOKEN=$(KUERY_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=kuery \
	FAROS_PROVIDER_KUBECONFIG=$(KUERY_RUNTIME_KUBECONFIG) \
	FAROS_DEV_ALLOW_TENANT_QUERY=true \
	KUERY_STORE_DRIVER=postgres \
	KUERY_STORE_DSN="$$STORE_DSN" \
		$(BINDIR)/kuery-provider

install-provider-kuery: ## Apply kuery Provider + CatalogEntry into root:faros:providers
	@test -f $(KUERY_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KUERY_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KUERY_KCP_KUBECONFIG) \
		--server=$(KUERY_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(KUERY_PROVIDER_MANIFEST) -f $(KUERY_MANIFEST)

## Dev bootstrap for the engagement controller. The Provider controller writes
## the minted kubeconfig into a Secret in root:faros:providers, but host-binary
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
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: faros\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: faros\n  context:\n    cluster: faros\n    user: faros\ncurrent-context: faros\nusers:\n- name: faros\n  user:\n    token: %s\n' \
		"$(KUERY_KCP_SERVER)/clusters/$(KUERY_WORKSPACE_PATH)" "$$TOKEN" \
		> $(KUERY_RUNTIME_KUBECONFIG)
	@# kcp requires kuery's first-party edges permissionClaim to carry the
	@# identityHash of the export that serves edges. Tenants consume edges
	@# through the core.faros.sh binding, so resolve core.faros.sh's
	@# identityHash from system:controllers (override via KUERY_EDGES_IDENTITY_HASH).
	@# The slice + bind grant are created inside install.Bootstrap using the
	@# provider SA (cluster-admin → has `bind`), not the admin kubeconfig.
	@echo "Running kuery-provider init (schemas + APIExport + endpoint slice + bind grant)"
	@HASH="$(KUERY_EDGES_IDENTITY_HASH)"; \
	if [ -z "$$HASH" ]; then \
		HASH=$$(kubectl --kubeconfig=$(KUERY_KCP_KUBECONFIG) \
			--server=$(KUERY_KCP_SERVER)/clusters/root:faros:system:controllers \
			--insecure-skip-tls-verify \
			get apiexport core.faros.sh -o jsonpath='{.status.identityHash}'); \
		echo "resolved edges identityHash from core.faros.sh: $$HASH"; \
	fi; \
	test -n "$$HASH" || { echo "could not resolve core.faros.sh identityHash — is the hub bootstrapped?"; exit 1; }; \
	FAROS_PROVIDER_KUBECONFIG=$(KUERY_RUNTIME_KUBECONFIG) \
	KUERY_WORKSPACE_PATH=$(KUERY_WORKSPACE_PATH) \
	FAROS_SCHEMAS_DIR=$(KUERY_SCHEMAS_DIR) \
	KUERY_EDGES_IDENTITY_HASH=$$HASH \
		$(BINDIR)/kuery-provider init

uninstall-provider-kuery: ## Delete kuery CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KUERY_KCP_KUBECONFIG) \
		--server=$(KUERY_KCP_SERVER)/clusters/root:faros:system:providers \
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
KROMC_HUB_URL ?= https://localhost:9443
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
APP_STUDIO_HUB_URL ?= https://localhost:9443
APP_STUDIO_TOKEN ?= $(STATIC_AUTH_TOKEN)
# Optional external HTTPS hub origin for generated development runtimes. Keep
# unset unless the operator has configured a pod-reachable, certificate-valid
# URL; the App Studio launcher must not invent an insecure localhost default.
FAROS_ACTIONS_EXTERNAL_URL ?=
APP_STUDIO_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
APP_STUDIO_KCP_SERVER ?= https://localhost:6443
APP_STUDIO_WORKSPACE_PATH ?= root:faros:providers:app-studio
APP_STUDIO_PROVIDER_KUBECONFIG ?= $(KCP_DATA_DIR)/app-studio-provider.kubeconfig
APP_STUDIO_SCHEMAS_DIR ?= providers/app-studio/deploy/chart/files/schemas
APP_STUDIO_MANIFEST ?= providers/app-studio/manifest.yaml
APP_STUDIO_PROVIDER_MANIFEST ?= providers/app-studio/provider.yaml
APP_STUDIO_DATABASE_URL ?=
APP_STUDIO_IN_MEMORY_MESSAGE_STORE ?=
APP_STUDIO_DEV_DATABASE_URL ?= postgres://appstudio:appstudio@localhost:55432/appstudio?sslmode=disable
APP_STUDIO_POSTGRES_CONTAINER ?= faros-app-studio-postgres
APP_STUDIO_POSTGRES_IMAGE ?= mirror.gcr.io/library/postgres:16-alpine
APP_STUDIO_POSTGRES_PORT ?= 55432
APP_STUDIO_POSTGRES_DATA_DIR ?= $(KCP_DATA_DIR)/app-studio-postgres
APP_STUDIO_POSTGRES_USER ?= appstudio
APP_STUDIO_POSTGRES_PASSWORD ?= appstudio
APP_STUDIO_POSTGRES_DB ?= appstudio
APP_STUDIO_PREVIEW_CONSOLE_DEV_KEY_DIR ?= $(KCP_DATA_DIR)/app-studio-preview-console
APP_STUDIO_PREVIEW_CONSOLE_DEV_PRIVATE_KEY ?= $(APP_STUDIO_PREVIEW_CONSOLE_DEV_KEY_DIR)/private-key.pem
APP_STUDIO_PREVIEW_CONSOLE_DEV_JWKS ?= $(APP_STUDIO_PREVIEW_CONSOLE_DEV_KEY_DIR)/verification-jwks.json
APP_STUDIO_PREVIEW_CONSOLE_DEV_KEY_ID ?= $(APP_STUDIO_PREVIEW_CONSOLE_DEV_KEY_DIR)/key-id

# --- agents provider (long-running personal AI agents) ---
AGENTS_PORT ?= 8087
AGENTS_HUB_URL ?= https://localhost:9443
AGENTS_TOKEN ?= $(STATIC_AUTH_TOKEN)
AGENTS_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
AGENTS_KCP_SERVER ?= https://localhost:6443
AGENTS_WORKSPACE_PATH ?= root:faros:providers:agents
AGENTS_PROVIDER_KUBECONFIG ?= $(KCP_DATA_DIR)/agents-provider.kubeconfig
AGENTS_SCHEMAS_DIR ?= providers/agents/deploy/chart/files/schemas
AGENTS_MANIFEST ?= providers/agents/manifest.yaml
AGENTS_PROVIDER_MANIFEST ?= providers/agents/provider.yaml
# Durable store: dev runs use a local Postgres container by default (mirrors
# app-studio). Set AGENTS_IN_MEMORY_STORE=true for a non-durable quick run.
AGENTS_IN_MEMORY_STORE ?=
AGENTS_DEV_DATABASE_URL ?= postgres://agents:agents@localhost:55434/agents?sslmode=disable
AGENTS_POSTGRES_CONTAINER ?= faros-agents-postgres
AGENTS_POSTGRES_IMAGE ?= mirror.gcr.io/library/postgres:16-alpine
AGENTS_POSTGRES_PORT ?= 55434
AGENTS_POSTGRES_DATA_DIR ?= $(KCP_DATA_DIR)/agents-postgres

## Run the infrastructure provider binary locally. Heartbeats to the hub on
## $(KROMC_HUB_URL); TLS verification skipped (dev cert is self-signed).
## KRO_KUBECONFIG is left unset by default → provider serves the baked-in
## stub catalog so the UI is demoable without standing up a real central
## kro cluster. Point KRO_KUBECONFIG at a real kubeconfig to use the
## real client + your own ResourceGraphDefinitions.
run-provider-infrastructure: build-infrastructure-provider app-studio-preview-console-dev-key ## Run the infrastructure provider (requires: make run-hub-embedded-static + make install-provider-infrastructure)
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
	FAROS_HUB_URL=$(KROMC_HUB_URL) \
	FAROS_HUB_TOKEN=$(KROMC_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=infrastructure \
	FAROS_DEV_ALLOW_TENANT_QUERY=true \
	INFRASTRUCTURE_WORKSPACE_PATH=$${INFRASTRUCTURE_WORKSPACE_PATH:-$(INFRASTRUCTURE_WORKSPACE_PATH)} \
	KRO_KUBECONFIG=$${KRO_KUBECONFIG:-$$( [ -f "$(KRO_KIND_KUBECONFIG)" ] && echo "$(KRO_KIND_KUBECONFIG)" )} \
	INFRASTRUCTURE_KUBECONFIG=$${INFRASTRUCTURE_KUBECONFIG:-$$( [ -f "$(INFRASTRUCTURE_RUNTIME_KUBECONFIG)" ] && echo "$(INFRASTRUCTURE_RUNTIME_KUBECONFIG)" )} \
	FAROS_APP_BASE_DOMAIN=$${FAROS_APP_BASE_DOMAIN:-apps.127.0.0.1.sslip.io} \
	FAROS_GATEWAY_NAME=$${FAROS_GATEWAY_NAME:-cloudflare-tunnel} \
	FAROS_GATEWAY_NAMESPACE=$${FAROS_GATEWAY_NAMESPACE:-cfgate-system} \
	FAROS_APP_PUBLIC_PORT=$${FAROS_APP_PUBLIC_PORT:-10443} \
	FAROS_PREVIEW_CONSOLE_VERIFICATION_JWKS="$$(cat "$(APP_STUDIO_PREVIEW_CONSOLE_DEV_JWKS)")" \
		$(BINDIR)/infrastructure-provider

run-provider-infrastructure-operator: build-infrastructure-provider app-studio-preview-console-dev-key ## Run the infrastructure provider in OPERATOR mode (bootstrap reconcile + serve from a provider + runtime kubeconfig)
	@echo "Starting infrastructure provider (operator) on :$(KROMC_PORT)"
	@echo "  hub:      $(KROMC_HUB_URL)"
	@echo "  provider: $${INFRASTRUCTURE_PROVIDER_KUBECONFIG:-$(KROMC_KCP_KUBECONFIG)} (kcp)"
	@echo "  runtime:  $${INFRASTRUCTURE_RUNTIME_KUBECONFIG:-$(KRO_KIND_KUBECONFIG)} (kro cluster)"
	@echo "  ws:       $(INFRASTRUCTURE_WORKSPACE_PATH)"
	@# The operator needs the provider workspace to already exist — run
	@# `make install-provider-infrastructure` (admin-portal onboarding in prod)
	@# first. It then reconciles the in-workspace bootstrap and seeds kro itself.
	PORT=$(KROMC_PORT) \
	FAROS_HUB_URL=$(KROMC_HUB_URL) \
	FAROS_HUB_TOKEN=$(KROMC_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=infrastructure \
	FAROS_DEV_ALLOW_TENANT_QUERY=true \
	INFRASTRUCTURE_WORKSPACE_PATH=$(INFRASTRUCTURE_WORKSPACE_PATH) \
	INFRASTRUCTURE_PROVIDER_KUBECONFIG=$${INFRASTRUCTURE_PROVIDER_KUBECONFIG:-$(KROMC_KCP_KUBECONFIG)} \
	INFRASTRUCTURE_RUNTIME_KUBECONFIG=$${INFRASTRUCTURE_RUNTIME_KUBECONFIG:-$$( [ -f "$(KRO_KIND_KUBECONFIG)" ] && echo "$(KRO_KIND_KUBECONFIG)" )} \
	FAROS_PREVIEW_CONSOLE_VERIFICATION_JWKS="$$(cat "$(APP_STUDIO_PREVIEW_CONSOLE_DEV_JWKS)")" \
		$(BINDIR)/infrastructure-provider operator

# ── CRD-driven operator (controller) dev flow ───────────────────────────────
# Replaces kro-mgmt-up + infrastructure-init: one host-binary controller that
# bootstraps the workspace, helm-installs kro (with the kind hostAliases +
# self-cluster patches), and (skip-serve in dev) leaves serve to the host binary.
INFRA_OPERATOR_NS ?= faros-infrastructure-operator
INFRA_OPERATOR_PROVIDER_KC ?= $(KROMC_KCP_KUBECONFIG)
INFRA_OPERATOR_RUNTIME_KC ?= $(KRO_KIND_KUBECONFIG)
INFRA_OPERATOR_KIND_NAME ?= $(KRO_KIND_NAME)
INFRA_OPERATOR_CRD ?= providers/infrastructure/config/crds/infrastructure.faros.sh_infrastructureproviders.yaml

run-provider-infrastructure-controller: build-infrastructure-provider app-studio-preview-console-dev-key ## Apply the operator CRD/Secrets/CR into the runtime cluster and run the controller (dev)
	@echo "Applying operator CRD + Secrets + CR into runtime cluster ($(INFRA_OPERATOR_RUNTIME_KC))"
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f $(INFRA_OPERATOR_CRD)
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl create namespace $(INFRA_OPERATOR_NS) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl -n $(INFRA_OPERATOR_NS) create secret generic provider-kubeconfig --from-file=kubeconfig=$(INFRA_OPERATOR_PROVIDER_KC) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl -n $(INFRA_OPERATOR_NS) create secret generic runtime-kubeconfig --from-file=kubeconfig=$(INFRA_OPERATOR_RUNTIME_KC) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	@printf 'apiVersion: infrastructure.faros.sh/v1alpha1\nkind: InfrastructureProvider\nmetadata:\n  name: infrastructure\n  namespace: %s\nspec:\n  providerWorkspace: %s\n  providerKubeconfigSecret:\n    name: provider-kubeconfig\n  runtimeKubeconfigSecret:\n    name: runtime-kubeconfig\n  kro:\n    chart: %s\n    version: %s\n  provider:\n    image:\n      repository: ghcr.io/faroshq/faros-infrastructure-provider\n      tag: dev\n' "$(INFRA_OPERATOR_NS)" "$(INFRASTRUCTURE_WORKSPACE_PATH)" "$(KRO_CHART)" "$(KRO_CHART_VERSION)" | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	@echo "Running infrastructure operator controller (KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC), skip-serve)"
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) \
	INFRASTRUCTURE_WORKSPACE_PATH=$(INFRASTRUCTURE_WORKSPACE_PATH) \
	INFRASTRUCTURE_OPERATOR_SKIP_SERVE=true \
	FAROS_PREVIEW_CONSOLE_VERIFICATION_JWKS="$$(cat "$(APP_STUDIO_PREVIEW_CONSOLE_DEV_JWKS)")" \
		$(BINDIR)/infrastructure-provider controller

## Run the App Studio provider binary locally. Mirrors the other external
## providers so the heartbeat path is consistent and the UI runs on :8085.
app-studio-preview-console-dev-key: ## Generate/reuse the local preview-console signing key and public JWKS under .kcp
	@node providers/app-studio/hack/preview-console-dev-keys.mjs \
		--output-dir "$(abspath $(APP_STUDIO_PREVIEW_CONSOLE_DEV_KEY_DIR))"

verify-app-studio-preview-console-dev-key: ## Verify local preview-console key generation, repair, and concurrency
	node --test providers/app-studio/hack/preview-console-dev-keys.test.mjs

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

run-provider-app-studio: build-app-studio-provider app-studio-db-up app-studio-preview-console-dev-key ## Run the App Studio provider (requires: make run-hub-embedded-static + make install-provider-app-studio)
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
	set -a; [ -f providers/app-studio/.env ] && . ./providers/app-studio/.env || true; set +a; \
	FAROS_ACTIONS_EXTERNAL_URL="$${FAROS_ACTIONS_EXTERNAL_URL:-$(FAROS_ACTIONS_EXTERNAL_URL)}"; \
	APP_STUDIO_DATABASE_URL="$${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DATABASE_URL)}"; \
	APP_STUDIO_IN_MEMORY_MESSAGE_STORE="$${APP_STUDIO_IN_MEMORY_MESSAGE_STORE:-$(APP_STUDIO_IN_MEMORY_MESSAGE_STORE)}"; \
	APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY="$$(cat "$(APP_STUDIO_PREVIEW_CONSOLE_DEV_PRIVATE_KEY)")"; \
	APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID="$$(cat "$(APP_STUDIO_PREVIEW_CONSOLE_DEV_KEY_ID)")"; \
	if [ "$${APP_STUDIO_IN_MEMORY_MESSAGE_STORE:-}" = "true" ]; then \
		echo "  store: in-memory (non-durable)"; \
		APP_STUDIO_DATABASE_URL= \
		PORT=$(APP_STUDIO_PORT) \
		FAROS_HUB_URL=$(APP_STUDIO_HUB_URL) \
		FAROS_HUB_TOKEN=$(APP_STUDIO_TOKEN) \
		FAROS_ACTIONS_EXTERNAL_URL="$${FAROS_ACTIONS_EXTERNAL_URL}" \
		FAROS_HUB_INSECURE=true \
		FAROS_PROVIDER_NAME=app-studio \
		FAROS_PROVIDER_KUBECONFIG=$${FAROS_PROVIDER_KUBECONFIG:-$(APP_STUDIO_PROVIDER_KUBECONFIG)} \
		APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true \
		APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY=true \
		APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY=true \
		APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY="$${APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY}" \
		APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID="$${APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID}" \
			$(BINDIR)/app-studio-provider; \
	else \
		echo "  store: $${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DEV_DATABASE_URL)}"; \
		PORT=$(APP_STUDIO_PORT) \
		FAROS_HUB_URL=$(APP_STUDIO_HUB_URL) \
		FAROS_HUB_TOKEN=$(APP_STUDIO_TOKEN) \
		FAROS_ACTIONS_EXTERNAL_URL="$${FAROS_ACTIONS_EXTERNAL_URL}" \
		FAROS_HUB_INSECURE=true \
		FAROS_PROVIDER_NAME=app-studio \
		FAROS_PROVIDER_KUBECONFIG=$${FAROS_PROVIDER_KUBECONFIG:-$(APP_STUDIO_PROVIDER_KUBECONFIG)} \
		APP_STUDIO_DATABASE_URL="$${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DEV_DATABASE_URL)}" \
		APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY=true \
		APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY=true \
		APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY="$${APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY}" \
		APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID="$${APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID}" \
			$(BINDIR)/app-studio-provider; \
	fi

## Apply the App Studio CatalogEntry into root:faros:providers. Idempotent.
install-provider-app-studio: ## Apply App Studio Provider + CatalogEntry into root:faros:providers
	@test -f $(APP_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(APP_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(APP_STUDIO_PROVIDER_MANIFEST) -f $(APP_STUDIO_MANIFEST)

## Create App Studio's APIExport (+ schemas + endpoint slice + bind grant) in
## its provider workspace so tenants can Enable it. Reads the provider-token the
## Provider controller minted on register, writes a dev provider kubeconfig, and
## runs the provider's `init` (sdkinstall.Bootstrap) with the shipped schemas.
## FAROS_CATALOGENTRY_FILE is intentionally unset — the dev install target
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
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: faros\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: faros\n  context:\n    cluster: faros\n    user: faros\ncurrent-context: faros\nusers:\n- name: faros\n  user:\n    token: %s\n' \
		"$(APP_STUDIO_KCP_SERVER)/clusters/$(APP_STUDIO_WORKSPACE_PATH)" "$$TOKEN" \
		> $(APP_STUDIO_PROVIDER_KUBECONFIG)
	@echo "Running app-studio-provider init (creates APIExport + schemas + endpoint slice + bind grant + instance claims)"
	@# The instance permission claims need the infrastructure APIExport's
	@# identityHash. Auto-discover it in dev; APP_STUDIO_INFRA_IDENTITY_HASH
	@# in the environment wins (prod supplies it via Helm).
	INFRA_HASH=$${APP_STUDIO_INFRA_IDENTITY_HASH:-$$(kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/root:faros:providers:infrastructure \
		--insecure-skip-tls-verify \
		get apiexport infrastructure.providers.faros.sh -o jsonpath='{.status.identityHash}' 2>/dev/null)}; \
	if [ -z "$$INFRA_HASH" ]; then echo "WARNING: could not discover the infrastructure APIExport identityHash — instance claims will be hash-less"; fi; \
	CODE_HASH=$${APP_STUDIO_CODE_IDENTITY_HASH:-$$(kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/root:faros:providers:code \
		--insecure-skip-tls-verify \
		get apiexport code.providers.faros.sh -o jsonpath='{.status.identityHash}' 2>/dev/null)}; \
	if [ -z "$$CODE_HASH" ]; then echo "WARNING: could not discover the code APIExport identityHash — the repositories claim will be hash-less"; fi; \
	FAROS_PROVIDER_KUBECONFIG=$(APP_STUDIO_PROVIDER_KUBECONFIG) \
	APP_STUDIO_WORKSPACE_PATH=$(APP_STUDIO_WORKSPACE_PATH) \
	FAROS_SCHEMAS_DIR=$(APP_STUDIO_SCHEMAS_DIR) \
	APP_STUDIO_INFRA_IDENTITY_HASH="$$INFRA_HASH" \
	APP_STUDIO_CODE_IDENTITY_HASH="$$CODE_HASH" \
		$(BINDIR)/app-studio-provider init

## Delete the App Studio CatalogEntry. Useful while iterating on the chart.
uninstall-provider-app-studio: ## Delete App Studio CatalogEntry
	@test -f $(APP_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(APP_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	-kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/root:faros:system:providers \
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
	FAROS_HUB_URL=$(AGENTS_HUB_URL) \
	FAROS_HUB_TOKEN=$(AGENTS_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=agents \
	FAROS_PROVIDER_KUBECONFIG=$${FAROS_PROVIDER_KUBECONFIG:-$$( for f in "$(AGENTS_PROVIDER_KUBECONFIG)" "$(AGENTS_KCP_KUBECONFIG)" "$(CURDIR)/tilt-frontproxy.kubeconfig"; do [ -f "$$f" ] && echo "$$f" && break; done )} \
	AGENTS_DATABASE_URL="$$AGENTS_DATABASE_URL" \
	AGENTS_IN_MEMORY_STORE="$$AGENTS_IN_MEMORY_STORE" \
		$(BINDIR)/agents-provider

install-provider-agents: ## Apply agents Provider + CatalogEntry into root:faros:providers
	@test -f $(AGENTS_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(AGENTS_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(AGENTS_KCP_KUBECONFIG) \
		--server=$(AGENTS_KCP_SERVER)/clusters/root:faros:system:providers \
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
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: faros\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: faros\n  context:\n    cluster: faros\n    user: faros\ncurrent-context: faros\nusers:\n- name: faros\n  user:\n    token: %s\n' \
		"$(AGENTS_KCP_SERVER)/clusters/$(AGENTS_WORKSPACE_PATH)" "$$TOKEN" \
		> $(AGENTS_PROVIDER_KUBECONFIG)
	@echo "Running agents-provider init (creates APIExport + schemas + endpoint slice + bind grant)"
	FAROS_PROVIDER_KUBECONFIG=$(AGENTS_PROVIDER_KUBECONFIG) \
	AGENTS_WORKSPACE_PATH=$(AGENTS_WORKSPACE_PATH) \
	FAROS_SCHEMAS_DIR=$(AGENTS_SCHEMAS_DIR) \
		$(BINDIR)/agents-provider init

uninstall-provider-agents: ## Delete the agents CatalogEntry + Provider
	@test -f $(AGENTS_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(AGENTS_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	-kubectl --kubeconfig=$(AGENTS_KCP_KUBECONFIG) \
		--server=$(AGENTS_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(AGENTS_MANIFEST) -f $(AGENTS_PROVIDER_MANIFEST)

## --- vibe-studio provider dev targets (mirror agents) ---
# Wizard-first app builder (docs/vibe-studio-design.md). Phase 0: event store
# + scripted engine; no LLM, no runtime kubeconfig at serve time.

VIBE_STUDIO_PORT ?= 8089
VIBE_STUDIO_HUB_URL ?= https://localhost:9443
VIBE_STUDIO_TOKEN ?= $(STATIC_AUTH_TOKEN)
VIBE_STUDIO_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
VIBE_STUDIO_KCP_SERVER ?= https://localhost:6443
VIBE_STUDIO_WORKSPACE_PATH ?= root:faros:providers:vibe-studio
VIBE_STUDIO_PROVIDER_KUBECONFIG ?= $(KCP_DATA_DIR)/vibe-studio-provider.kubeconfig
VIBE_STUDIO_SCHEMAS_DIR ?= providers/vibe-studio/deploy/chart/files/schemas
VIBE_STUDIO_MANIFEST ?= providers/vibe-studio/manifest.yaml
VIBE_STUDIO_PROVIDER_MANIFEST ?= providers/vibe-studio/provider.yaml
# Durable store: dev runs use a local Postgres container by default (mirrors
# agents). Set VIBE_STUDIO_IN_MEMORY_STORE=true for a non-durable quick run.
VIBE_STUDIO_IN_MEMORY_STORE ?=
VIBE_STUDIO_DEV_DATABASE_URL ?= postgres://vibestudio:vibestudio@localhost:55435/vibestudio?sslmode=disable
VIBE_STUDIO_POSTGRES_CONTAINER ?= faros-vibe-studio-postgres
VIBE_STUDIO_POSTGRES_IMAGE ?= mirror.gcr.io/library/postgres:16-alpine
VIBE_STUDIO_POSTGRES_PORT ?= 55435
VIBE_STUDIO_POSTGRES_DATA_DIR ?= $(KCP_DATA_DIR)/vibe-studio-postgres

## Start/reuse a local Postgres container for the vibe-studio event store.
## Skips when VIBE_STUDIO_IN_MEMORY_STORE=true or VIBE_STUDIO_DATABASE_URL
## points elsewhere.
vibe-studio-db-up:
	@set -a; [ -f providers/vibe-studio/.env ] && . ./providers/vibe-studio/.env || true; set +a; \
	if [ "$${VIBE_STUDIO_IN_MEMORY_STORE:-$(VIBE_STUDIO_IN_MEMORY_STORE)}" = "true" ]; then \
		echo "vibe-studio: in-memory store — skipping Postgres"; exit 0; fi; \
	if [ -n "$${VIBE_STUDIO_DATABASE_URL:-}" ]; then \
		echo "vibe-studio: external VIBE_STUDIO_DATABASE_URL set — skipping dev Postgres"; exit 0; fi; \
	if docker ps --format '{{.Names}}' | grep -q '^$(VIBE_STUDIO_POSTGRES_CONTAINER)$$'; then \
		echo "vibe-studio postgres already running on :$(VIBE_STUDIO_POSTGRES_PORT)"; exit 0; fi; \
	if docker ps -a --format '{{.Names}}' | grep -q '^$(VIBE_STUDIO_POSTGRES_CONTAINER)$$'; then \
		docker start $(VIBE_STUDIO_POSTGRES_CONTAINER); exit 0; fi; \
	mkdir -p $(VIBE_STUDIO_POSTGRES_DATA_DIR); \
	docker run -d --name $(VIBE_STUDIO_POSTGRES_CONTAINER) \
		-e POSTGRES_USER=vibestudio -e POSTGRES_PASSWORD=vibestudio -e POSTGRES_DB=vibestudio \
		-p $(VIBE_STUDIO_POSTGRES_PORT):5432 \
		-v "$(abspath $(VIBE_STUDIO_POSTGRES_DATA_DIR)):/var/lib/postgresql/data" \
		$(VIBE_STUDIO_POSTGRES_IMAGE)

vibe-studio-db-down: ## Stop and remove the vibe-studio dev Postgres container
	-docker rm -f $(VIBE_STUDIO_POSTGRES_CONTAINER)

run-provider-vibe-studio: build-vibe-studio-provider vibe-studio-db-up ## Run the vibe-studio provider (requires: make run-hub-embedded-static + make install-provider-vibe-studio + make init-provider-vibe-studio)
	@echo "Starting vibe-studio provider on :$(VIBE_STUDIO_PORT)"
	@echo "  hub:   $(VIBE_STUDIO_HUB_URL)"
	@# Keep the provider-scoped kubeconfig path stable across init ordering. The
	@# controller retry loop tolerates the file being absent until init writes it;
	@# selecting an existing fallback would pin this process to stale credentials.
	@# Auto-source providers/vibe-studio/.env (gitignored) for local overrides.
	set -a; [ -f providers/vibe-studio/.env ] && . ./providers/vibe-studio/.env || true; set +a; \
	VIBE_STUDIO_IN_MEMORY_STORE="$${VIBE_STUDIO_IN_MEMORY_STORE:-$(VIBE_STUDIO_IN_MEMORY_STORE)}"; \
	if [ "$$VIBE_STUDIO_IN_MEMORY_STORE" = "true" ]; then \
		echo "  store: in-memory (non-durable)"; VIBE_STUDIO_DATABASE_URL=; \
	else \
		VIBE_STUDIO_DATABASE_URL="$${VIBE_STUDIO_DATABASE_URL:-$(VIBE_STUDIO_DEV_DATABASE_URL)}"; \
		echo "  store: $$VIBE_STUDIO_DATABASE_URL"; \
	fi; \
	PORT=$(VIBE_STUDIO_PORT) \
	FAROS_HUB_URL=$(VIBE_STUDIO_HUB_URL) \
	FAROS_HUB_TOKEN=$(VIBE_STUDIO_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=vibe-studio \
	FAROS_PROVIDER_KUBECONFIG=$${FAROS_PROVIDER_KUBECONFIG:-$(VIBE_STUDIO_PROVIDER_KUBECONFIG)} \
	VIBE_STUDIO_DATABASE_URL="$$VIBE_STUDIO_DATABASE_URL" \
		$(BINDIR)/vibe-studio-provider

install-provider-vibe-studio: ## Apply vibe-studio Provider + CatalogEntry into root:faros:providers
	@test -f $(VIBE_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(VIBE_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(VIBE_STUDIO_KCP_KUBECONFIG) \
		--server=$(VIBE_STUDIO_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(VIBE_STUDIO_PROVIDER_MANIFEST) -f $(VIBE_STUDIO_MANIFEST)

init-provider-vibe-studio: build-vibe-studio-provider ## Bootstrap vibe-studio APIExport + write dev provider kubeconfig
	@test -f $(VIBE_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(VIBE_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Reading provider-token from $(VIBE_STUDIO_WORKSPACE_PATH) and writing $(VIBE_STUDIO_PROVIDER_KUBECONFIG)"
	@TOKEN=$$(kubectl --kubeconfig=$(VIBE_STUDIO_KCP_KUBECONFIG) \
		--server=$(VIBE_STUDIO_KCP_SERVER)/clusters/$(VIBE_STUDIO_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: faros\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: faros\n  context:\n    cluster: faros\n    user: faros\ncurrent-context: faros\nusers:\n- name: faros\n  user:\n    token: %s\n' \
		"$(VIBE_STUDIO_KCP_SERVER)/clusters/$(VIBE_STUDIO_WORKSPACE_PATH)" "$$TOKEN" \
		> $(VIBE_STUDIO_PROVIDER_KUBECONFIG)
	@echo "Running vibe-studio-provider init (creates APIExport + schemas + endpoint slice + bind grant + instance claims)"
	@# The instance permission claims need the infrastructure APIExport's
	@# identityHash. Auto-discover it in dev; VIBE_STUDIO_INFRA_IDENTITY_HASH
	@# in the environment wins (prod supplies it via Helm).
	INFRA_HASH=$${VIBE_STUDIO_INFRA_IDENTITY_HASH:-$$(kubectl --kubeconfig=$(VIBE_STUDIO_KCP_KUBECONFIG) \
		--server=$(VIBE_STUDIO_KCP_SERVER)/clusters/root:faros:providers:infrastructure \
		--insecure-skip-tls-verify \
		get apiexport infrastructure.providers.faros.sh -o jsonpath='{.status.identityHash}' 2>/dev/null)}; \
	if [ -z "$$INFRA_HASH" ]; then echo "WARNING: could not discover the infrastructure APIExport identityHash — instance claims will be hash-less"; fi; \
	CODE_HASH=$${VIBE_STUDIO_CODE_IDENTITY_HASH:-$$(kubectl --kubeconfig=$(VIBE_STUDIO_KCP_KUBECONFIG) \
		--server=$(VIBE_STUDIO_KCP_SERVER)/clusters/root:faros:providers:code \
		--insecure-skip-tls-verify \
		get apiexport code.providers.faros.sh -o jsonpath='{.status.identityHash}' 2>/dev/null)}; \
	if [ -z "$$CODE_HASH" ]; then echo "WARNING: could not discover the code APIExport identityHash — the repositories claim will be hash-less"; fi; \
	FAROS_PROVIDER_KUBECONFIG=$(VIBE_STUDIO_PROVIDER_KUBECONFIG) \
	VIBE_STUDIO_WORKSPACE_PATH=$(VIBE_STUDIO_WORKSPACE_PATH) \
	FAROS_SCHEMAS_DIR=$(VIBE_STUDIO_SCHEMAS_DIR) \
	VIBE_STUDIO_INFRA_IDENTITY_HASH="$$INFRA_HASH" \
	VIBE_STUDIO_CODE_IDENTITY_HASH="$$CODE_HASH" \
		$(BINDIR)/vibe-studio-provider init

uninstall-provider-vibe-studio: ## Delete the vibe-studio CatalogEntry + Provider
	@test -f $(VIBE_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(VIBE_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	-kubectl --kubeconfig=$(VIBE_STUDIO_KCP_KUBECONFIG) \
		--server=$(VIBE_STUDIO_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(VIBE_STUDIO_MANIFEST) -f $(VIBE_STUDIO_PROVIDER_MANIFEST)

# --- Dev agent image (template-native development mode) ---
# The static control binary an init container injects into any dev-mode
# component (docs/app-studio-template-sandboxes.md §2). Scratch image with a
# stdlib-only module; application dependencies are installed by each component
# from its declared package manifest, not projected by this image.
DEV_AGENT_DIR ?= providers/infrastructure/dev-agent
DEV_AGENT_IMAGE ?= ghcr.io/faroshq/faros-dev-agent:latest
DEV_AGENT_PLATFORM ?= linux/$(ARCH)

docker-build-dev-agent: ## Build the faros-dev-agent injector image used by dev-mode components
	docker build -f $(DEV_AGENT_DIR)/Dockerfile \
		--platform $(DEV_AGENT_PLATFORM) \
		--provenance=false \
		-t $(DEV_AGENT_IMAGE) $(DEV_AGENT_DIR)

load-dev-agent-image: docker-build-dev-agent ## Load the dev agent image into the local kind runtime cluster
	@echo ">>> loading $(DEV_AGENT_IMAGE) into kind cluster $(KRO_KIND_NAME)"
	kind load docker-image $(DEV_AGENT_IMAGE) --name $(KRO_KIND_NAME)

## Apply the infrastructure CatalogEntry into root:faros:providers. Idempotent.
## Requires the hub to be running so the admin kubeconfig exists.
install-provider-infrastructure: ## Apply infrastructure Provider + CatalogEntry into root:faros:providers
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		apply --validate=false -f $(KROMC_PROVIDER_MANIFEST) -f $(KROMC_MANIFEST)

## Delete the infrastructure CatalogEntry + Provider (Provider delete triggers
## full teardown of the sub-workspace via the controller's finalizer).
uninstall-provider-infrastructure: ## Delete infrastructure CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(KROMC_MANIFEST) -f $(KROMC_PROVIDER_MANIFEST)

## One-shot bootstrap for the infrastructure provider's workspace.
## Uses the hub's admin kubeconfig to install CRDs, register APIExport
## schemas, apply the Templates CachedResource, mint a low-privilege
## ServiceAccount + token, and write a runtime kubeconfig that
## run-provider-infrastructure picks up via INFRASTRUCTURE_KUBECONFIG.
##
## When KRO_KUBECONFIG is set, also seeds the kro cluster with a
## kro.run/cluster=true Secret pointing at this workspace's VW.
INFRASTRUCTURE_WORKSPACE_PATH ?= root:faros:providers:infrastructure
INFRASTRUCTURE_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/infrastructure-runtime.kubeconfig
init-provider-infrastructure: build-infrastructure-provider ## Bootstrap infrastructure provider workspace (CRDs, APIExport, SA, kubeconfig)
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Bootstrapping infrastructure provider workspace $(INFRASTRUCTURE_WORKSPACE_PATH)"
	@echo "  admin:   $(KROMC_KCP_KUBECONFIG)"
	@echo "  runtime: $(INFRASTRUCTURE_RUNTIME_KUBECONFIG)"
	INFRASTRUCTURE_ADMIN_KUBECONFIG=$(KROMC_KCP_KUBECONFIG) \
	INFRASTRUCTURE_WORKSPACE_PATH=$(INFRASTRUCTURE_WORKSPACE_PATH) \
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

run-provider-code: build-code-provider ## Run the code provider (requires: make run-hub-embedded-static + make install-provider-code)
	@echo "Starting code provider on :$(CODE_PORT) (hub $(KROMC_HUB_URL))"
	@# Auto-source providers/code/.env (gitignored) so GitHub OAuth + other dev
	@# env reach the provider without a manual export. See .env.example.
	set -a; [ -f providers/code/.env ] && . ./providers/code/.env || true; set +a; \
	PORT=$(CODE_PORT) \
	FAROS_HUB_URL=$(KROMC_HUB_URL) \
	FAROS_HUB_TOKEN=$(KROMC_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=code \
	FAROS_DEV_ALLOW_TENANT_QUERY=true \
	CODE_COMMIT_BUNDLE_DIR=$${CODE_COMMIT_BUNDLE_DIR:-$(KCP_DATA_DIR)/code-commit-bundles} \
	FAROS_PROVIDER_KUBECONFIG=$${FAROS_PROVIDER_KUBECONFIG:-$$( [ -f "$(CODE_RUNTIME_KUBECONFIG)" ] && echo "$(CODE_RUNTIME_KUBECONFIG)" )} \
	GITHUB_OAUTH_CLIENT_ID=$${GITHUB_OAUTH_CLIENT_ID:-} \
	GITHUB_OAUTH_CLIENT_SECRET=$${GITHUB_OAUTH_CLIENT_SECRET:-} \
	GITHUB_OAUTH_REDIRECT_URL=$${GITHUB_OAUTH_REDIRECT_URL:-http://localhost:$(CODE_PORT)/oauth/github/callback} \
	GITHUB_OAUTH_PORTAL_ORIGIN=$${GITHUB_OAUTH_PORTAL_ORIGIN:-$(KROMC_HUB_URL)} \
		$(BINDIR)/code-provider serve

install-provider-code: ## Apply the code Provider + CatalogEntry into root:faros:providers
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(CODE_PROVIDER_MANIFEST) -f $(CODE_MANIFEST)

uninstall-provider-code: ## Delete the code CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(CODE_MANIFEST) -f $(CODE_PROVIDER_MANIFEST)

CODE_WORKSPACE_PATH ?= root:faros:providers:code
## Dev bootstrap for the code provider. The hub mints a real provider
## kubeconfig only when it runs with a host cluster (--kubeconfig); the dev hubs
## (embedded + Tiltfile.cluster) do not, so we derive a runtime kubeconfig from
## the admin kubeconfig — reusing its working credential (a static token in
## embedded mode, a client cert in cluster mode) and retargeting only the server
## URL to the provider workspace — and ensure the APIExportEndpointSlice the
## controller manager needs. run-provider-code reads it via FAROS_PROVIDER_KUBECONFIG.
## Order: install-provider-code (creates the workspace) → init-provider-code →
## run-provider-code. Re-runnable. The Tiltfile.cluster flow reuses this target
## verbatim, overriding KROMC_KCP_KUBECONFIG / KROMC_KCP_SERVER.
init-provider-code: build-code-provider ## Write the dev kubeconfig + ensure the code APIExportEndpointSlice
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@mkdir -p $(KCP_DATA_DIR)
	@# The provider workspace (root:faros:providers:code) is created
	@# declaratively by the Provider controller when code-register applies the
	@# Provider CR — no need to create it here.
	@echo "Writing dev kubeconfig $(CODE_RUNTIME_KUBECONFIG) (workspace $(CODE_WORKSPACE_PATH), server $(KROMC_KCP_SERVER))"
	@kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) config view --minify --flatten > $(CODE_RUNTIME_KUBECONFIG)
	@CL=$$(kubectl --kubeconfig=$(CODE_RUNTIME_KUBECONFIG) config view -o jsonpath='{.clusters[0].name}'); \
		kubectl --kubeconfig=$(CODE_RUNTIME_KUBECONFIG) config set-cluster "$$CL" \
			--server=$(KROMC_KCP_SERVER)/clusters/$(CODE_WORKSPACE_PATH) \
			--insecure-skip-tls-verify=true >/dev/null
	FAROS_PROVIDER_KUBECONFIG=$(CODE_RUNTIME_KUBECONFIG) \
	CODE_WORKSPACE_PATH=$(CODE_WORKSPACE_PATH) \
	FAROS_SCHEMAS_DIR=$(CURDIR)/providers/code/deploy/chart/files/schemas \
		$(BINDIR)/code-provider init

# --- Provider Databricks (local dev) ---
DATABRICKS_PORT ?= 8086
DATABRICKS_MANIFEST ?= providers/databricks/manifest.yaml
DATABRICKS_PROVIDER_MANIFEST ?= providers/databricks/provider.yaml
DATABRICKS_WORKSPACE_PATH ?= root:faros:providers:databricks
DATABRICKS_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/databricks-runtime.kubeconfig

run-provider-databricks: build-databricks-provider ## Run the Databricks provider (requires: make run-hub-embedded-static + make install-provider-databricks)
	@echo "Starting Databricks provider on :$(DATABRICKS_PORT) (hub $(KROMC_HUB_URL))"
	PORT=$(DATABRICKS_PORT) \
	FAROS_HUB_URL=$(KROMC_HUB_URL) \
	FAROS_HUB_TOKEN=$(KROMC_TOKEN) \
	FAROS_HUB_INSECURE=true \
	FAROS_PROVIDER_NAME=databricks \
	DATABRICKS_MCP_DISABLE_LOCALHOST_PROTECTION=true \
	FAROS_PROVIDER_KUBECONFIG=$$( [ -f "$(DATABRICKS_RUNTIME_KUBECONFIG)" ] && echo "$(DATABRICKS_RUNTIME_KUBECONFIG)" ) \
		$(BINDIR)/databricks-provider serve

install-provider-databricks: ## Apply the Databricks Provider + CatalogEntry into root:faros:providers
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(DATABRICKS_PROVIDER_MANIFEST) -f $(DATABRICKS_MANIFEST)

uninstall-provider-databricks: ## Delete the Databricks CatalogEntry + Provider
	-kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:faros:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(DATABRICKS_MANIFEST) -f $(DATABRICKS_PROVIDER_MANIFEST)

init-provider-databricks: build-databricks-provider ## Bootstrap Databricks APIExport + write dev provider kubeconfig
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@mkdir -p $(KCP_DATA_DIR)
	@echo "Writing dev kubeconfig $(DATABRICKS_RUNTIME_KUBECONFIG) (workspace $(DATABRICKS_WORKSPACE_PATH), server $(KROMC_KCP_SERVER))"
	@kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) config view --minify --flatten > $(DATABRICKS_RUNTIME_KUBECONFIG)
	@CL=$$(kubectl --kubeconfig=$(DATABRICKS_RUNTIME_KUBECONFIG) config view -o jsonpath='{.clusters[0].name}'); \
		kubectl --kubeconfig=$(DATABRICKS_RUNTIME_KUBECONFIG) config set-cluster "$$CL" \
			--server=$(KROMC_KCP_SERVER)/clusters/$(DATABRICKS_WORKSPACE_PATH) \
			--insecure-skip-tls-verify=true >/dev/null
	FAROS_PROVIDER_KUBECONFIG=$(DATABRICKS_RUNTIME_KUBECONFIG) \
	DATABRICKS_WORKSPACE_PATH=$(DATABRICKS_WORKSPACE_PATH) \
	FAROS_SCHEMAS_DIR=$(CURDIR)/providers/databricks/deploy/chart/files/schemas \
		$(BINDIR)/databricks-provider init

# --- Experimental: run the infrastructure provider as a POD (init-container
#     bootstrap) instead of a host binary. Exercises the full hub-minted
#     flow: CatalogEntry -> hub mints + delivers faros-provider-kubeconfig
#     (HostSecretWriter) -> init container bootstraps with it -> serve runs.
#     Reuses the faros-kro kind cluster as the host cluster. Requires the hub
#     to run with --kubeconfig=$(KRO_KIND_KUBECONFIG) and
#     --hub-internal-url=$(HUB_INTERNAL_URL) (the Tiltfile sets
#     both). Apply the CatalogEntry first: make install-provider-infrastructure
INFRASTRUCTURE_NAMESPACE ?= infrastructure
INFRASTRUCTURE_IMAGE ?= faros-infrastructure-provider:dev
INFRASTRUCTURE_CHART ?= providers/infrastructure/deploy/chart
# Address provider pods in the kind cluster use to reach the hub front-proxy
# (browsers use https://localhost:9443; host.docker.internal resolves to the
# host from inside kind on Docker Desktop / Colima / OrbStack).
HUB_INTERNAL_URL ?= https://host.docker.internal:9443
helm-deploy-provider-infrastructure: ## (experimental) Build+load image, helm install the provider as a pod into faros-kro (hub-minted bootstrap)
	@command -v kind >/dev/null || { echo "kind not found; brew install kind"; exit 1; }
	@test -f $(KRO_KIND_KUBECONFIG) || { echo "faros-kro cluster missing; run 'make dev-kro-up' first"; exit 1; }
	@echo ">>> building $(INFRASTRUCTURE_IMAGE)"
	docker build -t $(INFRASTRUCTURE_IMAGE) providers/infrastructure
	@echo ">>> loading image into kind cluster $(KRO_KIND_NAME)"
	kind load docker-image $(INFRASTRUCTURE_IMAGE) --name $(KRO_KIND_NAME)
	@echo ">>> ensuring namespace + heartbeat token Secret in $(INFRASTRUCTURE_NAMESPACE)"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl create namespace $(INFRASTRUCTURE_NAMESPACE) \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(INFRASTRUCTURE_NAMESPACE) create secret generic faros-infrastructure-hub-token \
		--from-literal=token=$(STATIC_AUTH_TOKEN) \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	@echo ">>> helm install (bootstrap.enabled=true, kubeconfigSource=hubMinted)"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) helm upgrade --install infrastructure $(INFRASTRUCTURE_CHART) \
		--namespace $(INFRASTRUCTURE_NAMESPACE) \
		--set image.repository=faros-infrastructure-provider \
		--set image.tag=dev \
		--set image.pullPolicy=Never \
		--set replicaCount=1 \
		--set bootstrap.enabled=true \
		--set hub.url=$(HUB_INTERNAL_URL) \
		--set hub.insecure=true \
		--set catalogEntry.enabled=false
	@echo ">>> deployed. The pod stays in ContainerCreating until the hub delivers"
	@echo "    the faros-provider-kubeconfig Secret (apply the CatalogEntry first:"
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
# the retired faroshq/kro-multicluster fork is no longer used.
#
# The faros infrastructure provider points at this cluster via
# KRO_KUBECONFIG so provisioning materializes real Deployments /
# Services.
#
KRO_KIND_NAME ?= faros-kro
KRO_KIND_KUBECONFIG ?= $(CURDIR)/.faros-kro.kubeconfig
KRO_CHART ?= oci://registry.k8s.io/kro/charts/kro
KRO_CHART_VERSION ?= 0.9.3
KRO_NAMESPACE ?= kro-system
KRO_SEED_DIR ?= providers/infrastructure/examples/rgds

# --- Infrastructure template e2e (RGD acceptance against a real kro) ---
# A throwaway kind cluster running STANDALONE kro (no kcp) — enough to validate
# that every seeded Template authors a kro graph kro accepts. See
# providers/infrastructure/backend/kro/e2e_test.go.
E2E_KRO_KIND_NAME ?= faros-kro-e2e
E2E_KRO_KUBECONFIG ?= $(CURDIR)/.faros-kro-e2e.kubeconfig
GATEWAY_API_VERSION ?= v1.2.1

## Bring up the management kro cluster + install upstream kro. Idempotent:
## re-running just helm-upgrades the chart (with an explicit CRD apply —
## helm never upgrades crds/-dir CRDs).
dev-kro-up: ## Bring up the faros-kro kind cluster + install upstream kro
	@command -v kind >/dev/null || { echo "kind not found; install: brew install kind"; exit 1; }
	@command -v helm >/dev/null || { echo "helm not found; install: brew install helm"; exit 1; }
	@if ! kind get clusters | grep -qx "$(KRO_KIND_NAME)"; then \
		echo ">>> creating kind cluster $(KRO_KIND_NAME)"; \
		kind create cluster --name $(KRO_KIND_NAME) --kubeconfig $(KRO_KIND_KUBECONFIG); \
	else \
		echo ">>> kind cluster $(KRO_KIND_NAME) already exists"; \
		kind get kubeconfig --name $(KRO_KIND_NAME) > $(KRO_KIND_KUBECONFIG); \
	fi
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
dev-kro-down: ## Delete the faros-kro kind cluster + kubeconfig file
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
	@echo "=== Faros Hub Development Modes ==="
	@echo ""
	@echo "STANDALONE (no external dependencies):"
	@echo "  make run-hub-standalone         - Embedded kcp + static token + embedded GraphQL"
	@echo "                                    Just run this and use: make dev-login-static"
	@echo "  make run-hub-embedded-static    - Embedded kcp + static token (no GraphQL)"
	@echo ""
	@echo "WITH DEX (OIDC authentication):"
	@echo "  Terminal 1: make run-dex"
	@echo "  Terminal 2: make run-hub-embedded-graphql - Embedded kcp + OIDC + embedded GraphQL"
	@echo "              make run-hub-embedded          - Embedded kcp + OIDC (no GraphQL)"
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
	@echo "  Then open https://localhost:9443/ui/providers (Enable the provider)"
	@echo ""
	@echo "APP STUDIO PROVIDER (after the hub is running):"
	@echo "  Terminal A: make install-provider-app-studio   - Apply CatalogEntry"
	@echo "  Terminal B: make run-provider-app-studio       - Run the provider"
	@echo "  Then open https://localhost:9443/ui/providers (Enable the provider)"
	@echo ""
	@echo "ENVIRONMENT VARIABLES:"
	@echo "  STATIC_AUTH_TOKEN  - Token for static auth (default: dev-token)"
	@echo "  KCP_DATA_DIR       - Directory for kcp data (default: .kcp)"
	@echo "  QUICKSTART_PORT    - Port the quickstart provider listens on (default: 8081)"
	@echo "  QUICKSTART_HUB_URL - Hub URL the provider heartbeats to (default: https://localhost:9443)"
	@echo "  APP_STUDIO_PORT    - Port the App Studio provider listens on (default: 8085)"
	@echo "  APP_STUDIO_HUB_URL - Hub URL the provider heartbeats to (default: https://localhost:9443)"
	@echo "  APP_STUDIO_DEV_DATABASE_URL - Local App Studio Postgres DSN (default: postgres://appstudio:appstudio@localhost:55432/appstudio?sslmode=disable)"
	@echo "  APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true - Force non-durable App Studio message store"
	@echo ""

DOCKER_PLATFORM ?= linux/amd64

docker-build: docker-build-hub docker-build-agent ## Build all container images

docker-build-hub: ## Build faros-hub container image
	docker build -f deploy/Dockerfile.hub \
		--platform $(DOCKER_PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t ghcr.io/faroshq/faros-hub:$(VERSION) .

docker-build-agent: ## Build faros-agent container image
	docker build -f deploy/Dockerfile.agent \
		--platform $(DOCKER_PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t ghcr.io/faroshq/faros-agent:$(VERSION) .

docker-build-access-proxy: ## Build published-app access-proxy container image (infrastructure module)
	docker build -f providers/infrastructure/Dockerfile.access-proxy \
		--platform $(DOCKER_PLATFORM) \
		-t ghcr.io/faroshq/faros-access-proxy:$(VERSION) providers/infrastructure

docker-push-hub: docker-build-hub ## Build and push faros-hub container image
	docker push ghcr.io/faroshq/faros-hub:$(VERSION)

docker-push-agent: docker-build-agent ## Build and push faros-agent container image
	docker push ghcr.io/faroshq/faros-agent:$(VERSION)

docker-build-dex: ## Build faros-dex container image (custom dex with branded web overlay)
	cd hack/dex && docker build \
		--platform $(DOCKER_PLATFORM) \
		-f Dockerfile \
		-t ghcr.io/faroshq/faros-dex:$(VERSION) .

docker-push-dex: docker-build-dex ## Build and push faros-dex container image
	docker push ghcr.io/faroshq/faros-dex:$(VERSION)

docker-push: docker-push-hub docker-push-agent ## Build and push all container images

clean:
	rm -rf $(BINDIR)
	rm -rf $(TOOLSDIR)
	rm -rf tmp
	-kind delete cluster --name faros-agent 2>/dev/null

path: ## Print export command to add bin/ to PATH
	@echo 'export PATH=$(CURDIR)/$(BINDIR):$$PATH'

verify: verify-boilerplate verify-codegen verify-portalkit verify-app-studio-preview-console-dev-key verify-app-studio-eval vet lint build test ## Run all checks

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
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/faros-hub:test .
	docker build -f deploy/Dockerfile.agent -t ghcr.io/faroshq/faros-agent:test .
	FAROS_HUB_IMAGE=ghcr.io/faroshq/faros-hub \
	FAROS_HUB_IMAGE_TAG=test \
	FAROS_HUB_IMAGE_PULL_POLICY=Never \
	FAROS_AGENT_IMAGE=ghcr.io/faroshq/faros-agent \
	FAROS_AGENT_IMAGE_TAG=test \
	FAROS_AGENT_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/standalone/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-ssh: build ## Run SSH server-mode e2e suite (hub-only cluster)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/faros-hub:test .
	FAROS_HUB_IMAGE=ghcr.io/faroshq/faros-hub \
	FAROS_HUB_IMAGE_TAG=test \
	FAROS_HUB_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/ssh/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-oidc: build ## Run OIDC e2e suite (Dex OIDC provider, requires --with-dex cluster)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/faros-hub:test .
	FAROS_HUB_IMAGE=ghcr.io/faroshq/faros-hub \
	FAROS_HUB_IMAGE_TAG=test \
	FAROS_HUB_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/oidc/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-external-kcp: build ## Run external KCP e2e suite (kcp via Helm in kind, push-to-main only in CI)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/faros-hub:test .
	FAROS_HUB_IMAGE=ghcr.io/faroshq/faros-hub \
	FAROS_HUB_IMAGE_TAG=test \
	FAROS_HUB_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/external_kcp/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-all: build ## Run all e2e suites
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/faros-hub:test .
	docker build -f deploy/Dockerfile.agent -t ghcr.io/faroshq/faros-agent:test .
	FAROS_HUB_IMAGE=ghcr.io/faroshq/faros-hub \
	FAROS_HUB_IMAGE_TAG=test \
	FAROS_HUB_IMAGE_PULL_POLICY=Never \
	FAROS_AGENT_IMAGE=ghcr.io/faroshq/faros-agent \
	FAROS_AGENT_IMAGE_TAG=test \
	FAROS_AGENT_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/... -v -timeout 30m $(E2E_FLAGS)

e2e-keep: ## Run standalone e2e, keep clusters on failure for debugging
	$(MAKE) e2e-standalone E2E_FLAGS="--keep-clusters"
