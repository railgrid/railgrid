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
        'providers/app-studio/portal/src',
        'providers/app-studio/portal/package.json',
        'providers/code/portal/src',
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
mkdir -p providers/mcp/portal/dist providers/kubernetesedges/portal/dist providers/serveredges/portal/dist providers/code/portal/dist portal/dist && \
go build -o bin/kedge-hub ./cmd/kedge-hub
''',
    serve_cmd='''./bin/kedge-hub \
  --serving-cert-file=certs/apiserver.crt \
  --serving-key-file=certs/apiserver.key \
  --hub-external-url=https://localhost:9443 \
  --dev-mode -v 4 \
  --static-auth-token=dev-token \
  --admin-users=static-dev-toke@kedge.local \
  --embedded-kcp \
  --kcp-root-dir=.kcp \
  --kcp-secure-port=6443 \
  --embedded-graphql \
  --graphql-apiexport-slice-name=core.faros.sh \
  --graphql-apiexport-logical-cluster=root:kedge:system:controllers \
  --graphql-grpc-addr=localhost:50051 \
  --graphql-playground \
  --portal-dev-url=http://localhost:3000 \
  --portal-frame-source=https://*.preview.localhost:10443 \
  --kubeconfig=.kedge-kro.kubeconfig \
  --provider-internal-url=https://host.docker.internal:9443
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
        # Restart the hub once the kedge-kro kubeconfig appears so the
        # HostSecretWriter (which delivers kedge-provider-kubeconfig into
        # that cluster) activates. The wiring is tolerant of the file being
        # absent at first boot — see pkg/hub/server.go.
        '.kedge-kro.kubeconfig',
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
#   providers-app-studio   — the AI workspace provider (port :8085)
#   providers-kro          — infrastructure broker (port :8082) +
#                            management kind cluster that kro runs in
#   providers-code         — git repository manager (port :8083)
#   providers-kuery        — fleet query engine (port :8084)
#
# Each provider has three resources:
#   <name>            build + serve; auto-restarts on src change
#   <name>-register   manual ▶ to kubectl apply the Provider + CatalogEntry.
#                     Applying the Provider CR is what provisions the
#                     sub-workspace + ServiceAccount + kubeconfig Secret —
#                     the hub's Provider controller does it declaratively
#                     (no admin "onboard" step anymore). The Provider
#                     (admin.kedge.faros.sh) is admin-only; the CatalogEntry
#                     (providers.kedge.faros.sh) is also bound into provider
#                     sub-workspaces so a provider can self-register it from
#                     inside. Both objects live in root:kedge:system:providers;
#                     in dev we apply Provider + CatalogEntry there for
#                     host-binary simplicity; in production the provider's init
#                     self-registers the CatalogEntry into its own workspace via
#                     KEDGE_CATALOGENTRY_FILE.
#   <name>-unregister manual ▶ to kubectl delete them (deleting the Provider
#                     triggers full teardown of the sub-workspace)
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

# Creates quickstart's APIExport (+ endpoint slice + bind grant) so tenants can
# Enable it. Run AFTER quickstart-register (which applies the Provider CR → the
# controller provisions the workspace + provider-token Secret this reads).
local_resource(
    'quickstart-init',
    cmd='make init-provider-quickstart',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub', 'quickstart-register'],
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

# --- providers-code (git repository management) ---
# Long-lived provider: serves the portal + MCP on :8083 and, once a kubeconfig
# is present, runs the multicluster controller manager. run-provider-code reads
# CODE_KUBECONFIG from .kcp/code-runtime.kubeconfig (written by code-init), and
# falls back to portal/MCP-only when it's absent — so this can start before the
# workspace exists.
local_resource(
    'code',
    cmd='make build-code-provider',
    serve_cmd='make run-provider-code',
    deps=[
        'providers/code/main.go',
        'providers/code/heartbeat.go',
        'providers/code/assets.go',
        'providers/code/controller_manager.go',
        'providers/code/init_cmd.go',
        'providers/code/server',
        'providers/code/tenant',
        'providers/code/mcpserver',
        'providers/code/controller',
        'providers/code/backend',
        'providers/code/install',
        'providers/code/scheme',
        'providers/code/oauthgithub',
        'providers/code/portal/src',
        'providers/code/portal/package.json',
        'providers/code/go.mod',
        'providers/code/go.sum',
        'providers/code/.env',
        '.kcp/code-runtime.kubeconfig',
    ],
    resource_deps=['hub'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=8083, path='/healthz'),
    ),
    labels=['providers-code'],
)

