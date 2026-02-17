.PHONY: build test lint fix-lint codegen crds clean certs dev-setup run-dex run-hub run-kcp dev-login dev-site-create dev-create-workload dev-run-agent dev dev-infra path boilerplate verify-boilerplate verify-codegen ldflags tools

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

GOLANGCI_LINT_VER := v2.9.0
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
LDFLAGS_PKG := github.com/faroshq/faros-kedge/pkg/cli/cmd
LDFLAGS := -s -w -X $(LDFLAGS_PKG).version=$(VERSION) -X $(LDFLAGS_PKG).gitCommit=$(GIT_COMMIT) -X $(LDFLAGS_PKG).buildDate=$(BUILD_DATE)

ldflags: ## Print ldflags for goreleaser
	@echo "$(LDFLAGS)"

all: build

build: build-kedge build-hub build-agent

build-kedge:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/kedge ./cmd/kedge/

build-hub:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/kedge-hub ./cmd/kedge-hub/

build-agent:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINDIR)/kedge-agent ./cmd/kedge-agent/

test:
	go test ./...

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
	PATH=$(CURDIR)/$(BINDIR):$$PATH $(BINDIR)/kedge login --hub-url https://localhost:8443 --insecure-skip-tls-verify

DEV_SITE_NAME ?= dev-site-1

dev-site-create: build-kedge
	PATH=$(CURDIR)/$(BINDIR):$$PATH BINDIR=$(CURDIR)/$(BINDIR) hack/scripts/dev-site-setup.sh $(DEV_SITE_NAME) "env=dev,provider=local"

dev-create-workload: ## Create a demo VirtualWorkload targeting dev sites
	kubectl apply -f hack/dev/examples/virtualworkload-nginx.yaml

-include .env
export

dev-run-agent: build-agent
	@test -f .env || (echo "Run 'make dev-site-create' first"; exit 1)
	hack/scripts/ensure-kind-cluster.sh
	$(BINDIR)/kedge-agent join \
		--hub-kubeconfig=$(KEDGE_SITE_KUBECONFIG) \
		--kubeconfig=.kubeconfig-kedge-agent \
		--tunnel-url=https://localhost:8443 \
		--site-name=$(KEDGE_SITE_NAME) \
		--labels=$(KEDGE_LABELS)


dev-infra: $(KCP) $(DEX) certs ## Run infra only (kcp + Dex)
	hack/scripts/dev-infra.sh

dev-run-kcp: $(KCP)
	$(KCP) start --root-directory=$(KCP_DATA_DIR) --feature-gates=WorkspaceMounts=true


run-dex: $(DEX) certs
	$(DEX) serve hack/dev/dex/dex-config-dev.yaml

run-hub: build-hub certs
	$(BINDIR)/kedge-hub \
		--dex-issuer-url=https://localhost:5554/dex \
		--dex-client-id=kedge \
		--dex-client-secret=ZXhhbXBsZS1hcHAtc2VjcmV0 \
		--serving-cert-file=certs/apiserver.crt \
		--serving-key-file=certs/apiserver.key \
		--hub-external-url=https://localhost:8443 \
		--external-kcp-kubeconfig=.kcp/admin.kubeconfig \
		--dev-mode

clean:
	rm -rf $(BINDIR)
	rm -rf $(TOOLSDIR)
	rm -rf tmp
	-kind delete cluster --name kedge-agent 2>/dev/null

path: ## Print export command to add bin/ to PATH
	@echo 'export PATH=$(CURDIR)/$(BINDIR):$$PATH'

verify: verify-boilerplate verify-codegen vet lint build test ## Run all checks
