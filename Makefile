.PHONY: build test lint codegen crds clean certs dev-setup run-dex run-hub run-kcp dev-login dev-site-create dev-create-workload dev-run-agent dev dev-infra dev-hub path

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

AIR_VER := v1.64.5
AIR_BIN := air
AIR := $(TOOLSDIR)/$(AIR_BIN)-$(AIR_VER)

OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH := $(shell uname -m)
ifeq ($(ARCH),x86_64)
  ARCH := amd64
endif
ifeq ($(ARCH),aarch64)
  ARCH := arm64
endif

all: build

build: build-kedge build-hub build-agent

build-kedge:
	go build $(GOFLAGS) -o $(BINDIR)/kedge ./cmd/kedge/

build-hub:
	go build $(GOFLAGS) -o $(BINDIR)/kedge-hub ./cmd/kedge-hub/

build-agent:
	go build $(GOFLAGS) -o $(BINDIR)/kedge-agent ./cmd/kedge-agent/

test:
	go test ./...

test-util:
	go test ./pkg/util/...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

# --- Code generation ---

crds: $(CONTROLLER_GEN) $(KCP_APIGEN_GEN) ## Generate CRDs and KCP APIResourceSchemas
	./hack/update-codegen-crds.sh

codegen: crds ## Generate all (CRDs + KCP resources)

# --- Tool installation ---

$(CONTROLLER_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) sigs.k8s.io/controller-tools/cmd/controller-gen $(CONTROLLER_GEN_BIN) $(CONTROLLER_GEN_VER)

$(KCP_APIGEN_GEN):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/kcp-dev/sdk/cmd/apigen $(KCP_APIGEN_BIN) $(KCP_APIGEN_VER)

$(AIR):
	GOBIN=$(TOOLS_GOBIN_DIR) $(GO_INSTALL) github.com/air-verse/air $(AIR_BIN) $(AIR_VER)

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
	@echo "Downloading KCP $(KCP_VER) for $(OS)/$(ARCH)..."
	curl -sL "https://github.com/kcp-dev/kcp/releases/download/$(KCP_VER)/kcp_$(subst v,,$(KCP_VER))_$(OS)_$(ARCH).tar.gz" | \
		tar xz -C $(TOOLSDIR) bin/kcp
	mv $(TOOLSDIR)/bin/kcp $(KCP)
	rmdir $(TOOLSDIR)/bin 2>/dev/null || true
	chmod +x $(KCP)
	ln -sf $(notdir $(KCP)) $(TOOLSDIR)/kcp
	@echo "KCP binary: $(KCP)"

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

run-kcp: $(KCP)
	$(KCP) start --root-directory=$(KCP_DATA_DIR) --feature-gates=WorkspaceMounts=true

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
		--kubeconfig=.kind-kubeconfig \
		--tunnel-url=https://localhost:8443 \
		--site-name=$(KEDGE_SITE_NAME) \
		--labels=$(KEDGE_LABELS)


dev-infra: $(KCP) $(DEX) certs ## Run infra only (KCP + Dex)
	hack/scripts/dev-infra.sh

dev-hub: $(AIR) certs ## Run hub + agent with hot reload (requires dev-infra running)
	@if [ -f .env ]; then hack/scripts/ensure-kind-cluster.sh; fi
	hack/scripts/dev-hub.sh

# dev runs everything in one terminal. KCP and Dex start once and stay up.
# Hub and Agent hot-reload on Go file changes via air.
# Usage: make dev
dev: $(AIR) $(KCP) $(DEX) certs ## Run full dev stack (KCP + Dex + Hub + Agent)
	@if [ -f .env ]; then hack/scripts/ensure-kind-cluster.sh; fi
	hack/scripts/dev-all.sh

clean:
	rm -rf $(BINDIR)
	rm -rf $(TOOLSDIR)
	rm -rf tmp
	-kind delete cluster --name kedge-agent 2>/dev/null

path: ## Print export command to add bin/ to PATH
	@echo 'export PATH=$(CURDIR)/$(BINDIR):$$PATH'

verify: vet lint test
