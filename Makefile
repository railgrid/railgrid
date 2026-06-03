.PHONY: dev-edge-create dev-run-edge build test lint fix-lint codegen crds clean certs dev-setup run-dex run-hub run-hub-static run-hub-embedded run-hub-embedded-static run-hub-standalone run-hub-embedded-graphql run-kcp dev-login dev-login-static dev-create-workload dev dev-infra dev-run-kcp path boilerplate verify-boilerplate verify-codegen ldflags tools docker-build docker-build-hub docker-build-agent docker-build-dex docker-push-dex verify help-dev dev-status dev-clean-hooks helm-build-local helm-push-local helm-clean build-quickstart-provider build-quickstart-provider-portal run-provider-quickstart install-provider-quickstart uninstall-provider-quickstart build-infrastructure-provider build-infrastructure-provider-portal run-provider-infrastructure install-provider-infrastructure uninstall-provider-infrastructure dev-kro-up dev-kro-down dev-kro-seed dev-kro-register-self e2e-infrastructure portal-provider-symlinks build-mcp-provider-portal build-kubernetes-edges-provider-portal build-server-edges-provider-portal e2e-provider e2e-provider-flags e2e-provider-all

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

build-infrastructure-provider-portal: ## Build the infrastructure provider's micro-frontend (Vite + Vue → portal/dist)
	cd providers/infrastructure/portal && npm install --no-audit --no-fund && npm run build

build-infrastructure-provider: build-infrastructure-provider-portal ## Build the infrastructure provider binary (portal embedded)
	cd providers/infrastructure && go build $(GOFLAGS) -o $(CURDIR)/$(BINDIR)/infrastructure-provider .

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

codegen: crds boilerplate ## Generate all (CRDs + kcp resources + boilerplate)

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
		($(KCP) start --root-directory=$(KCP_DATA_DIR) --feature-gates=WorkspaceMounts=true & \
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
	--static-auth-token=$(STATIC_AUTH_TOKEN)

# KCP: External (requires running kcp separately)
HUB_FLAGS_KCP_EXTERNAL := \
	--external-kcp-kubeconfig=.kcp/admin.kubeconfig

# KCP: Embedded (runs kcp in-process)
HUB_FLAGS_KCP_EMBEDDED := \
	--embedded-kcp \
	--kcp-root-dir=.kcp \
	--kcp-secure-port=6443

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
QUICKSTART_MANIFEST ?= providers/quickstart/manifest.yaml

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
install-provider-quickstart: ## Apply quickstart CatalogEntry into root:kedge:providers
	@test -f $(QUICKSTART_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(QUICKSTART_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=https://localhost:6443/clusters/root:kedge:providers \
		--insecure-skip-tls-verify \
		apply -f $(QUICKSTART_MANIFEST)

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

## Delete the quickstart CatalogEntry. Useful while iterating on the manifest.
uninstall-provider-quickstart: ## Delete quickstart CatalogEntry
	-kubectl --kubeconfig=$(QUICKSTART_KCP_KUBECONFIG) \
		--server=https://localhost:6443/clusters/root:kedge:providers \
		--insecure-skip-tls-verify \
		delete -f $(QUICKSTART_MANIFEST)

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
KROMC_MANIFEST ?= providers/infrastructure/manifest.yaml

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
	KRO_KUBECONFIG=$${KRO_KUBECONFIG:-$$( [ -f "$(KRO_KIND_KUBECONFIG)" ] && echo "$(KRO_KIND_KUBECONFIG)" )} \
		$(BINDIR)/infrastructure-provider

## Apply the infrastructure CatalogEntry into root:kedge:providers. Idempotent.
## Requires the hub to be running so the admin kubeconfig exists.
install-provider-infrastructure: ## Apply infrastructure CatalogEntry into root:kedge:providers
	@test -f $(KROMC_KCP_KUBECONFIG) || { \
		echo "kubeconfig not found at $(KROMC_KCP_KUBECONFIG)"; \
		echo "start the hub first with: make run-hub-embedded-static"; \
		exit 1; \
	}
	kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=https://localhost:6443/clusters/root:kedge:providers \
		--insecure-skip-tls-verify \
		apply -f $(KROMC_MANIFEST)

## Delete the infrastructure CatalogEntry. Useful while iterating on the manifest.
uninstall-provider-infrastructure: ## Delete infrastructure CatalogEntry
	-kubectl --kubeconfig=$(KROMC_KCP_KUBECONFIG) \
		--server=https://localhost:6443/clusters/root:kedge:providers \
		--insecure-skip-tls-verify \
		delete -f $(KROMC_MANIFEST)

# --- Management kro cluster (backend for the infrastructure provider) ---
# Brings up a dedicated kind cluster running the faroshq/infrastructure
# fork (image + chart published to ghcr.io/faroshq/infrastructure/*),
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
KRO_CHART ?= oci://ghcr.io/faroshq/infrastructure/charts/kro/kro
KRO_CHART_VERSION ?= v0.0.1-mc.1
KRO_IMAGE_REPO ?= ghcr.io/faroshq/infrastructure/kro
KRO_IMAGE_TAG ?= v0.0.1-mc.1
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
	@echo ">>> installing kro (fork, multicluster mode) into $(KRO_NAMESPACE)"
	@# args[] override comes from docs/multicluster-setup.md:
	@#   --enable-multicluster              turn on the discovery loop
	@#   --cluster-secrets-namespace=...    where to look for kubeconfig Secrets
	@#   --cluster-secrets-label=...        label used to identify those Secrets
	@#   --cluster-secrets-key=...          key in Secret.data holding the YAML
	@# image.repository/tag pin the fork's controller image (the chart's
	@# default ${VERSION} placeholder doesn't resolve outside the release
	@# pipeline).
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) helm upgrade --install kro $(KRO_CHART) \
		--version $(KRO_CHART_VERSION) \
		--namespace $(KRO_NAMESPACE) --create-namespace \
		--set image.repository=$(KRO_IMAGE_REPO) \
		--set image.tag=$(KRO_IMAGE_TAG) \
		--set 'controller.args[0]=--enable-multicluster' \
		--set 'controller.args[1]=--cluster-secrets-namespace=$(KRO_NAMESPACE)' \
		--set 'controller.args[2]=--cluster-secrets-label=kro.run/cluster' \
		--set 'controller.args[3]=--cluster-secrets-key=kubeconfig' \
		--wait --timeout 5m
	@echo ">>> waiting for kro controller to become Ready"
	KUBECONFIG=$(KRO_KIND_KUBECONFIG) kubectl -n $(KRO_NAMESPACE) wait \
		--for=condition=Available --timeout=120s deployment/kro
	@$(MAKE) dev-kro-register-self
	@$(MAKE) dev-kro-seed
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
	@echo "ENVIRONMENT VARIABLES:"
	@echo "  STATIC_AUTH_TOKEN  - Token for static auth (default: dev-token)"
	@echo "  KCP_DATA_DIR       - Directory for kcp data (default: .kcp)"
	@echo "  QUICKSTART_PORT    - Port the quickstart provider listens on (default: 8081)"
	@echo "  QUICKSTART_HUB_URL - Hub URL the provider heartbeats to (default: https://localhost:9443)"
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
