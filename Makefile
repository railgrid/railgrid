.PHONY: dev-edge-create dev-run-edge build test lint fix-lint codegen crds clean certs dev-setup run-dex run-hub run-hub-static run-hub-embedded run-hub-embedded-static run-hub-standalone run-hub-embedded-graphql run-kcp dev-login dev-login-static dev-create-workload dev dev-infra dev-run-kcp path boilerplate verify-boilerplate verify-codegen ldflags tools docker-build docker-build-hub docker-build-agent docker-build-dex docker-push-dex verify help-dev dev-status dev-clean-hooks helm-build-local helm-push-local helm-clean build-quickstart-provider build-quickstart-provider-portal build-kuery-provider build-kuery-provider-portal run-provider-kuery kuery-db-up kuery-db-down install-provider-kuery init-provider-kuery uninstall-provider-kuery run-provider-quickstart install-provider-quickstart init-provider-quickstart uninstall-provider-quickstart build-infrastructure-provider build-infrastructure-provider-portal codegen-infrastructure-provider run-provider-infrastructure install-provider-infrastructure init-provider-infrastructure uninstall-provider-infrastructure build-app-studio-provider build-app-studio-provider-portal codegen-app-studio-provider app-studio-db-up app-studio-db-down run-provider-app-studio install-provider-app-studio init-provider-app-studio uninstall-provider-app-studio build-code-provider build-code-provider-portal codegen-code-provider run-provider-code install-provider-code init-provider-code uninstall-provider-code dev-kro-up dev-kro-down dev-kro-seed dev-kro-register-self e2e-infrastructure portal-provider-symlinks build-mcp-provider-portal build-kubernetes-edges-provider-portal build-server-edges-provider-portal e2e-provider e2e-provider-flags e2e-provider-all

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
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS_PKG := github.com/faroshq/faros-kedge/pkg/version
LDFLAGS := -s -w -X $(LDFLAGS_PKG).Version=$(VERSION) -X $(LDFLAGS_PKG).GitCommit=$(GIT_COMMIT) -X $(LDFLAGS_PKG).BuildDate=$(BUILD_DATE)

ldflags: ## Print ldflags for goreleaser
	@echo "$(LDFLAGS)"

all: build

build: build-kedge build-hub build-graphql

build-kedge:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/kedge ./cmd/kedge/

build-kedge-release: ## Build the release-tagging helper (kedge-release <component|all>)
	go build $(GOFLAGS) -o $(BINDIR)/kedge-release ./cmd/kedge-release/

build-hub: build-mcp-provider-portal build-kubernetes-edges-provider-portal build-server-edges-provider-portal
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/kedge-hub ./cmd/kedge-hub/

build-hub-portal: build-portal ## Build hub with embedded portal
	cp -r portal/dist pkg/hub/portal/
	go build $(GOFLAGS) -tags portal_embed -ldflags "$(LDFLAGS)" -o $(BINDIR)/kedge-hub ./cmd/kedge-hub/

build-portal: portal-provider-symlinks build-mcp-provider-portal build-kubernetes-edges-provider-portal build-server-edges-provider-portal ## Build the portal Vue.js SPA (and built-in provider micro-frontends it depends on)
	cd portal && npm ci && npm run build

dev-portal: portal-provider-symlinks ## Run the portal dev server
	cd portal && npm run dev

# Built-in providers ship their UI as separate Vite bundles under
# providers/{name}/portal/. The hub binary embeds those dist/ outputs via
# //go:embed and serves them under /ui/providers/{name}/* from memory
# (see pkg/hub/providers/proxy.go LocalUIAssets branch). The build chain
# below is per-provider so a portal-only rebuild doesn't trigger every
# provider's bundle, but build-hub depends on each one to keep the
# embedded FS in sync with the binary.
build-mcp-provider-portal: portal-provider-symlinks ## Build the mcp provider's micro-frontend (Vite → providers/mcp/portal/dist)
	cd providers/mcp/portal && npx vite build

build-kubernetes-edges-provider-portal: portal-provider-symlinks ## Build the kubernetes-edges provider's micro-frontend
	cd providers/kubernetesedges/portal && npx vite build

build-server-edges-provider-portal: portal-provider-symlinks build-kubernetes-edges-provider-portal ## Build the server-edges provider's micro-frontend (depends on kubernetes-edges sources via Vite alias)
	cd providers/serveredges/portal && npx vite build

# portal-provider-symlinks creates the local node_modules symlink each
# provider portal needs to resolve shared deps (vue, vue-router, pinia,
# urql, tailwind, …) from the main portal's installation. The .vue
# files in providers/{name}/portal/src/ live outside portal/node_modules'
# default Node lookup path, so without the symlink Vite/Rollup fail to
# resolve `vue-router` etc. Symlinks are gitignored and idempotent.
# Installs portal/node_modules first if missing — fresh CI checkouts
# otherwise symlink into a nonexistent dir and `npx vite build` fails.
portal-provider-symlinks:
	@if [ ! -d portal/node_modules ]; then \
		echo "  → installing portal dependencies"; \
		(cd portal && npm ci); \
	fi
	@for d in providers/mcp/portal providers/kubernetesedges/portal providers/serveredges/portal; do \
		if [ ! -L "$$d/node_modules" ]; then \
			ln -sfn ../../../portal/node_modules "$$d/node_modules" && \
			echo "  → symlinked $$d/node_modules"; \
		fi; \
	done

build-graphql: ## Build the GraphQL gateway binary (listener + gateway subcommands)
	go build $(GOFLAGS) -o $(BINDIR)/kedge-graphql ./cmd/graphql/

# build-agent is an alias for build-kedge: the agent container image now ships
# the kedge CLI binary (cmd/kedge/) with ENTRYPOINT [/kedge, agent, run].
build-agent: build-kedge

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

build-app-studio-provider-portal: ## Build the App Studio provider's micro-frontend (Vite + TS → portal/dist)
	cd providers/app-studio/portal && npm install --no-audit --no-fund && npm run build

build-app-studio-provider: build-app-studio-provider-portal ## Build the App Studio provider binary (portal embedded)
	cd providers/app-studio && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/app-studio-provider .

build-code-provider-portal: ## Build the code provider's micro-frontend (Vite + Vue → portal/dist)
	cd providers/code/portal && npm install --no-audit --no-fund && npm run build

build-code-provider: build-code-provider-portal ## Build the code provider binary (portal embedded)
	cd providers/code && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/code-provider .

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
	./$(KCP_APIGEN_GEN) --input-dir providers/code/config/crds --output-dir providers/code/config/kcp
	@for r in connections repositories repositorycommits deploykeys collaborators packages; do \
		cp providers/code/config/kcp/apiresourceschema-$$r.code.kedge.faros.sh.yaml \
		   providers/code/deploy/chart/files/schemas/$$r.code.kedge.faros.sh.yaml; \
	done
	./hack/ensure-boilerplate.sh

codegen-app-studio-provider: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Codegen for the App Studio provider's local API (+ manifest + chart schema)
	@mkdir -p providers/app-studio/config/crds providers/app-studio/config/kcp providers/app-studio/deploy/chart/files/schemas
	cd providers/app-studio && \
		$(CURDIR)/$(CONTROLLER_GEN) object paths="./apis/..." && \
		$(CURDIR)/$(CONTROLLER_GEN) crd paths="./apis/..." \
			output:crd:artifacts:config=$(CURDIR)/providers/app-studio/config/crds
	./$(KCP_APIGEN_GEN) --input-dir providers/app-studio/config/crds --output-dir providers/app-studio/config/kcp
	cp providers/app-studio/config/kcp/apiresourceschema-projects.ai.kedge.faros.sh.yaml \
	   providers/app-studio/deploy/chart/files/schemas/projects.ai.kedge.faros.sh.yaml
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