local_resource(
    'code-register',
    cmd='make install-provider-code',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-code'],
)

# Writes the dev kubeconfig (.kcp/code-runtime.kubeconfig) and ensures the
# APIExportEndpointSlice the controller manager watches. Order:
#   code-register  → creates root:kedge:providers:code
#   code-init      → writes kubeconfig + endpoint slice
#   code (serve)   → Tilt restarts it when the kubeconfig dep appears
local_resource(
    'code-init',
    cmd='make init-provider-code',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub', 'code-register'],
    labels=['providers-code'],
)

local_resource(
    'code-unregister',
    cmd='make uninstall-provider-code',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-code'],
)

# --- providers-app-studio ---
local_resource(
    'app-studio-db',
    cmd='make app-studio-db-up',
    deps=[
        'Makefile',
        'providers/app-studio/.env',
        'providers/app-studio/.env.example',
    ],
    resource_deps=['hub'],
    labels=['providers-app-studio'],
)

local_resource(
    'app-studio',
    cmd='make build-app-studio-provider',
    serve_cmd='make run-provider-app-studio',
    deps=[
        'providers/app-studio/main.go',
        'providers/app-studio/heartbeat.go',
        'providers/app-studio/assets.go',
        'providers/app-studio/api',
        'providers/app-studio/apis',
        'providers/app-studio/client',
        'providers/app-studio/store',
        'providers/app-studio/tenant',
        'providers/app-studio/runner',
        'providers/app-studio/Dockerfile.runner',
        'providers/app-studio/go.mod',
        'providers/app-studio/go.sum',
        'providers/app-studio/portal/src',
        'providers/app-studio/portal/package.json',
        'providers/app-studio/portal/vite.config.ts',
        'providers/app-studio/deploy/chart/templates/catalogentry.yaml',
        'providers/app-studio/deploy/chart/values.yaml',
        'providers/app-studio/.env',
    ],
    resource_deps=['hub', 'app-studio-db', 'sandbox-runner-image'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=8085, path='/healthz'),
    ),
    labels=['providers-app-studio'],
)

local_resource(
    'app-studio-db-down',
    cmd='make app-studio-db-down',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['providers-app-studio'],
)

local_resource(
    'app-studio-register',
    cmd='make install-provider-app-studio',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-app-studio'],
)

# Creates App Studio's APIExport (+ schemas + endpoint slice + bind grant) so
# tenants can Enable it. Run AFTER app-studio-register.
local_resource(
    'app-studio-init',
    cmd='make init-provider-app-studio',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub', 'app-studio-register'],
    labels=['providers-app-studio'],
)

local_resource(
    'app-studio-unregister',
    cmd='make uninstall-provider-app-studio',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-app-studio'],
)

# --- App Studio sandbox runner image (live development runtimes) ---
local_resource(
    'sandbox-runner-image',
    cmd='make load-sandbox-runner-image',
    deps=[
        'providers/app-studio/Dockerfile.runner',
        'providers/app-studio/go.mod',
        'providers/app-studio/go.sum',
        'providers/app-studio/runner',
    ],
    resource_deps=['kro-mgmt-up'],
    labels=['providers-app-studio'],
)

# --- providers-kuery (fleet query engine) ---
# Local Postgres for the kuery store. Dev always runs the same SQL backend
# as production — SQLite hid real Postgres-only query bugs, so it is not an
# option here. make run-provider-kuery also depends on this; the resource
# gives Tilt visibility + a restart button.
local_resource(
    'kuery-db',
    cmd='make kuery-db-up',
    deps=['Makefile'],
    resource_deps=['hub'],
    labels=['providers-kuery'],
)

