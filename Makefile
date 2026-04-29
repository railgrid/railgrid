.PHONY: dev-edge-create dev-run-edge build test lint fix-lint codegen crds clean certs dev-setup run-dex run-hub run-hub-static run-hub-embedded run-hub-embedded-static run-hub-standalone run-hub-embedded-graphql run-kcp dev-login dev-login-static dev-create-workload dev dev-infra dev-run-kcp path boilerplate verify-boilerplate verify-codegen ldflags tools docker-build docker-build-hub docker-build-agent verify help-dev dev-status dev-clean-hooks helm-build-local helm-push-local helm-clean

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

build-hub:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/kedge-hub ./cmd/kedge-hub/

build-hub-portal: build-portal ## Build hub with embedded portal
	cp -r portal/dist pkg/hub/portal/
	go build $(GOFLAGS) -tags portal_embed -ldflags "$(LDFLAGS)" -o $(BINDIR)/kedge-hub ./cmd/kedge-hub/

build-portal: ## Build the portal Vue.js SPA
	cd portal && npm ci && npm run build

dev-portal: ## Run the portal dev server
	cd portal && npm run dev

build-graphql: ## Build the GraphQL gateway binary (listener + gateway subcommands)
	go build $(GOFLAGS) -o $(BINDIR)/kedge-graphql ./cmd/graphql/

# build-agent is an alias for build-kedge: the agent container image now ships
# the kedge CLI binary (cmd/kedge/) with ENTRYPOINT [/kedge, agent, run].
build-agent: build-kedge

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

DEV_EDGE_NAME ?= dev-edge-1
# TYPE selects the Edge type for dev-edge-create and dev-run-edge.
# Values: kubernetes (default) | server
TYPE ?= kubernetes

dev-edge-create: build-kedge ## Create an Edge resource: TYPE=kubernetes (default) or TYPE=server
	PATH=$(CURDIR)/$(BINDIR):$$PATH BINDIR=$(CURDIR)/$(BINDIR) hack/scripts/dev-edge-setup.sh $(DEV_EDGE_NAME) $(TYPE) "env=dev,provider=local"

dev-run-edge: build-kedge ## Run the edge agent: TYPE=kubernetes (default) or TYPE=server
	@test -f .env.edge || (echo "Run 'make dev-edge-create [TYPE=$(TYPE)]' first"; exit 1)
ifeq ($(TYPE),server)
	$(BINDIR)/kedge agent run \
		--hub-url=https://localhost:9443 \
		--hub-insecure-skip-tls-verify \
		--token=$(KEDGE_EDGE_JOIN_TOKEN) \
		--tunnel-url=https://localhost:9443 \
		--edge-name=$(DEV_EDGE_NAME) \
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
		--edge-name=$(DEV_EDGE_NAME) \
		--kubeconfig=.kubeconfig-kedge-agent \
		--cluster=$(KEDGE_EDGE_CLUSTER) \
		--type=kubernetes
endif

dev-create-workload: ## Create a demo VirtualWorkload targeting dev sites
	kubectl apply -f hack/dev/examples/virtualworkload-nginx.yaml

-include .env
-include .env.edge
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
	@echo "ENVIRONMENT VARIABLES:"
	@echo "  STATIC_AUTH_TOKEN  - Token for static auth (default: dev-token)"
	@echo "  KCP_DATA_DIR       - Directory for kcp data (default: .kcp)"
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