codegen: crds codegen-code-provider codegen-app-studio-provider boilerplate ## Generate all (CRDs + kcp resources + provider schemas + boilerplate)

verify-codegen: codegen ## Verify codegen is up to date
	@if ! git diff --quiet HEAD; then \
		echo "ERROR: codegen produced a diff. Please run 'make codegen' and commit the result."; \
		git diff --stat; \
		exit 1; \
	fi

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

dev-login: build-kedge
	PATH=$(CURDIR)/$(BINDIR):$$PATH $(BINDIR)/kedge login --hub-url https://localhost:9443 --insecure-skip-tls-verify

dev-login-static: build-kedge ## Login using static token auth (for use with run-hub-static)
	PATH=$(CURDIR)/$(BINDIR):$$PATH $(BINDIR)/kedge login --hub-url https://localhost:9443 --insecure-skip-tls-verify --token=$(STATIC_AUTH_TOKEN)

# TYPE selects the Edge type for dev-edge-create and dev-run-edge.
# Values: kubernetes (default) | server
TYPE ?= kubernetes
# Default DEV_EDGE_NAME is per-type so kubernetes and server edges can coexist.
DEV_EDGE_NAME ?= $(if $(filter server,$(TYPE)),dev-edge-server-1,dev-edge-kube-1)

dev-edge-create: build-kedge ## Create an Edge resource: TYPE=kubernetes (default) or TYPE=server
	PATH=$(CURDIR)/$(BINDIR):$$PATH BINDIR=$(CURDIR)/$(BINDIR) hack/scripts/dev-edge-setup.sh $(DEV_EDGE_NAME) $(TYPE) "env=dev,provider=local"

dev-run-edge: build-kedge ## Run the edge agent: TYPE=kubernetes (default) or TYPE=server
	@test -f .env.edge.$(TYPE) || (echo "Run 'make dev-edge-create TYPE=$(TYPE)' first (expected .env.edge.$(TYPE))"; exit 1)
ifeq ($(TYPE),server)
	$(BINDIR)/kedge agent run \
		--hub-url=https://localhost:9443 \
		--hub-insecure-skip-tls-verify \
		--token=$(KEDGE_EDGE_JOIN_TOKEN) \
		--tunnel-url=https://localhost:9443 \
		--edge-name=$(KEDGE_EDGE_NAME) \
		--cluster=$(KEDGE_EDGE_CLUSTER) \
		--type=server \
		--ssh-proxy-port=2222 \
		--ssh-user=kedge \
		--ssh-password=password
else
	hack/scripts/ensure-kind-cluster.sh
	$(BINDIR)/kedge agent run \
		--hub-url=https://localhost:9443 \
		--hub-insecure-skip-tls-verify \
		--token=$(KEDGE_EDGE_JOIN_TOKEN) \
		--tunnel-url=https://localhost:9443 \
		--edge-name=$(KEDGE_EDGE_NAME) \
		--kubeconfig=.kubeconfig-kedge-agent \
		--cluster=$(KEDGE_EDGE_CLUSTER) \
		--type=kubernetes
endif

dev-create-workload: ## Create a demo VirtualWorkload targeting dev sites
	kubectl apply -f hack/dev/examples/virtualworkload-nginx.yaml

-include .env
-include .env.edge.$(TYPE)
export

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
  -e USER_NAME=kedge \
  -p 2222:2222 \
  --restart unless-stopped \
  lscr.io/linuxserver/openssh-server:latest

GRAPHQL_GRPC_ADDR ?= localhost:50051
GRAPHQL_APIEXPORT_SLICE ?= core.faros.sh
GRAPHQL_APIEXPORT_LOGICAL_CLUSTER ?= root:kedge:providers

dev-run-graphql: build-graphql ## Run GraphQL (listener + gateway, kcp mode, gRPC transport, playground at :8080)
	$(BINDIR)/kedge-graphql run \
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
	--idp-client-id=kedge \
	--idp-client-secret=ZXhhbXBsZS1hcHAtc2VjcmV0

# Auth: Static token
HUB_FLAGS_STATIC := \
	--static-auth-token=$(STATIC_AUTH_TOKEN) \
	--admin-users=$(ADMIN_USERS)

# Platform-admin identities allowed at /api/admin/* + the portal /bonkers area.
# The dev static token "$(STATIC_AUTH_TOKEN)" resolves (proxy.ensureStaticTokenUserOnce)
# to email static-<first8chars>@kedge.local — for dev-token that's
# static-dev-toke@kedge.local. Override for OIDC dev with your real email.
ADMIN_USERS ?= static-dev-toke@kedge.local

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
GRAPHQL_APIEXPORT_LOGICAL_CLUSTER ?= root:kedge:providers
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
	$(BINDIR)/kedge-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_OIDC) $(HUB_FLAGS_KCP_EXTERNAL)

## External KCP + static token auth (requires: make dev-run-kcp)
run-hub-static: build-hub certs
	@source $(SERVICE_HOOKS) && require_service kcp "make dev-run-kcp"
	$(BINDIR)/kedge-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EXTERNAL)

## Embedded KCP + OIDC auth (requires: make run-dex)
run-hub-embedded: build-hub certs
	@source $(SERVICE_HOOKS) && require_service dex "make run-dex"
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/kedge-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_OIDC) $(HUB_FLAGS_KCP_EMBEDDED)

## Embedded KCP + static token auth + embedded GraphQL + portal dev proxy (standalone - no external deps)
run-hub-embedded-static: build-hub certs
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/kedge-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EMBEDDED) $(HUB_FLAGS_GRAPHQL_EMBEDDED) $(HUB_FLAGS_PORTAL_DEV)

## Embedded KCP + static token + embedded GraphQL (fully standalone)
run-hub-standalone: build-hub certs
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/kedge-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_STATIC) $(HUB_FLAGS_KCP_EMBEDDED) $(HUB_FLAGS_GRAPHQL_EMBEDDED)

## Embedded KCP + OIDC + embedded GraphQL
run-hub-embedded-graphql: build-hub certs
	@source $(SERVICE_HOOKS) && require_service dex "make run-dex"
	@source $(SERVICE_HOOKS) && require_service_not_running kcp "embedded kcp mode"
	$(BINDIR)/kedge-hub $(HUB_FLAGS_BASE) $(HUB_FLAGS_OIDC) $(HUB_FLAGS_KCP_EMBEDDED) $(HUB_FLAGS_GRAPHQL_EMBEDDED)

# Local kcp checkout to iterate against. Defaults to the standard per-user Go
# workspace path. Override on the CLI or via env:
#   make tilt-cluster TILT_KCP_DIR=/path/to/kcp
#   make tilt-cluster KCP_DIR=/path/to/kcp
TILT_KCP_DIR ?= $(or $(KCP_DIR),$(HOME)/go/src/github.com/kcp-dev/kcp)

## Full multi-shard kcp in a kind cluster + kedge-hub in-cluster, against a local kcp checkout
tilt-cluster: ## Run Tiltfile.cluster against a local kcp tree (override with TILT_KCP_DIR=... or KCP_DIR=...)
	@# Create the kind cluster + context BEFORE `tilt up`. If the cluster is
	@# created from inside the Tiltfile, Tilt initializes its deploy client
	@# before the kind-kcp-tilt context exists and caches an empty config,
	@# leaving every native k8s_yaml resource stuck on "could not set up
	@# kubernetes client: no configuration has been provided". Guaranteeing the
	@# cluster+context up front avoids that race.
	@kind get clusters 2>/dev/null | grep -qx kcp-tilt || kind create cluster --name kcp-tilt
	@kind export kubeconfig --name kcp-tilt
	tilt up -f Tiltfile.cluster -- --kcp-dir="$(TILT_KCP_DIR)"

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
QUICKSTART_WORKSPACE_PATH ?= root:kedge:providers:quickstart
QUICKSTART_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/quickstart-runtime.kubeconfig

