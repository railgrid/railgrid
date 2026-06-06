# Tiltfile — local dev for kedge hub + portal + edges
# Replaces: make run-hub-embedded-static (terminal 1) + make dev-portal (terminal 2)
# Usage: tilt up

trigger_mode(TRIGGER_MODE_AUTO)

# ---------------------------------------------------------------------------
# portal — Vue.js SPA dev server on :3000
# ---------------------------------------------------------------------------
local_resource(
    'portal',
    cmd='make portal-provider-symlinks',
    serve_cmd='cd portal && npx vite --strictPort',
    deps=[
        'portal/src',
        'portal/package.json',
        'portal/index.html',
        'portal/vite.config.ts',
        'providers/mcp/portal/src',
        'providers/kubernetesedges/portal/src',
        'providers/serveredges/portal/src',
    ],
    labels=['hub'],
)

# ---------------------------------------------------------------------------
# hub — kedge-hub binary (embedded KCP, static auth, embedded GraphQL, portal proxy)
# ---------------------------------------------------------------------------
local_resource(
    'hub',
    cmd='''
make certs && \
mkdir -p providers/mcp/portal/dist providers/kubernetesedges/portal/dist providers/serveredges/portal/dist portal/dist && \
go build -o bin/kedge-hub ./cmd/kedge-hub
''',
    serve_cmd='''./bin/kedge-hub \
  --serving-cert-file=certs/apiserver.crt \
  --serving-key-file=certs/apiserver.key \
  --hub-external-url=https://localhost:9443 \
  --dev-mode -v 4 \
  --static-auth-token=dev-token \
  --embedded-kcp \
  --kcp-root-dir=.kcp \
  --kcp-secure-port=6443 \
  --embedded-graphql \
  --graphql-apiexport-slice-name=core.faros.sh \
  --graphql-apiexport-logical-cluster=root:kedge:providers \
  --graphql-grpc-addr=localhost:50051 \
  --graphql-playground \
  --portal-dev-url=http://localhost:3000
''',
    deps=[
        'cmd/kedge-hub',
        'pkg',
        'apis',
        'go.mod',
        'go.sum',
        'providers/mcp',
        'providers/kubernetesedges',
        'providers/serveredges',
    ],
    resource_deps=['portal'],
    labels=['hub'],
)

# ---------------------------------------------------------------------------
# providers — external (Helm-style) provider binaries that register with
# the hub via a CatalogEntry and serve their UI + HTTP API on a host
# port. Resources are split by provider for clarity in the Tilt UI:
#
#   providers-quickstart   — the reference example (port :8081)
#   providers-kro          — infrastructure broker (port :8082) +
#                            management kind cluster that kro runs in
#
# Each provider has three resources:
#   <name>            build + serve; auto-restarts on src change
#   <name>-register   manual ▶ to kubectl apply the CatalogEntry
#   <name>-unregister manual ▶ to kubectl delete it
#
# The kro group adds two more for the backing infrastructure:
#   kro-mgmt-up    builds the kind cluster + helm-installs kro +
#                  applies seed RGDs (see providers/infrastructure/
#                  examples/rgds/). Auto-runs at `tilt up`.
#   kro-mgmt-down  manual ▶ to tear down (kind delete cluster).
#
# Wiring: the `infrastructure` provider resource_deps on `kro-mgmt-up`
# so the provider starts AFTER the kro management cluster is reachable
# and the seed RGDs are applied. The provider's `make run-...` target
# auto-detects the .kedge-kro.kubeconfig file and passes it as
# KRO_KUBECONFIG, so the catalog UI shows the real seeded RGDs.
# ---------------------------------------------------------------------------

# --- providers-quickstart ---
local_resource(
    'quickstart',
    cmd='make build-quickstart-provider',
    serve_cmd='make run-provider-quickstart',
    deps=[
        'providers/quickstart/main.go',
        'providers/quickstart/assets.go',
        'providers/quickstart/portal/src',
        'providers/quickstart/portal/package.json',
        'providers/quickstart/go.mod',
    ],
    resource_deps=['hub'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=8081, path='/healthz'),
    ),
    labels=['providers-quickstart'],
)

local_resource(
    'quickstart-register',
    cmd='make install-provider-quickstart',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-quickstart'],
)

local_resource(
    'quickstart-unregister',
    cmd='make uninstall-provider-quickstart',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-quickstart'],
)