# Long-lived provider embedding the kuery engine. Serves the portal +
# /api/query + MCP on :8084 immediately; the edge engagement controller
# additionally needs the dev runtime kubeconfig, minted by ▶ kuery-init
# AFTER ▶ kuery-register has been applied and reconciled. Tilt restarts
# the serve process when the kubeconfig file appears (it's in deps).
local_resource(
    'kuery',
    cmd='make build-kuery-provider',
    serve_cmd='make run-provider-kuery',
    deps=[
        'providers/kuery/main.go',
        'providers/kuery/assets.go',
        'providers/kuery/core',
        'providers/kuery/engagement',
        'providers/kuery/queryapi',
        'providers/kuery/mcpserver',
        'providers/kuery/portal/src',
        'providers/kuery/portal/package.json',
        'providers/kuery/go.mod',
        'providers/kuery/go.sum',
        '.kcp/kuery-runtime.kubeconfig',
    ],
    resource_deps=['hub', 'kuery-db'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=8084, path='/healthz'),
    ),
    labels=['providers-kuery'],
)

local_resource(
    'kuery-db-down',
    cmd='make kuery-db-down',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['providers-kuery'],
)

local_resource(
    'kuery-register',
    cmd='make install-provider-kuery',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-kuery'],
)

# Mints the dev runtime kubeconfig from the provider SA token (created by
# the Provider controller when kuery-register applies the Provider CR) and
# ensures the APIExportEndpointSlice the engagement controller discovers VW
# URLs from.
local_resource(
    'kuery-init',
    cmd='make init-provider-kuery',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub', 'kuery-register'],
    labels=['providers-kuery'],
)

local_resource(
    'kuery-unregister',
    cmd='make uninstall-provider-kuery',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-kuery'],
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
        # The operator path: the controller/manager, the bootstrap install
        # helpers, the embedded CRDs + seed Templates (install/), the API types,
        # and the kro backend. Without these, edits to the CRD schema, the seed
        # templates, or the controller don't trigger a rebuild and the running
        # binary embeds a stale CRD (kcp then prunes new fields like sampleValues).
        'providers/infrastructure/apis',
        'providers/infrastructure/install',
        'providers/infrastructure/controller',
        'providers/infrastructure/operator',
        'providers/infrastructure/backend',
        'providers/infrastructure/apps',
        'providers/infrastructure/portal/src',
        'providers/infrastructure/portal/package.json',
        'providers/infrastructure/go.mod',
        'providers/infrastructure/go.sum',
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

# --- EXPERIMENTAL: run the infrastructure provider as a POD (init-container
#     bootstrap) instead of the host binary above. Exercises the full
#     hub-minted flow end to end: the hub mints + delivers
#     kedge-provider-kubeconfig (HostSecretWriter, enabled by the hub's
#     --kubeconfig + --provider-internal-url flags above), the init container
#     bootstraps the workspace with it, then serve runs — all inside the
#     kedge-kro kind cluster.
#
#     Manual (click ▶). Order: kro-mgmt-up → infrastructure-register →
#     infrastructure-pod. Stop the host-binary `infrastructure` resource
#     first so two providers don't both serve as "infrastructure".
local_resource(
    'infrastructure-pod',
    cmd='make helm-deploy-provider-infrastructure',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub', 'kro-mgmt-up', 'infrastructure-register'],
    labels=['providers-kro'],
)

local_resource(
    'infrastructure-pod-down',
    cmd='make helm-undeploy-provider-infrastructure',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
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
    # Drop any saved agent kubeconfig from a previous hub/kcp incarnation
    # before re-creating. A stale ~/.kedge/agent-<edge>.kubeconfig points at
    # an old workspace + revoked SA token; the agent would load it, skip
    # re-registration, and fail every call with "workspace access not
    # permitted" (User ""). Clearing it forces a fresh join-token exchange.
    cmd='rm -f ~/.kedge/agent-dev-edge-kube-1.kubeconfig ~/.kedge/agent-dev-edge-kube-1.json && make dev-login-static && make dev-edge-create TYPE=kubernetes DEV_EDGE_NAME=dev-edge-kube-1',
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
    # Same stale-kubeconfig cleanup as edge-kube-create (see note there).
    cmd='rm -f ~/.kedge/agent-dev-edge-server-1.kubeconfig ~/.kedge/agent-dev-edge-server-1.json && make dev-login-static && make dev-edge-create TYPE=server DEV_EDGE_NAME=dev-edge-server-1',
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