## Run the quickstart provider binary locally. Heartbeats to the hub on
## $(QUICKSTART_HUB_URL); TLS verification skipped (dev cert is self-signed).
run-provider-quickstart: build-quickstart-provider ## Run the quickstart provider (requires: make run-hub-embedded-static + make install-provider-quickstart)
	@echo "Starting quickstart provider on :$(QUICKSTART_PORT)"
	@echo "  hub:   $(QUICKSTART_HUB_URL)"
	@echo "  token: $(QUICKSTART_TOKEN)"
	PORT=$(QUICKSTART_PORT) \
	KEDGE_HUB_URL=$(QUICKSTART_HUB_URL) \
	KEDGE_HUB_TOKEN=$(QUICKSTART_TOKEN) \
	KEDGE_HUB_INSECURE=true \
	KEDGE_PROVIDER_NAME=quickstart \
		$(BINDIR)/quickstart-provider

## Apply the quickstart CatalogEntry into root:kedge:providers. Idempotent.
## Requires the hub to be running so the admin kubeconfig exists.
install-provider-quickstart: ## Apply quickstart Provider + CatalogEntry into root:kedge:providers
	@test -f $(QUICKSTART_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(QUICKSTART_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=$(QUICKSTART_KCP_SERVER)/clusters/root:kedge:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(QUICKSTART_PROVIDER_MANIFEST) -f $(QUICKSTART_MANIFEST)

## Run provider e2e suite (embedded kcp + quickstart-provider subprocess).
## Lightweight — no kind/Helm, just two host binaries the suite drives over
## HTTP + kcp dynamic clients. KEDGE_E2E_KEEP_DATA=true preserves logs/data.
E2E_PROVIDER_TIMEOUT ?= 10m
e2e-provider: build-hub build-quickstart-provider ## Run provider e2e suite
	@test -z "$$(lsof -ti :19443 :16443 :18081 :2380 2>/dev/null)" || { \
		echo "ports 19443/16443/18081/2380 are in use; stop any running kedge-hub/quickstart-provider first"; \
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
		echo "ports 19443/16443/2380 are in use; stop any running kedge-hub first (e.g. pkill kedge-hub)"; \
		exit 1; \
	}
	go test ./test/e2e/suites/providerflags/... -v -timeout $(E2E_PROVIDER_FLAGS_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Run both provider suites back-to-back (sequential — they share port 2380).
e2e-provider-all: e2e-provider e2e-provider-flags ## Run provider + provider-flags suites sequentially

## Provider-specific isolation suite for infrastructure. Spawns only
## the infrastructure-provider binary on :18082 in stub mode (no kro
## cluster, no hub, no kcp) and asserts cross-tenant reads / writes /
## deletes are properly scoped. Independent of e2e-provider — they
## don't share ports, so they could in principle run in parallel.
E2E_KROMC_TIMEOUT ?= 5m
e2e-infrastructure: build-infrastructure-provider ## Run infrastructure tenant-isolation e2e
	@test -z "$$(lsof -ti :18082 2>/dev/null)" || { \
		echo "port 18082 is in use; pkill infrastructure-provider and retry"; \
		exit 1; \
	}
	go test ./test/e2e/suites/infrastructure/... -v -timeout $(E2E_KROMC_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

## Tilt-cluster suite: runs against an ALREADY-RUNNING operator-deployed,
## multi-shard Tilt stack (start it in another terminal with `make tilt-cluster`).
## Unlike the other e2e suites it does NOT spawn its own processes — it connects
## to the live stack (kcp front-proxy via tilt-frontproxy.kubeconfig, the
## in-cluster hub, the host-run providers) and verifies the providers end-to-end:
## provider registration, the templates catalog/projection, MCP tool federation,
## and the per-tenant identity gate. Endpoints override via KEDGE_E2E_* env.
E2E_TILT_TIMEOUT ?= 10m
E2E_TILT_HUB_URL ?= https://localhost:9443
E2E_TILT_INFRA_URL ?= http://localhost:8082
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
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: kedge\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: kedge\n  context:\n    cluster: kedge\n    user: kedge\ncurrent-context: kedge\nusers:\n- name: kedge\n  user:\n    token: %s\n' \
		"$(QUICKSTART_KCP_SERVER)/clusters/$(QUICKSTART_WORKSPACE_PATH)" "$$TOKEN" \
		> $(QUICKSTART_RUNTIME_KUBECONFIG)
	@echo "Running quickstart-provider init (creates APIExport + endpoint slice + bind grant)"
	KEDGE_PROVIDER_KUBECONFIG=$(QUICKSTART_RUNTIME_KUBECONFIG) \
	QUICKSTART_WORKSPACE_PATH=$(QUICKSTART_WORKSPACE_PATH) \
	KEDGE_SCHEMAS_DIR=/nonexistent \
		$(BINDIR)/quickstart-provider init

## Delete the quickstart CatalogEntry + Provider. Deleting the Provider triggers
## full teardown of root:kedge:providers:quickstart (workspace, SA, APIExport)
## via the controller's finalizer.
uninstall-provider-quickstart: ## Delete quickstart CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=$(QUICKSTART_KCP_SERVER)/clusters/root:kedge:system:providers \
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
KUERY_WORKSPACE_PATH ?= root:kedge:providers:kuery
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
KUERY_POSTGRES_CONTAINER ?= kedge-kuery-postgres
KUERY_POSTGRES_IMAGE ?= postgres:16-alpine
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
	KEDGE_HUB_URL=$(KUERY_HUB_URL) \
	KEDGE_HUB_TOKEN=$(KUERY_TOKEN) \
	KEDGE_HUB_INSECURE=true \
	KEDGE_PROVIDER_NAME=kuery \
	KEDGE_PROVIDER_KUBECONFIG=$(KUERY_RUNTIME_KUBECONFIG) \
	KEDGE_DEV_ALLOW_TENANT_QUERY=true \
	KUERY_STORE_DRIVER=postgres \
	KUERY_STORE_DSN="$$STORE_DSN" \
		$(BINDIR)/kuery-provider

install-provider-kuery: ## Apply kuery Provider + CatalogEntry into root:kedge:providers
	@test -f $(KUERY_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KUERY_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KUERY_KCP_KUBECONFIG) \
		--server=$(KUERY_KCP_SERVER)/clusters/root:kedge:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(KUERY_PROVIDER_MANIFEST) -f $(KUERY_MANIFEST)

## Dev bootstrap for the engagement controller. The Provider controller writes
## the minted kubeconfig into a Secret in root:kedge:providers, but host-binary
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
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: kedge\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: kedge\n  context:\n    cluster: kedge\n    user: kedge\ncurrent-context: kedge\nusers:\n- name: kedge\n  user:\n    token: %s\n' \
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
			--server=$(KUERY_KCP_SERVER)/clusters/root:kedge:system:controllers \
			--insecure-skip-tls-verify \
			get apiexport core.faros.sh -o jsonpath='{.status.identityHash}'); \
		echo "resolved edges identityHash from core.faros.sh: $$HASH"; \
	fi; \
	test -n "$$HASH" || { echo "could not resolve core.faros.sh identityHash — is the hub bootstrapped?"; exit 1; }; \
	KEDGE_PROVIDER_KUBECONFIG=$(KUERY_RUNTIME_KUBECONFIG) \
	KUERY_WORKSPACE_PATH=$(KUERY_WORKSPACE_PATH) \
	KEDGE_SCHEMAS_DIR=$(KUERY_SCHEMAS_DIR) \
	KUERY_EDGES_IDENTITY_HASH=$$HASH \
		$(BINDIR)/kuery-provider init

uninstall-provider-kuery: ## Delete kuery CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KUERY_KCP_KUBECONFIG) \
		--server=$(KUERY_KCP_SERVER)/clusters/root:kedge:system:providers \
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
APP_STUDIO_KCP_KUBECONFIG ?= $(KCP_DATA_DIR)/admin.kubeconfig
APP_STUDIO_KCP_SERVER ?= https://localhost:6443
APP_STUDIO_WORKSPACE_PATH ?= root:kedge:providers:app-studio
APP_STUDIO_RUNTIME_KUBECONFIG ?= $(KCP_DATA_DIR)/app-studio-runtime.kubeconfig
APP_STUDIO_SCHEMAS_DIR ?= providers/app-studio/deploy/chart/files/schemas
APP_STUDIO_MANIFEST ?= providers/app-studio/manifest.yaml
APP_STUDIO_PROVIDER_MANIFEST ?= providers/app-studio/provider.yaml
APP_STUDIO_DATABASE_URL ?=
APP_STUDIO_IN_MEMORY_MESSAGE_STORE ?=
APP_STUDIO_AUTO_APPROVE_ACTIONS ?= true
APP_STUDIO_DEV_DATABASE_URL ?= postgres://appstudio:appstudio@localhost:55432/appstudio?sslmode=disable
APP_STUDIO_POSTGRES_CONTAINER ?= kedge-app-studio-postgres
APP_STUDIO_POSTGRES_IMAGE ?= postgres:16-alpine
APP_STUDIO_POSTGRES_PORT ?= 55432
APP_STUDIO_POSTGRES_DATA_DIR ?= $(KCP_DATA_DIR)/app-studio-postgres
APP_STUDIO_POSTGRES_USER ?= appstudio
APP_STUDIO_POSTGRES_PASSWORD ?= appstudio
APP_STUDIO_POSTGRES_DB ?= appstudio

## Run the infrastructure provider binary locally. Heartbeats to the hub on
## $(KROMC_HUB_URL); TLS verification skipped (dev cert is self-signed).
## KRO_KUBECONFIG is left unset by default → provider serves the baked-in
## stub catalog so the UI is demoable without standing up a real central
## kro cluster. Point KRO_KUBECONFIG at a real kubeconfig to use the
## real client + your own ResourceGraphDefinitions.
run-provider-infrastructure: build-infrastructure-provider ## Run the infrastructure provider (requires: make run-hub-embedded-static + make install-provider-infrastructure)
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
	KEDGE_HUB_URL=$(KROMC_HUB_URL) \
	KEDGE_HUB_TOKEN=$(KROMC_TOKEN) \
	KEDGE_HUB_INSECURE=true \
	KEDGE_PROVIDER_NAME=infrastructure \
	KEDGE_DEV_ALLOW_TENANT_QUERY=true \
	INFRASTRUCTURE_WORKSPACE_PATH=$${INFRASTRUCTURE_WORKSPACE_PATH:-$(INFRASTRUCTURE_WORKSPACE_PATH)} \
	KRO_KUBECONFIG=$${KRO_KUBECONFIG:-$$( [ -f "$(KRO_KIND_KUBECONFIG)" ] && echo "$(KRO_KIND_KUBECONFIG)" )} \
	INFRASTRUCTURE_KUBECONFIG=$${INFRASTRUCTURE_KUBECONFIG:-$$( [ -f "$(INFRASTRUCTURE_RUNTIME_KUBECONFIG)" ] && echo "$(INFRASTRUCTURE_RUNTIME_KUBECONFIG)" )} \
		$(BINDIR)/infrastructure-provider

run-provider-infrastructure-operator: build-infrastructure-provider ## Run the infrastructure provider in OPERATOR mode (bootstrap reconcile + serve from a provider + runtime kubeconfig)
	@echo "Starting infrastructure provider (operator) on :$(KROMC_PORT)"
	@echo "  hub:      $(KROMC_HUB_URL)"
	@echo "  provider: $${INFRASTRUCTURE_PROVIDER_KUBECONFIG:-$(KROMC_KCP_KUBECONFIG)} (kcp)"
	@echo "  runtime:  $${INFRASTRUCTURE_RUNTIME_KUBECONFIG:-$(KRO_KIND_KUBECONFIG)} (kro cluster)"
	@echo "  ws:       $(INFRASTRUCTURE_WORKSPACE_PATH)"
	@# The operator needs the provider workspace to already exist — run
	@# `make install-provider-infrastructure` (admin-portal onboarding in prod)
	@# first. It then reconciles the in-workspace bootstrap and seeds kro itself.
	PORT=$(KROMC_PORT) \
	KEDGE_HUB_URL=$(KROMC_HUB_URL) \
	KEDGE_HUB_TOKEN=$(KROMC_TOKEN) \
	KEDGE_HUB_INSECURE=true \
	KEDGE_PROVIDER_NAME=infrastructure \
	KEDGE_DEV_ALLOW_TENANT_QUERY=true \
	INFRASTRUCTURE_WORKSPACE_PATH=$(INFRASTRUCTURE_WORKSPACE_PATH) \
	INFRASTRUCTURE_PROVIDER_KUBECONFIG=$${INFRASTRUCTURE_PROVIDER_KUBECONFIG:-$(KROMC_KCP_KUBECONFIG)} \
	INFRASTRUCTURE_RUNTIME_KUBECONFIG=$${INFRASTRUCTURE_RUNTIME_KUBECONFIG:-$$( [ -f "$(KRO_KIND_KUBECONFIG)" ] && echo "$(KRO_KIND_KUBECONFIG)" )} \
		$(BINDIR)/infrastructure-provider operator

# ── CRD-driven operator (controller) dev flow ───────────────────────────────
# Replaces kro-mgmt-up + infrastructure-init: one host-binary controller that
# bootstraps the workspace, helm-installs kro (with the kind hostAliases +
# self-cluster patches), and (skip-serve in dev) leaves serve to the host binary.
INFRA_OPERATOR_NS ?= kedge-infrastructure-operator
INFRA_OPERATOR_PROVIDER_KC ?= $(KROMC_KCP_KUBECONFIG)
INFRA_OPERATOR_RUNTIME_KC ?= $(KRO_KIND_KUBECONFIG)
INFRA_OPERATOR_KIND_NAME ?= $(KRO_KIND_NAME)
INFRA_OPERATOR_SELF_KC ?= $(KCP_DATA_DIR)/kro-self.kubeconfig
INFRA_OPERATOR_HOSTALIASES_IP ?= 10.96.2.2
INFRA_OPERATOR_HOSTALIASES_NAMES ?= kcp.localhost,root.kcp.localhost,theseus.kcp.localhost
INFRA_OPERATOR_CRD ?= providers/infrastructure/config/crds/infrastructure.kedge.faros.sh_infrastructureproviders.yaml

run-provider-infrastructure-controller: build-infrastructure-provider ## Apply the operator CRD/Secrets/CR into the runtime cluster and run the controller (dev)
	@echo "Applying operator CRD + Secrets + CR into runtime cluster ($(INFRA_OPERATOR_RUNTIME_KC))"
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f $(INFRA_OPERATOR_CRD)
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl create namespace $(INFRA_OPERATOR_NS) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl -n $(INFRA_OPERATOR_NS) create secret generic provider-kubeconfig --from-file=kubeconfig=$(INFRA_OPERATOR_PROVIDER_KC) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl -n $(INFRA_OPERATOR_NS) create secret generic runtime-kubeconfig --from-file=kubeconfig=$(INFRA_OPERATOR_RUNTIME_KC) --dry-run=client -o yaml | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	@# kind-internal kubeconfig for the kro local-runtime self-member.
	kind get kubeconfig --internal --name $(INFRA_OPERATOR_KIND_NAME) > $(INFRA_OPERATOR_SELF_KC)
	@printf 'apiVersion: infrastructure.kedge.faros.sh/v1alpha1\nkind: InfrastructureProvider\nmetadata:\n  name: infrastructure\n  namespace: %s\nspec:\n  providerWorkspace: %s\n  providerKubeconfigSecret:\n    name: provider-kubeconfig\n  runtimeKubeconfigSecret:\n    name: runtime-kubeconfig\n  kro:\n    chart: %s\n    version: %s\n    image:\n      repository: %s\n      tag: %s\n  provider:\n    image:\n      repository: ghcr.io/faroshq/kedge-infrastructure-provider\n      tag: dev\n' "$(INFRA_OPERATOR_NS)" "$(INFRASTRUCTURE_WORKSPACE_PATH)" "$(KRO_CHART)" "$(KRO_CHART_VERSION)" "$(KRO_IMAGE_REPO)" "$(KRO_IMAGE_TAG)" | KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) kubectl apply -f -
	@echo "Running infrastructure operator controller (KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC), skip-serve)"
	KUBECONFIG=$(INFRA_OPERATOR_RUNTIME_KC) \
	INFRASTRUCTURE_WORKSPACE_PATH=$(INFRASTRUCTURE_WORKSPACE_PATH) \
	INFRASTRUCTURE_OPERATOR_SKIP_SERVE=true \
	INFRASTRUCTURE_KRO_HOSTALIASES_IP=$(INFRA_OPERATOR_HOSTALIASES_IP) \
	INFRASTRUCTURE_KRO_HOSTALIASES_NAMES=$(INFRA_OPERATOR_HOSTALIASES_NAMES) \
	INFRASTRUCTURE_KRO_SELF_CLUSTER_KUBECONFIG=$(INFRA_OPERATOR_SELF_KC) \
		$(BINDIR)/infrastructure-provider controller

## Run the App Studio provider binary locally. Mirrors the other external
## providers so the heartbeat path is consistent and the UI runs on :8085.
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

run-provider-app-studio: build-app-studio-provider app-studio-db-up ## Run the App Studio provider (requires: make run-hub-embedded-static + make install-provider-app-studio)
	@echo "Starting App Studio provider on :$(APP_STUDIO_PORT)"
	@echo "  hub:   $(APP_STUDIO_HUB_URL)"
	@echo "  token: $(APP_STUDIO_TOKEN)"
	@# Auto-source providers/app-studio/.env (gitignored) so local store/LLM
	@# overrides reach Tilt and make without a manual export. See .env.example.
	set -a; [ -f providers/app-studio/.env ] && . ./providers/app-studio/.env || true; set +a; \
	APP_STUDIO_DATABASE_URL="$${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DATABASE_URL)}"; \
	APP_STUDIO_IN_MEMORY_MESSAGE_STORE="$${APP_STUDIO_IN_MEMORY_MESSAGE_STORE:-$(APP_STUDIO_IN_MEMORY_MESSAGE_STORE)}"; \
	APP_STUDIO_AUTO_APPROVE_ACTIONS="$${APP_STUDIO_AUTO_APPROVE_ACTIONS:-$(APP_STUDIO_AUTO_APPROVE_ACTIONS)}"; \
	if [ "$${APP_STUDIO_IN_MEMORY_MESSAGE_STORE:-}" = "true" ]; then \
		echo "  store: in-memory (non-durable)"; \
		echo "  auto-approve actions: $${APP_STUDIO_AUTO_APPROVE_ACTIONS}"; \
		APP_STUDIO_DATABASE_URL= \
		PORT=$(APP_STUDIO_PORT) \
		KEDGE_HUB_URL=$(APP_STUDIO_HUB_URL) \
		KEDGE_HUB_TOKEN=$(APP_STUDIO_TOKEN) \
		KEDGE_HUB_INSECURE=true \
		KEDGE_PROVIDER_NAME=app-studio \
		KEDGE_PROVIDER_KUBECONFIG=$${KEDGE_PROVIDER_KUBECONFIG:-$$( for f in "$(APP_STUDIO_KCP_KUBECONFIG)" "$(CURDIR)/tilt-frontproxy.kubeconfig"; do [ -f "$$f" ] && echo "$$f" && break; done )} \
		APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true \
		APP_STUDIO_AUTO_APPROVE_ACTIONS="$${APP_STUDIO_AUTO_APPROVE_ACTIONS}" \
		APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY=true \
			$(BINDIR)/app-studio-provider; \
	else \
		echo "  store: $${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DEV_DATABASE_URL)}"; \
		echo "  auto-approve actions: $${APP_STUDIO_AUTO_APPROVE_ACTIONS}"; \
		PORT=$(APP_STUDIO_PORT) \
		KEDGE_HUB_URL=$(APP_STUDIO_HUB_URL) \
		KEDGE_HUB_TOKEN=$(APP_STUDIO_TOKEN) \
		KEDGE_HUB_INSECURE=true \
		KEDGE_PROVIDER_NAME=app-studio \
		KEDGE_PROVIDER_KUBECONFIG=$${KEDGE_PROVIDER_KUBECONFIG:-$$( for f in "$(APP_STUDIO_KCP_KUBECONFIG)" "$(CURDIR)/tilt-frontproxy.kubeconfig"; do [ -f "$$f" ] && echo "$$f" && break; done )} \
		APP_STUDIO_DATABASE_URL="$${APP_STUDIO_DATABASE_URL:-$(APP_STUDIO_DEV_DATABASE_URL)}" \
		APP_STUDIO_AUTO_APPROVE_ACTIONS="$${APP_STUDIO_AUTO_APPROVE_ACTIONS}" \
		APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY=true \
			$(BINDIR)/app-studio-provider; \
	fi

## Apply the App Studio CatalogEntry into root:kedge:providers. Idempotent.
install-provider-app-studio: ## Apply App Studio Provider + CatalogEntry into root:kedge:providers
	@test -f $(APP_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(APP_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/root:kedge:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(APP_STUDIO_PROVIDER_MANIFEST) -f $(APP_STUDIO_MANIFEST)

## Create App Studio's APIExport (+ schemas + endpoint slice + bind grant) in
## its provider workspace so tenants can Enable it. Reads the provider-token the
## Provider controller minted on register, writes a dev runtime kubeconfig, and
## runs the provider's `init` (sdkinstall.Bootstrap) with the shipped schemas.
## KEDGE_CATALOGENTRY_FILE is intentionally unset — the dev install target
## already applied the CatalogEntry to system:providers. Idempotent.
init-provider-app-studio: build-app-studio-provider ## Bootstrap App Studio APIExport + write dev runtime kubeconfig
	@test -f $(APP_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(APP_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	@echo "Reading provider-token from $(APP_STUDIO_WORKSPACE_PATH) and writing $(APP_STUDIO_RUNTIME_KUBECONFIG)"
	@TOKEN=$$(kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/$(APP_STUDIO_WORKSPACE_PATH) \
		--insecure-skip-tls-verify \
		get secret -n default provider-token -o jsonpath='{.data.token}' | base64 -d); \
	test -n "$$TOKEN" || { echo "provider-token Secret empty — wait for the Provider controller to provision the workspace"; exit 1; }; \
	mkdir -p $(KCP_DATA_DIR); \
	printf 'apiVersion: v1\nkind: Config\nclusters:\n- name: kedge\n  cluster:\n    server: %s\n    insecure-skip-tls-verify: true\ncontexts:\n- name: kedge\n  context:\n    cluster: kedge\n    user: kedge\ncurrent-context: kedge\nusers:\n- name: kedge\n  user:\n    token: %s\n' \
		"$(APP_STUDIO_KCP_SERVER)/clusters/$(APP_STUDIO_WORKSPACE_PATH)" "$$TOKEN" \
		> $(APP_STUDIO_RUNTIME_KUBECONFIG)
	@echo "Running app-studio-provider init (creates APIExport + schemas + endpoint slice + bind grant)"
	KEDGE_PROVIDER_KUBECONFIG=$(APP_STUDIO_RUNTIME_KUBECONFIG) \
	APP_STUDIO_WORKSPACE_PATH=$(APP_STUDIO_WORKSPACE_PATH) \
	KEDGE_SCHEMAS_DIR=$(APP_STUDIO_SCHEMAS_DIR) \
		$(BINDIR)/app-studio-provider init

## Delete the App Studio CatalogEntry. Useful while iterating on the chart.
uninstall-provider-app-studio: ## Delete App Studio CatalogEntry
	@test -f $(APP_STUDIO_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(APP_STUDIO_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	-kubectl --kubeconfig=$(APP_STUDIO_KCP_KUBECONFIG) \
		--server=$(APP_STUDIO_KCP_SERVER)/clusters/root:kedge:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(APP_STUDIO_MANIFEST) -f $(APP_STUDIO_PROVIDER_MANIFEST)

## Apply the infrastructure CatalogEntry into root:kedge:providers. Idempotent.
## Requires the hub to be running so the admin kubeconfig exists.
install-provider-infrastructure: ## Apply infrastructure Provider + CatalogEntry into root:kedge:providers
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:kedge:system:providers \
		--insecure-skip-tls-verify \
		apply --validate=false -f $(KROMC_PROVIDER_MANIFEST) -f $(KROMC_MANIFEST)

## Delete the infrastructure CatalogEntry + Provider (Provider delete triggers
## full teardown of the sub-workspace via the controller's finalizer).
uninstall-provider-infrastructure: ## Delete infrastructure CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:kedge:system:providers \
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
INFRASTRUCTURE_WORKSPACE_PATH ?= root:kedge:providers:infrastructure
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
	KEDGE_HUB_URL=$(KROMC_HUB_URL) \
	KEDGE_HUB_TOKEN=$(KROMC_TOKEN) \
	KEDGE_HUB_INSECURE=true \
	KEDGE_PROVIDER_NAME=code \
	KEDGE_DEV_ALLOW_TENANT_QUERY=true \
	CODE_COMMIT_BUNDLE_DIR=$${CODE_COMMIT_BUNDLE_DIR:-$(KCP_DATA_DIR)/code-commit-bundles} \
	KEDGE_PROVIDER_KUBECONFIG=$${KEDGE_PROVIDER_KUBECONFIG:-$$( [ -f "$(CODE_RUNTIME_KUBECONFIG)" ] && echo "$(CODE_RUNTIME_KUBECONFIG)" )} \
	GITHUB_OAUTH_CLIENT_ID=$${GITHUB_OAUTH_CLIENT_ID:-} \
	GITHUB_OAUTH_CLIENT_SECRET=$${GITHUB_OAUTH_CLIENT_SECRET:-} \
	GITHUB_OAUTH_REDIRECT_URL=$${GITHUB_OAUTH_REDIRECT_URL:-http://localhost:$(CODE_PORT)/oauth/github/callback} \
	GITHUB_OAUTH_PORTAL_ORIGIN=$${GITHUB_OAUTH_PORTAL_ORIGIN:-$(KROMC_HUB_URL)} \
		$(BINDIR)/code-provider serve

install-provider-code: ## Apply the code Provider + CatalogEntry into root:kedge:providers
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:kedge:system:providers \
		--insecure-skip-tls-verify \
		apply -f $(CODE_PROVIDER_MANIFEST) -f $(CODE_MANIFEST)

uninstall-provider-code: ## Delete the code CatalogEntry + Provider (full teardown)
	-kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=$(KROMC_KCP_SERVER)/clusters/root:kedge:system:providers \
		--insecure-skip-tls-verify \
		delete -f $(CODE_MANIFEST) -f $(CODE_PROVIDER_MANIFEST)

CODE_WORKSPACE_PATH ?= root:kedge:providers:code
## Dev bootstrap for the code provider. The hub mints a real provider
## kubeconfig only when it runs with a host cluster (--kubeconfig); the dev hubs
## (embedded + Tiltfile.cluster) do not, so we derive a runtime kubeconfig from
## the admin kubeconfig — reusing its working credential (a static token in
## embedded mode, a client cert in cluster mode) and retargeting only the server
## URL to the provider workspace — and ensure the APIExportEndpointSlice the
## controller manager needs. run-provider-code reads it via KEDGE_PROVIDER_KUBECONFIG.
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
	@# The provider workspace (root:kedge:providers:code) is created
	@# declaratively by the Provider controller when code-register applies the
	@# Provider CR — no need to create it here.
	@echo "Writing dev kubeconfig $(CODE_RUNTIME_KUBECONFIG) (workspace $(CODE_WORKSPACE_PATH), server $(KROMC_KCP_SERVER))"
	@kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) config view --minify --flatten > $(CODE_RUNTIME_KUBECONFIG)
	@CL=$$(kubectl --kubeconfig=$(CODE_RUNTIME_KUBECONFIG) config view -o jsonpath='{.clusters[0].name}'); \
		kubectl --kubeconfig=$(CODE_RUNTIME_KUBECONFIG) config set-cluster "$$CL" \
			--server=$(KROMC_KCP_SERVER)/clusters/$(CODE_WORKSPACE_PATH) \
			--insecure-skip-tls-verify=true >/dev/null
	KEDGE_PROVIDER_KUBECONFIG=$(CODE_RUNTIME_KUBECONFIG) \
	CODE_WORKSPACE_PATH=$(CODE_WORKSPACE_PATH) \
	KEDGE_SCHEMAS_DIR=$(CURDIR)/providers/code/deploy/chart/files/schemas \
		$(BINDIR)/code-provider init

# --- Experimental: run the infrastructure provider as a POD (init-container
#     bootstrap) instead of a host binary. Exercises the full hub-minted
#     flow: CatalogEntry -> hub mints + delivers kedge-provider-kubeconfig
#     (HostSecretWriter) -> init container bootstraps with it -> serve runs.
#     Reuses the kedge-kro kind cluster as the host cluster. Requires the hub
#     to run with --kubeconfig=$(KRO_KIND_KUBECONFIG) and
#     --provider-internal-url=$(PROVIDER_INTERNAL_HUB_URL) (the Tiltfile sets
#     both). Apply the CatalogEntry first: make install-provider-infrastructure
INFRASTRUCTURE_NAMESPACE ?= infrastructure
INFRASTRUCTURE_IMAGE ?= kedge-infrastructure-provider:dev
INFRASTRUCTURE_CHART ?= providers/infrastructure/deploy/chart
# Address provider pods in the kind cluster use to reach the hub front-proxy
# (browsers use https://localhost:9443; host.docker.internal resolves to the
# host from inside kind on Docker Desktop / Colima / OrbStack).
PROVIDER_INTERNAL_HUB_URL ?= https://host.docker.internal:9443
helm-deploy-provider-infrastructure: ## (experimental) Build+load image, helm install the provider as a pod into kedge-kro (hub-minted bootstrap)
	@command -v kind >/dev/null || { echo "kind not found; brew install kind"; exit 1; }
	@test -f $(KRO_KIND_KUBECONFIG) || { echo "kedge-kro cluster missing; run 'make dev-kro-up' first"; exit 1; }
	@echo ">>> building $(INFRASTRUCTURE_IMAGE)"
	docker build -t $(INFRASTRUCTURE_IMAGE) providers/infrastructure
	@echo ">>> loading image into kind cluster $(KRO_KIND_NAME)"
	kind load docker-image $(INFRASTRUCTURE_IMAGE) --name $(KRO_KIND_NAME)
	@echo ">>> ensuring namespace + heartbeat token Secret in $(INFRASTRUCTURE_NAMESPACE)"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl create namespace $(INFRASTRUCTURE_NAMESPACE) \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(INFRASTRUCTURE_NAMESPACE) create secret generic kedge-infrastructure-hub-token \
		--from-literal=token=$(STATIC_AUTH_TOKEN) \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	@echo ">>> helm install (bootstrap.enabled=true, kubeconfigSource=hubMinted)"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) helm upgrade --install infrastructure $(INFRASTRUCTURE_CHART) \
		--namespace $(INFRASTRUCTURE_NAMESPACE) \
		--set image.repository=kedge-infrastructure-provider \
		--set image.tag=dev \
		--set image.pullPolicy=Never \
		--set replicaCount=1 \
		--set bootstrap.enabled=true \
		--set hub.url=$(PROVIDER_INTERNAL_HUB_URL) \
		--set hub.insecure=true \
		--set catalogEntry.enabled=false
	@echo ">>> deployed. The pod stays in ContainerCreating until the hub delivers"
	@echo "    the kedge-provider-kubeconfig Secret (apply the CatalogEntry first:"
	@echo "    make install-provider-infrastructure). Watch:"
	@echo "    KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(INFRASTRUCTURE_NAMESPACE) get pods -w"

helm-undeploy-provider-infrastructure: ## (experimental) helm uninstall the infrastructure provider pod
	-KUBECONFIG=$(KRO_KIND_KUBECONFIG) helm uninstall infrastructure -n $(INFRASTRUCTURE_NAMESPACE)

# --- Management kro cluster (backend for the infrastructure provider) ---
# Brings up a dedicated kind cluster running the faroshq/kro-multicluster
# fork (image + chart published to ghcr.io/faroshq/kro-multicluster/*),
# configured for multicluster mode per the fork's docs/multicluster-setup.md:
#
#   - --enable-multicluster flag turns on the discovery loop
#   - cluster kubeconfigs are stored as Secrets in $(KRO_NAMESPACE),
#     labeled kro.run/cluster=true; key "kubeconfig" holds the YAML
#   - the kind cluster is registered AS A MEMBER OF ITSELF so kro
#     reconciles instances back into the same cluster (single-node
#     multicluster — turnkey for dev without standing up two clusters)
#
# Seeds the cluster with the sample ResourceGraphDefinitions under
# providers/infrastructure/examples/rgds/. The kedge infrastructure
# provider points at this cluster via KRO_KUBECONFIG so the catalog UI
# shows real templates and provision materializes real Deployments /
# Services.
#
KRO_KIND_NAME ?= kedge-kro
KRO_KIND_KUBECONFIG ?= $(CURDIR)/.kedge-kro.kubeconfig
KRO_CHART ?= oci://ghcr.io/faroshq/kro-multicluster/charts/kro/kro
KRO_CHART_VERSION ?= v0.0.1-mc.7
KRO_IMAGE_REPO ?= ghcr.io/faroshq/kro-multicluster/kro
KRO_IMAGE_TAG ?= v0.0.1-mc.7
KRO_NAMESPACE ?= kro-system
KRO_SEED_DIR ?= providers/infrastructure/examples/rgds

## Bring up the management kro cluster + install kro (fork w/ multicluster) +
## register the cluster as a self-member + seed RGDs. Idempotent: re-running
## just helm-upgrades the chart and re-applies the RGDs.
dev-kro-up: ## Bring up the kedge-kro kind cluster + install kro (multicluster) + apply seed RGDs
	@command -v kind >/dev/null || { echo "kind not found; install: brew install kind"; exit 1; }
	@command -v helm >/dev/null || { echo "helm not found; install: brew install helm"; exit 1; }
	@if ! kind get clusters | grep -qx "$(KRO_KIND_NAME)"; then \
		echo ">>> creating kind cluster $(KRO_KIND_NAME)"; \
		kind create cluster --name $(KRO_KIND_NAME) --kubeconfig $(KRO_KIND_KUBECONFIG); \
	else \
		echo ">>> kind cluster $(KRO_KIND_NAME) already exists"; \
		kind get kubeconfig --name $(KRO_KIND_NAME) > $(KRO_KIND_KUBECONFIG); \
	fi
	@# Pre-create the namespace + placeholder kcp-kubeconfig Secret BEFORE
	@# helm install. The chart's kro Deployment mounts this Secret at
	@# /etc/kro/kcp/kubeconfig — without it, the pod gets stuck in
	@# ContainerCreating ("secret kcp-kubeconfig not found") and the
	@# `helm upgrade --wait` times out. The Secret's content is a stub;
	@# `make init-provider-infrastructure` later overwrites it with the
	@# real minted kubeconfig and bounces the kro pod to pick it up.
	@echo ">>> ensuring $(KRO_NAMESPACE) namespace + placeholder kcp-kubeconfig Secret"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl create namespace $(KRO_NAMESPACE) \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	@# --from-literal needs a value; "pending-init" is human-readable
	@# enough that anyone inspecting the Secret pre-init knows it's not
	@# the real thing.
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(KRO_NAMESPACE) create secret generic kcp-kubeconfig \
		--from-literal=kubeconfig=pending-init \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	@echo ">>> installing kro (fork, kcp-apiexport mode) into $(KRO_NAMESPACE)"
	@# image.repository/tag pin the fork's controller image; the chart's
	@# default ${VERSION} placeholder doesn't resolve outside the release
	@# pipeline. We use the kcp-apiexport provider (mc.3+) so kro reads
	@# the APIExportEndpointSlice in the provider workspace directly
	@# instead of watching labeled Secrets — that's the cleaner kcp-aware
	@# topology. Wiring:
	@#   multicluster.kcp.kubeconfigSecret = kcp-kubeconfig (written by
	@#     install.SeedKroCluster in init mode; mounted at /etc/kro/kcp/kubeconfig)
	@#   multicluster.kcp.apiExportEndpointSlice = infrastructure (created by
	@#     install.PlatformAPIExportEndpointSlice in init mode; matches
	@#     install.APIExportEndpointSliceName)
	@# Drop --wait because the kro pod won't become Ready until init has
	@# replaced the placeholder kubeconfig and bounced it — but helm
	@# install itself only needs to succeed (resources applied).
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) helm upgrade --install kro $(KRO_CHART) \
		--version $(KRO_CHART_VERSION) \
		--namespace $(KRO_NAMESPACE) --create-namespace \
		--set image.repository=$(KRO_IMAGE_REPO) \
		--set image.tag=$(KRO_IMAGE_TAG) \
		--set multicluster.enabled=true \
		--set multicluster.provider=kcp-apiexport \
		--set multicluster.kcp.kubeconfigSecret=kcp-kubeconfig \
		--set multicluster.kcp.apiExportEndpointSlice=infrastructure \
		--set controller.deployToLocalRuntime=true \
		--timeout 5m
	@echo ">>> kro Deployment created; pod will become Ready after \`make init-provider-infrastructure\` writes the real kcp-kubeconfig Secret"
	@$(MAKE) dev-kro-register-self
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

## Register the kind cluster as a member of itself by writing its INTERNAL
## kubeconfig (the one with the docker-network address kro pods can reach,
## not 127.0.0.1) as a labeled Secret kro watches.
dev-kro-register-self: ## Create the self-member Secret (labeled kro.run/cluster=true)
	@test -f $(KRO_KIND_KUBECONFIG) || { \
		echo "no kubeconfig at $(KRO_KIND_KUBECONFIG); run 'make dev-kro-up' first"; \
		exit 1; \
	}
	@echo ">>> writing self-member Secret (in-cluster kubeconfig)"
	@INTERNAL_KUBECONFIG=$$(kind get kubeconfig --internal --name $(KRO_KIND_NAME)); \
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(KRO_NAMESPACE) \
		create secret generic self-cluster \
		--from-literal=kubeconfig="$$INTERNAL_KUBECONFIG" \
		--dry-run=client -o yaml | \
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl apply -f -
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(KRO_NAMESPACE) \
		label secret self-cluster kro.run/cluster=true --overwrite

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
dev-kro-down: ## Delete the kedge-kro kind cluster + kubeconfig file
	-kind delete cluster --name $(KRO_KIND_NAME)
	-rm -f $(KRO_KIND_KUBECONFIG)

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
dev-kro-up-into: ## Install kro into an existing cluster ($KRO_TARGET_KUBECONFIG)
	@command -v helm >/dev/null || { echo "helm not found; install: brew install helm"; exit 1; }
	@test -n "$(KRO_TARGET_KUBECONFIG)" || { echo "KRO_TARGET_KUBECONFIG not set"; exit 1; }
	@test -n "$(KRO_TARGET_KIND_NAME)" || { echo "KRO_TARGET_KIND_NAME not set"; exit 1; }
	@test -f "$(KRO_TARGET_KUBECONFIG)" || { echo "no kubeconfig at $(KRO_TARGET_KUBECONFIG)"; exit 1; }
	@echo ">>> ensuring $(KRO_NAMESPACE) + placeholder kcp-kubeconfig Secret in $(KRO_TARGET_KIND_NAME)"
	KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl create namespace $(KRO_NAMESPACE) \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl apply -f -
	KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl -n $(KRO_NAMESPACE) create secret generic kcp-kubeconfig \
		--from-literal=kubeconfig=pending-init \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl apply -f -
	@echo ">>> installing kro into $(KRO_NAMESPACE) (in-cluster mode, no --wait)"
	KUBECONFIG=$(KRO_TARGET_KUBECONFIG) helm upgrade --install kro $(KRO_CHART) \
		--version $(KRO_CHART_VERSION) \
		--namespace $(KRO_NAMESPACE) --create-namespace \
		--set image.repository=$(KRO_IMAGE_REPO) \
		--set image.tag=$(KRO_IMAGE_TAG) \
		--set multicluster.enabled=true \
		--set multicluster.provider=kcp-apiexport \
		--set multicluster.kcp.kubeconfigSecret=kcp-kubeconfig \
		--set multicluster.kcp.apiExportEndpointSlice=infrastructure \
		--set controller.deployToLocalRuntime=true \
		--timeout 5m
	@# kro pods need to resolve kcp.localhost/root.kcp.localhost/theseus.kcp.localhost
	@# (which the operator-issued kubeconfigs bake in) to the envoy gateway
	@# ClusterIP 10.96.2.2 — same trick the hub uses. The chart doesn't expose
	@# hostAliases as a value, so we patch the Deployment in place. Idempotent:
	@# strategic merge replaces the list, so re-runs just re-write the same map.
	@echo ">>> patching kro Deployment with hostAliases (kcp.localhost → envoy 10.96.2.2)"
	KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl -n $(KRO_NAMESPACE) patch deploy kro --type=strategic -p '{"spec":{"template":{"spec":{"hostAliases":[{"ip":"10.96.2.2","hostnames":["kcp.localhost","root.kcp.localhost","theseus.kcp.localhost"]}]}}}}'
	@echo ">>> registering $(KRO_TARGET_KIND_NAME) as self-member (using --internal kubeconfig)"
	@INTERNAL_KUBECONFIG=$$(kind get kubeconfig --internal --name $(KRO_TARGET_KIND_NAME)); \
	KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl -n $(KRO_NAMESPACE) create secret generic self-cluster \
		--from-literal=kubeconfig="$$INTERNAL_KUBECONFIG" \
		--dry-run=client -o yaml | KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl apply -f -
	KUBECONFIG=$(KRO_TARGET_KUBECONFIG) kubectl -n $(KRO_NAMESPACE) \
		label secret self-cluster kro.run/cluster=true --overwrite
	@echo ">>> kro ready in $(KRO_TARGET_KIND_NAME):$(KRO_NAMESPACE); becomes Ready after init-provider-infrastructure writes the real kcp-kubeconfig"
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
	@echo "=== Kedge Hub Development Modes ==="
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

docker-build-hub: ## Build kedge-hub container image
	docker build -f deploy/Dockerfile.hub \
		--platform $(DOCKER_PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t ghcr.io/faroshq/kedge-hub:$(VERSION) .

docker-build-agent: ## Build kedge-agent container image
	docker build -f deploy/Dockerfile.agent \
		--platform $(DOCKER_PLATFORM) \
		--build-arg VERSION=$(VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t ghcr.io/faroshq/kedge-agent:$(VERSION) .

docker-push-hub: docker-build-hub ## Build and push kedge-hub container image
	docker push ghcr.io/faroshq/kedge-hub:$(VERSION)

docker-push-agent: docker-build-agent ## Build and push kedge-agent container image
	docker push ghcr.io/faroshq/kedge-agent:$(VERSION)

docker-build-dex: ## Build kedge-dex container image (custom dex with branded web overlay)
	cd hack/dex && docker build \
		--platform $(DOCKER_PLATFORM) \
		-f Dockerfile \
		-t ghcr.io/faroshq/kedge-dex:$(VERSION) .

docker-push-dex: docker-build-dex ## Build and push kedge-dex container image
	docker push ghcr.io/faroshq/kedge-dex:$(VERSION)

docker-push: docker-push-hub docker-push-agent ## Build and push all container images

clean:
	rm -rf $(BINDIR)
	rm -rf $(TOOLSDIR)
	rm -rf tmp
	-kind delete cluster --name kedge-agent 2>/dev/null

path: ## Print export command to add bin/ to PATH
	@echo 'export PATH=$(CURDIR)/$(BINDIR):$$PATH'

verify: verify-boilerplate verify-codegen vet lint build test ## Run all checks

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
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/kedge-hub:test .
	docker build -f deploy/Dockerfile.agent -t ghcr.io/faroshq/kedge-agent:test .
	KEDGE_HUB_IMAGE=ghcr.io/faroshq/kedge-hub \
	KEDGE_HUB_IMAGE_TAG=test \
	KEDGE_HUB_IMAGE_PULL_POLICY=Never \
	KEDGE_AGENT_IMAGE=ghcr.io/faroshq/kedge-agent \
	KEDGE_AGENT_IMAGE_TAG=test \
	KEDGE_AGENT_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/standalone/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-ssh: build ## Run SSH server-mode e2e suite (hub-only cluster)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/kedge-hub:test .
	KEDGE_HUB_IMAGE=ghcr.io/faroshq/kedge-hub \
	KEDGE_HUB_IMAGE_TAG=test \
	KEDGE_HUB_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/ssh/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-oidc: build ## Run OIDC e2e suite (Dex OIDC provider, requires --with-dex cluster)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/kedge-hub:test .
	KEDGE_HUB_IMAGE=ghcr.io/faroshq/kedge-hub \
	KEDGE_HUB_IMAGE_TAG=test \
	KEDGE_HUB_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/oidc/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-external-kcp: build ## Run external KCP e2e suite (kcp via Helm in kind, push-to-main only in CI)
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/kedge-hub:test .
	KEDGE_HUB_IMAGE=ghcr.io/faroshq/kedge-hub \
	KEDGE_HUB_IMAGE_TAG=test \
	KEDGE_HUB_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/external_kcp/... -v -timeout $(E2E_TIMEOUT) $(if $(E2E_FLAGS),-args $(E2E_FLAGS))

e2e-all: build ## Run all e2e suites
	docker build -f deploy/Dockerfile.hub -t ghcr.io/faroshq/kedge-hub:test .
	docker build -f deploy/Dockerfile.agent -t ghcr.io/faroshq/kedge-agent:test .
	KEDGE_HUB_IMAGE=ghcr.io/faroshq/kedge-hub \
	KEDGE_HUB_IMAGE_TAG=test \
	KEDGE_HUB_IMAGE_PULL_POLICY=Never \
	KEDGE_AGENT_IMAGE=ghcr.io/faroshq/kedge-agent \
	KEDGE_AGENT_IMAGE_TAG=test \
	KEDGE_AGENT_IMAGE_PULL_POLICY=Never \
	go test ./test/e2e/suites/... -v -timeout 30m $(E2E_FLAGS)

e2e-keep: ## Run standalone e2e, keep clusters on failure for debugging
	$(MAKE) e2e-standalone E2E_FLAGS="--keep-clusters"