# --- providers-kro ---
# Management kro cluster: a kind cluster running upstream kro from
# oci://ghcr.io/kro-run/kro/kro plus the sample RGDs under
# providers/infrastructure/examples/rgds/. The first run pulls
# images and bootstraps the cluster (~30–60s on a clean machine);
# subsequent runs short-circuit when `kind get clusters` matches.
local_resource(
    'kro-mgmt-up',
    cmd='make dev-kro-up',
    deps=[
        'providers/infrastructure/examples/rgds',
    ],
    labels=['providers-kro'],
)

local_resource(
    'kro-mgmt-down',
    cmd='make dev-kro-down',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['providers-kro'],
)

local_resource(
    'infrastructure',
    cmd='make build-infrastructure-provider',
    serve_cmd='make run-provider-infrastructure',
    deps=[
        'providers/infrastructure/main.go',
        'providers/infrastructure/heartbeat.go',
        'providers/infrastructure/assets.go',
        'providers/infrastructure/server',
        'providers/infrastructure/kro',
        'providers/infrastructure/tenant',
        'providers/infrastructure/mcpserver',
        'providers/infrastructure/portal/src',
        'providers/infrastructure/portal/package.json',
        'providers/infrastructure/go.mod',
        # Restart whenever init writes/updates the runtime kubeconfig:
        # the controller manager only starts when INFRASTRUCTURE_KUBECONFIG
        # resolves to a real file (see the Makefile target). Without this
        # watch, the provider that booted before `infrastructure-init`
        # keeps running with controllers disabled, and per-template
        # APIResourceSchemas never get added to the APIExport.
        '.kcp/infrastructure-runtime.kubeconfig',
    ],
    # hub for CatalogEntry registration target, kro-mgmt-up for the
    # backend cluster the catalog reads from. Both must be green
    # before the provider starts; otherwise it boots in stub mode
    # which is fine but confusing for the dev who just ran `tilt up`.
    resource_deps=['hub', 'kro-mgmt-up'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=8082, path='/healthz'),
    ),
    labels=['providers-kro'],
)

local_resource(
    'infrastructure-register',
    cmd='make install-provider-infrastructure',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-kro'],
)

# One-shot bootstrap: installs CRDs, registers APIExport schemas,
# applies the Templates CachedResource, mints the runtime SA + token,
# writes the kubeconfig run-provider-infrastructure reads. Order is:
#   infrastructure-register  → creates the workspace
#   infrastructure-init      → seeds the workspace + writes kubeconfig
#   infrastructure (long-lived) → picks up INFRASTRUCTURE_KUBECONFIG
# Manual so devs control when re-bootstrap happens (it overwrites
# the runtime kubeconfig and rotates the token).
local_resource(
    'infrastructure-init',
    cmd='make init-provider-infrastructure',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub', 'infrastructure-register'],
    labels=['providers-kro'],
)

local_resource(
    'infrastructure-unregister',
    cmd='make uninstall-provider-infrastructure',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-kro'],
)

# ---------------------------------------------------------------------------
# edges — kubernetes & server agents. All manual triggers (click ▶ in Tilt UI).
#
# Workflow:
#   1. Click ▶ on `edge-{kube,server}-create` to log in via static token,
#      register the Edge with the hub, and write .env.edge.<type>.
#   2. Click ▶ on `edge-{kube,server}-agent` to run the agent.
#        - kubernetes: also spins up a `kedge-agent` kind cluster on first run.
#        - server: also click ▶ on `ssh-server` so the agent has an SSH target.
# ---------------------------------------------------------------------------
local_resource(
    'edge-kube-create',
    cmd='make dev-login-static && make dev-edge-create TYPE=kubernetes',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['edges'],
)

local_resource(
    'edge-kube-agent',
    serve_cmd='make dev-run-edge TYPE=kubernetes',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['edges'],
)

local_resource(
    'edge-server-create',
    cmd='make dev-login-static && make dev-edge-create TYPE=server',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['edges'],
)

local_resource(
    'edge-server-agent',
    serve_cmd='make dev-run-edge TYPE=server',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['edges'],
)

# openssh-server container — target for the server-edge agent.
# Pre-step removes any stale container left from a previous run so the
# named --name=openssh-server doesn't collide.
local_resource(
    'ssh-server',
    serve_cmd='docker rm -f openssh-server >/dev/null 2>&1; make dev-run-ssh-server',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['edges'],
)
