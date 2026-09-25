# Tiltfile — local dev for railgrid hub + portal + edges
# Replaces: make run-hub-embedded-static (terminal 1) + make dev-portal (terminal 2)
# Usage: tilt up

trigger_mode(TRIGGER_MODE_AUTO)

# Providers developed in another repository join this session when their
# checkout is given (docs/external-providers-tilt.md). They run as pods in the
# railgrid-kro kind cluster, so `make tilt` creates it before `tilt up` and
# selects its context. Without --external-providers every provider in the
# checkout is loaded:
#
#   make tilt EXTERNAL_PROVIDERS_DIR=../providers
#   make tilt EXTERNAL_PROVIDERS_DIR=../providers EXTERNAL_PROVIDERS=planner
config.define_string('external-providers-dir')
config.define_string('external-providers')
# Run kcp from a published image instead of the server compiled into the hub,
# so a kcp change can be tried without moving this repository's kcp dependency.
# The hub then starts with --external-kcp and the embedded server stays off.
#
# kcp pushes one image per PR commit to ghcr.io/kcp-dev/kcp-prs, tagged
# pr-<number>-<short sha>; a bare pr-<number> tag does not exist.
#
#   make tilt KCP_IMAGE=ghcr.io/kcp-dev/kcp-prs:pr-4388-4bce57376
#
# The external server keeps its own root directory, so switching starts from an
# EMPTY kcp: no workspaces, no providers, no tenants. Switch back by dropping
# the flag; the embedded server's .kcp directory is left untouched.
config.define_string('kcp-image')
cfg = config.parse()

kcp_image = cfg.get('kcp-image', '') or os.getenv('RAILGRID_KCP_IMAGE', '')
kcp_external_root = '.kcp-external'
kcp_external_kubeconfig = kcp_external_root + '/admin.kubeconfig'

# Public URL overrides keep the default sslip.io local loop intact while
# allowing a developer to put trusted DNS/TLS in front of the same dynamic
# virtual-host routing. The public app port may be explicitly empty when the
# external endpoint uses the normal HTTPS port.
railgrid_hub_external_url = os.getenv('RAILGRID_HUB_EXTERNAL_URL', 'https://console.127.0.0.1.sslip.io:9443')
preview_app_base_domain = os.getenv('RAILGRID_APP_BASE_DOMAIN', 'apps.127.0.0.1.sslip.io')
preview_gateway_port = os.getenv('PREVIEW_GATEWAY_PORT', '10443')
preview_app_public_port = os.getenv('RAILGRID_APP_PUBLIC_PORT', preview_gateway_port)
preview_app_public_port_suffix = (':' + preview_app_public_port) if preview_app_public_port else ''
preview_app_frame_source = 'https://*.' + preview_app_base_domain + preview_app_public_port_suffix
preview_hub_public_url = os.getenv(
    'RAILGRID_ACCESS_HUB_PUBLIC_URL',
    'https://console.127.0.0.1.sslip.io:9443',
)
preview_hub_public_host = preview_hub_public_url.replace('https://', '').replace('http://', '').split('/')[0].split(':')[0]
# CoreDNS sends browser-worker traffic directly to Envoy's ClusterIP. Expose
# the port advertised in app URLs there even when the internal listener and
# host port-forward stay on an unprivileged development port.
preview_gateway_service_port = preview_app_public_port or '443'

# ---------------------------------------------------------------------------
# portal — Vue.js SPA dev server on :3000
# ---------------------------------------------------------------------------
local_resource(
    'portal',
    # Reconcile the locked dependencies on startup and manifest changes. A
    # node_modules directory can exist even when an install is incomplete.
    cmd='cd portal && npm ci --include=dev --no-audit --no-fund',
    serve_cmd='make dev-portal ARGS=--strictPort',
    deps=[
        # Vite handles source changes with HMR; do not reinstall on each edit.
        'portal/package.json',
        'portal/package-lock.json',
        'portal/index.html',
        'portal/vite.config.ts',
    ],
    labels=['hub'],
)

# ---------------------------------------------------------------------------
# hub — railgrid-hub binary (embedded KCP, static auth, portal proxy)
# kcp — only when an external binary was named. It serves on the same port the
# embedded server uses, so every other kcp address in the Makefile and in the
# provider kubeconfigs keeps working, and the two can never run at once.
if kcp_image:
    local_resource(
        'kcp',
        serve_cmd='hack/kcp-external.sh %s %s 6443' % (kcp_image, kcp_external_root),
        readiness_probe=probe(
            period_secs=5,
            initial_delay_secs=10,
            exec=exec_action(['test', '-f', kcp_external_kubeconfig]),
        ),
        labels=['hub'],
    )
    kcp_server_flags = '  --external-kcp-kubeconfig=' + kcp_external_kubeconfig
    hub_deps = ['portal', 'kcp']
else:
    kcp_server_flags = '  --embedded-kcp \\\n  --kcp-root-dir=.kcp \\\n  --kcp-secure-port=6443'
    hub_deps = ['portal']

# ---------------------------------------------------------------------------
local_resource(
    'hub',
    cmd='''
make certs && \
go build -o bin/railgrid-hub ./cmd/railgrid-hub
''',
    serve_cmd=('''./bin/railgrid-hub \
  --serving-cert-file=certs/apiserver.crt \
  --serving-key-file=certs/apiserver.key \
  --hub-external-url=%s \
  --dev-mode -v 4 \
  --static-auth-token=dev-token \
  --static-auth-token=dev-token2 \
  --admin-users=railgrid:static:47b9dce0e91570a1 \
%s \
  --portal-dev-url=http://localhost:3000 \
  --portal-frame-source=%s \
  --published-apps-domain=%s \
  --kubeconfig=.railgrid-kro.kubeconfig \
  --hub-internal-url=https://host.docker.internal:9443
''' % (
        railgrid_hub_external_url,
        kcp_server_flags,
        preview_app_frame_source,
        preview_app_base_domain,
    )),
    deps=[
        'cmd/railgrid-hub',
        'pkg',
        'apis',
        # config/kcp and config/crds are embedded into the hub binary
        # (config/kcp/embed.go, pkg/hub/bootstrap/crds); a regenerated
        # APIResourceSchema must rebuild the hub or bootstrap keeps retrying
        # an immutable update forever and the portal never comes up.
        'config',
        'go.mod',
        'go.sum',
        # Restart the hub once the railgrid-kro kubeconfig appears so the
        # HostSecretWriter (which delivers railgrid-provider-kubeconfig into
        # that cluster) activates. The wiring is tolerant of the file being
        # absent at first boot — see pkg/hub/server.go.
        '.railgrid-kro.kubeconfig',
    ],
    # The standalone generic runner is reached through its enrolled Edge
    # Service and is not a hub dependency; runner edits must not restart KCP.
    ignore=['pkg/runner'],
    resource_deps=hub_deps,
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
#   providers-agents       — long-running personal AI agents (port :8087)
#
# Each provider has three resources:
#   <name>            build + serve; auto-restarts on src change
#   <name>-register   manual ▶ to kubectl apply the Provider + CatalogEntry.
#                     Applying the Provider CR is what provisions the
#                     sub-workspace + ServiceAccount + kubeconfig Secret —
#                     the hub's Provider controller does it declaratively
#                     (no admin "onboard" step anymore). The Provider
#                     (admin.railgrid.ai) is admin-only; the CatalogEntry
#                     (providers.railgrid.ai) is also bound into provider
#                     sub-workspaces so a provider can self-register it from
#                     inside. Both objects live in root:railgrid:system:providers;
#                     in dev we apply Provider + CatalogEntry there for
#                     host-binary simplicity; in production the provider's init
#                     self-registers the CatalogEntry into its own workspace via
#                     RAILGRID_CATALOGENTRY_FILE.
#   <name>-unregister manual ▶ to kubectl delete them (deleting the Provider
#                     triggers full teardown of the sub-workspace)
#
# The kro group adds two more for the backing infrastructure:
#   kro-mgmt-up    builds the kind cluster + helm-installs UPSTREAM kro
#                  (single-cluster; the provider's instance controller
#                  bridges kcp → this cluster). Auto-runs at `tilt up`.
#   kro-mgmt-down  manual ▶ to tear down (kind delete cluster).
#
# Wiring: the `infrastructure` provider resource_deps on `kro-mgmt-up`
# so the provider starts AFTER the kro management cluster is reachable.
# The provider's `make run-...` target auto-detects the
# .railgrid-kro.kubeconfig file and passes it as KRO_KUBECONFIG.
# ---------------------------------------------------------------------------

preview_gateway_name = 'app-studio-preview'
preview_gateway_namespace = 'envoy-gateway-system'
preview_kro_kubeconfig = '.railgrid-kro.kubeconfig'
preview_kro_context = 'kind-railgrid-kro'
preview_kro_node = 'railgrid-kro-control-plane'
dev_agent_image = 'ghcr.io/railgrid/railgrid-dev-agent:latest'
dev_agent_image_repository = 'ghcr.io/railgrid/railgrid-dev-agent'
universal_dev_image = 'ghcr.io/railgrid/railgrid-universal-dev:latest'
universal_dev_image_repository = 'ghcr.io/railgrid/railgrid-universal-dev'
# Universal sandbox is opt-in locally. Use the same resolved mode for Tilt's
# resource graph and the provider process so they cannot disagree.
app_studio_sandbox_mode = os.getenv('APP_STUDIO_RUN_SANDBOX_MODE', '').strip().lower() or 'off'
app_studio_sandbox_force = app_studio_sandbox_mode == 'force'
# Host address as seen FROM INSIDE the railgrid-kro containers. Resolve
# host.docker.internal inside the node first: on Docker Desktop/OrbStack the
# bridge gateway below is the VM, not the host, and dialing it gets connection
# refused. Native Linux Docker has no host.docker.internal — there the kind
# network gateway IS the host, so the docker-inspect gateway is the fallback.
docker_gateway_format = '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}'

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
# Each update builds the binary and initializes its APIs/credentials before
# Tilt replaces the running process. Registration is an explicit prerequisite.
# Watch the input admin credential, never the runtime kubeconfig written by
# init: watching our own output would schedule another update.
local_resource(
    'code',
    cmd='make init-provider-code',
    serve_cmd='make serve-provider-code',
    deps=[
        'providers/code/main.go',
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
        'providers/code/portal/package-lock.json',
        'providers/code/portal/vite.config.ts',
        'providers/code/apis',
        'providers/code/deploy/chart/files',
        'provider-sdk',
        'go.work',
        'Makefile',
        'providers/code/go.mod',
        'providers/code/go.sum',
        'providers/code/.env',
        '.kcp/admin.kubeconfig',
    ],
    resource_deps=['hub', 'code-register'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=8083, path='/readyz'),
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
#   code-register  → creates root:railgrid:providers:code
#   code-init      → writes kubeconfig + endpoint slice
# The Code update now runs this same init target before each restart. This
# separate manual action remains useful for setup/repair without restarting.
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
    'app-studio-preview-bridge-key',
    cmd='make app-studio-preview-bridge-dev-key',
    deps=[
        'Makefile',
        'providers/app-studio/hack/preview-bridge-dev-keys.mjs',
    ],
    labels=['providers-app-studio'],
)

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
    serve_cmd=('APP_STUDIO_HUB_PUBLIC_URL=%s ' +
               'APP_STUDIO_RUN_SANDBOX_MODE=%s ' +
               'APP_STUDIO_DEVELOPMENT_MODE=true APP_STUDIO_REPLICA_COUNT=1 ' +
               'make run-provider-app-studio') % (preview_hub_public_url, app_studio_sandbox_mode),
    deps=[
        'providers/app-studio/main.go',
        'providers/app-studio/assets.go',
        'providers/app-studio/api',
        'providers/app-studio/apis',
        'providers/app-studio/scaffold',
        'providers/app-studio/client',
        'providers/app-studio/store',
        'providers/app-studio/tenant',
        'providers/app-studio/workspace',
        'providers/app-studio/go.mod',
        'providers/app-studio/go.sum',
        'providers/app-studio/portal/src',
        'providers/app-studio/portal/package.json',
        'providers/app-studio/portal/vite.config.ts',
        'providers/app-studio/deploy/chart/templates/catalogentry.yaml',
        'providers/app-studio/deploy/chart/values.yaml',
        'providers/app-studio/.env',
    ],
    resource_deps=(
        ['hub', 'app-studio-db', 'dev-agent-image', 'app-studio-preview-bridge-key']
        + (['universal-dev-image'] if app_studio_sandbox_force else [])
    ),
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

# --- providers-agents ---
# Long-running personal AI agents (chat, scheduled runs, tools, durable memory).
# Standalone: only hard deps are the hub and the durable store (local Postgres
# container via agents-db). No app-studio/infrastructure dependency.
local_resource(
    'agents-db',
    cmd='make agents-db-up',
    deps=[
        'providers/agents/.env',
    ],
    labels=['providers-agents'],
)

local_resource(
    'agents',
    cmd='make build-agents-provider',
    serve_cmd='make run-provider-agents',
    deps=[
        'providers/agents/main.go',
        'providers/agents/assets.go',
        'providers/agents/init_cmd.go',
        'providers/agents/api',
        'providers/agents/channels',
        'providers/agents/apis',
        'providers/agents/client',
        'providers/agents/engine',
        'providers/agents/executor',
        'providers/agents/tools',
        'providers/agents/llm',
        'providers/agents/store',
        'providers/agents/tenant',
        'providers/agents/go.mod',
        'providers/agents/go.sum',
        'providers/agents/portal/src',
        'providers/agents/portal/package.json',
        'providers/agents/portal/vite.config.ts',
        'providers/agents/.env',
    ],
    resource_deps=['hub', 'agents-db'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=8087, path='/healthz'),
    ),
    labels=['providers-agents'],
)

local_resource(
    'agents-register',
    cmd='make install-provider-agents',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-agents'],
)

# Creates the agents APIExport (+ schemas + endpoint slice + bind grant) so
# tenants can Enable it. Run AFTER agents-register.
local_resource(
    'agents-init',
    cmd='make init-provider-agents',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub', 'agents-register'],
    labels=['providers-agents'],
)

local_resource(
    'agents-unregister',
    cmd='make uninstall-provider-agents',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['providers-agents'],
)

local_resource(
    'agents-db-down',
    cmd='make agents-db-down',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['providers-agents'],
)

# --- Dev agent image (template-native development mode) ---
# The static control binary injected into dev-mode components; owned by the
# infrastructure provider (docs/app-studio-template-sandboxes.md §2).
local_resource(
    'dev-agent-image',
    cmd=('''
set -eu
make load-dev-agent-image
dev_agent_digest="$(docker exec {kro_node} ctr -n k8s.io images ls 'name=={dev_agent_image}' | awk 'NR == 2 {{ print $3 }}')"
case "$dev_agent_digest" in
  sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "local dev-agent image has no immutable sha256 manifest digest" >&2; exit 1 ;;
esac
# kind imports the local tag but does not create its digest-qualified image
# name. Add that CRI-visible alias so Infrastructure can use the immutable
# reference without pulling from a registry.
docker exec {kro_node} ctr -n k8s.io images tag --force \
  {dev_agent_image} {dev_agent_repository}@$dev_agent_digest
''').format(
        kro_node=preview_kro_node,
        dev_agent_image=dev_agent_image,
        dev_agent_repository=dev_agent_image_repository,
    ),
    deps=[
        'providers/infrastructure/dev-agent',
    ],
    resource_deps=['kro-mgmt-up'],
    labels=['providers-app-studio'],
)

# Platform-curated Node/Go/Python toolchain image for the private coding
# environment. It is separate from the injected dev-agent binary so the image
# can be pinned independently in a production InfrastructureProvider CR.
if app_studio_sandbox_force:
    local_resource(
        'universal-dev-image',
        cmd=('''
set -eu
make load-universal-dev-image
universal_digest="$(docker exec {kro_node} ctr -n k8s.io images ls 'name=={universal_image}' | awk 'NR == 2 {{ print $3 }}')"
case "$universal_digest" in
  sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "local universal image has no immutable sha256 manifest digest" >&2; exit 1 ;;
esac
# kind imports the local tag but does not create its digest-qualified image
# name. Add that CRI-visible alias so the same immutable reference admitted by
# Infrastructure resolves without pulling from a registry.
docker exec {kro_node} ctr -n k8s.io images tag --force \
  {universal_image} {universal_repository}@$universal_digest
''').format(
                       kro_node=preview_kro_node,
                       universal_image=universal_dev_image,
                       universal_repository=universal_dev_image_repository,
                   ),
        deps=[
            'providers/infrastructure/dev-agent/Dockerfile.universal',
            'Makefile',
        ],
        resource_deps=['kro-mgmt-up'],
        labels=['providers-kro'],
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
# oci://registry.k8s.io/kro/charts/kro (single-cluster — the provider's
# instance controller bridges kcp → this cluster). The first run pulls
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

# The preview Gateway lives in the railgrid-kro management cluster, rather than
# the embedded-kcp hub process. Keep its bootstrap as a separate idempotent
# one-shot so both the Infrastructure provider and its host-side tunnel can
# wait for the Gateway, TLS secret, and Envoy Gateway controller together.
local_resource(
    'preview-gateway-up',
    cmd=(('PREVIEW_GATEWAY_CONTEXT=%s PREVIEW_GATEWAY_HOSTNAME="*.%s" ' +
          'PREVIEW_GATEWAY_PORT=%s PREVIEW_GATEWAY_SERVICE_PORT=%s ' +
          'make dev-preview-gateway-up') % (
             preview_kro_context,
             preview_app_base_domain,
             preview_gateway_port,
             preview_gateway_service_port,
         )),
    deps=[
        'Makefile',
        'hack/scripts/configure-tilt-preview-gateway.sh',
        preview_kro_kubeconfig,
    ],
    resource_deps=['kro-mgmt-up'],
    labels=['providers-kro'],
)

# With the default sslip.io domain, the public preview hostname resolves to
# 127.0.0.1 so a host browser reaches the port-forward below. For any configured
# public domain, the browser worker still needs an in-cluster route that avoids
# leaving the cluster. Override the preview wildcard inside kind so inspection
# and screenshots reach the Envoy Gateway Service directly in either mode.
local_resource(
    'app-studio-preview-dns',
    cmd='''
set -eu
KRO_KUBECONFIG=%s
CTX=%s
GW=%s
gateway_ip=''
host_gateway="$(docker exec %s getent ahostsv4 host.docker.internal 2>/dev/null | awk 'NR == 1 {print $1}')"
if [ -z "$host_gateway" ]; then
  host_gateway="$(docker inspect -f '%s' %s)"
fi
test -n "$host_gateway"
for _ in $(seq 1 60); do
  gateway_ip="$(kubectl --kubeconfig "$KRO_KUBECONFIG" --context "$CTX" \
    get svc -n %s \
    -l gateway.envoyproxy.io/owning-gateway-name=$GW \
    -o jsonpath='{.items[0].spec.clusterIP}' 2>/dev/null || true)"
  if [ -n "$gateway_ip" ] && [ "$gateway_ip" != 'None' ]; then
    break
  fi
  sleep 1
done
if [ -z "$gateway_ip" ] || [ "$gateway_ip" = 'None' ]; then
  echo 'preview gateway Service did not expose a ClusterIP' >&2
  exit 1
fi
KUBECONFIG="$KRO_KUBECONFIG" \
  hack/scripts/configure-tilt-preview-dns.sh "$CTX" %s "$gateway_ip" %s "$host_gateway"
'''.strip() % (
        preview_kro_kubeconfig,
        preview_kro_context,
        preview_gateway_name,
        preview_kro_node,
        docker_gateway_format,
        preview_kro_node,
        preview_gateway_namespace,
        preview_app_base_domain,
        preview_hub_public_host,
    ),
    deps=[
        'Tiltfile',
        'hack/scripts/configure-tilt-preview-dns.sh',
        preview_kro_kubeconfig,
    ],
    resource_deps=['preview-gateway-up'],
    labels=['providers-app-studio'],
)

local_resource(
    'kro-mgmt-down',
    cmd='make dev-kro-down',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['providers-kro'],
)

infrastructure_sandbox_digest_script = ''
infrastructure_sandbox_env = 'RAILGRID_CODING_SANDBOX_ENABLED=false \\'
infrastructure_resource_deps = [
    'hub',
    'dev-agent-image',
    'kro-mgmt-up',
    'preview-gateway-up',
    'app-studio-preview-dns',
    'app-studio-preview-port-forward',
    'app-studio-preview-bridge-key',
]
if app_studio_sandbox_force:
    infrastructure_sandbox_digest_script = ('''
dev_agent_digest="$(docker exec {kro_node} ctr -n k8s.io images ls 'name=={dev_agent_image}' | awk 'NR == 2 {{ print $3 }}')"
case "$dev_agent_digest" in
  sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "local dev-agent image has no immutable sha256 manifest digest" >&2; exit 1 ;;
esac
universal_digest="$(docker exec {kro_node} ctr -n k8s.io images ls 'name=={universal_image}' | awk 'NR == 2 {{ print $3 }}')"
case "$universal_digest" in
  sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "local universal image has no immutable sha256 manifest digest" >&2; exit 1 ;;
esac
''').format(
        kro_node=preview_kro_node,
        dev_agent_image=dev_agent_image,
        universal_image=universal_dev_image,
    )
    infrastructure_sandbox_env = ('''RAILGRID_CODING_SANDBOX_ENABLED=true \\
RAILGRID_DEV_AGENT_IMAGE="{dev_agent_repository}@$dev_agent_digest" \\
RAILGRID_DEV_IMAGE_UNIVERSAL="{universal_repository}@$universal_digest" \\''').format(
        dev_agent_repository=dev_agent_image_repository,
        universal_repository=universal_dev_image_repository,
    )
    infrastructure_resource_deps = infrastructure_resource_deps + ['universal-dev-image']

local_resource(
    'infrastructure',
    cmd='make build-infrastructure-provider',
    serve_cmd=('''
set -eu
host_gateway="$(docker exec {kro_node} getent ahostsv4 host.docker.internal 2>/dev/null | awk 'NR == 1 {{print $1}}')"
if [ -z "$host_gateway" ]; then
  host_gateway="$(docker inspect -f '{docker_gateway_format}' {kro_node})"
fi
test -n "$host_gateway"
{sandbox_digest_script}KRO_KUBECONFIG={kro_kubeconfig} \
RAILGRID_GATEWAY_NAME={gateway_name} \
RAILGRID_GATEWAY_NAMESPACE={gateway_namespace} \
RAILGRID_APP_BASE_DOMAIN={base_domain} \
RAILGRID_APP_PUBLIC_PORT={public_port} \
RAILGRID_ACCESS_HUB_URL="https://$host_gateway:9443" \
RAILGRID_ACCESS_HUB_PUBLIC_URL={hub_public_url} \
RAILGRID_ACCESS_HUB_INSECURE=true \
{sandbox_env}
make run-provider-infrastructure
''').format(
                   docker_gateway_format=docker_gateway_format,
                   kro_node=preview_kro_node,
                   kro_kubeconfig=preview_kro_kubeconfig,
                   gateway_name=preview_gateway_name,
                   gateway_namespace=preview_gateway_namespace,
                   base_domain=preview_app_base_domain,
                   public_port=preview_app_public_port,
                   hub_public_url=preview_hub_public_url,
                   sandbox_digest_script=infrastructure_sandbox_digest_script,
                   sandbox_env=infrastructure_sandbox_env,
               ),
    deps=[
        'providers/infrastructure/main.go',
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
        # serve refuses to start until RAILGRID_PROVIDER_KUBECONFIG resolves
        # to a real file (see the Makefile target), so the provider must be
        # restarted once `infrastructure-init` has written it.
        '.kcp/infrastructure-runtime.kubeconfig',
    ],
    # hub for CatalogEntry registration target, kro-mgmt-up for the
    # backend cluster the catalog reads from. Both must be green
    # before the provider starts; otherwise it boots in stub mode
    # which is fine but confusing for the dev who just ran `tilt up`.
    resource_deps=infrastructure_resource_deps,
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
#   infrastructure (long-lived) → serve mounts RAILGRID_PROVIDER_KUBECONFIG
# Manual so devs control when re-bootstrap happens (it overwrites
# the runtime kubeconfig and rotates the token).
infrastructure_init_cmd = 'make init-provider-infrastructure'
infrastructure_init_resource_deps = ['hub', 'infrastructure-register']
if app_studio_sandbox_force:
    # The init command owns Template seeding, so it must receive the same
    # immutable universal image contract as the long-lived provider. Without
    # this, local force mode can run against a stale platform Template even
    # though the serving process correctly enables coding sandboxes.
    infrastructure_init_cmd = ('''set -eu
{sandbox_digest_script}{sandbox_env}
make init-provider-infrastructure
''').format(
        sandbox_digest_script=infrastructure_sandbox_digest_script,
        sandbox_env=infrastructure_sandbox_env,
    )
    infrastructure_init_resource_deps = infrastructure_init_resource_deps + [
        'dev-agent-image',
        'universal-dev-image',
        'kro-mgmt-up',
    ]
local_resource(
    'infrastructure-init',
    cmd=infrastructure_init_cmd,
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=infrastructure_init_resource_deps,
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
#     railgrid-provider-kubeconfig (HostSecretWriter, enabled by the hub's
#     --kubeconfig + --hub-internal-url flags above), the init container
#     bootstraps the workspace with it, then serve runs — all inside the
#     railgrid-kro kind cluster. Deploys TWO replicas (the only Tilt resource
#     with real kube replicas + Service round-robin): exercises the
#     leader-elected Template/Instance controllers and bootstrap loop.
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

# Preview/apps ingress tunnel — forwards the Envoy Gateway's HTTPS listener
# from railgrid-kro to the host on :10443. The kubeconfig and context are explicit
# on every kubectl invocation: the developer's ambient context commonly points
# at an embedded-kcp workspace and must never control this loop.
#
# The probe MUST send SNI. The Gateway listener is hostname-scoped HTTPS, so a
# TLS hello without a matching server name selects no filter chain and Envoy
# resets the connection. `--resolve` supplies both the matching SNI/Host and
# the loopback address without requiring /etc/hosts.
local_resource(
    'app-studio-preview-port-forward',
    cmd='true',
    serve_cmd='''
set -eu
KRO_KUBECONFIG=%s
CTX=%s
GW=%s
PROBE_HOST=tilt-probe.%s
pf=''
cleanup_forward() {
  if [ -n "$pf" ] && kill -0 "$pf" 2>/dev/null; then
    kill "$pf" 2>/dev/null || true
    wait "$pf" 2>/dev/null || true
  fi
}
trap cleanup_forward EXIT
trap 'exit 0' INT TERM
while true; do
  if [ ! -f "$KRO_KUBECONFIG" ]; then
    sleep 2
    continue
  fi
  svc="$(kubectl --kubeconfig "$KRO_KUBECONFIG" --context "$CTX" \
    get svc -n %s \
    -l gateway.envoyproxy.io/owning-gateway-name=$GW \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [ -z "$svc" ]; then
    sleep 2
    continue
  fi
  kubectl --kubeconfig "$KRO_KUBECONFIG" --context "$CTX" \
    -n %s port-forward "svc/$svc" %s:%s &
  pf=$!
  fails=0
  while kill -0 "$pf" 2>/dev/null; do
    sleep 15
    # Any HTTP response (including 404) means the listener is programmed.
    if curl -sk --max-time 5 -o /dev/null \
         --resolve "$PROBE_HOST:%s:127.0.0.1" \
         "https://$PROBE_HOST:%s/"; then
      fails=0
    else
      fails=$((fails + 1))
      echo "preview tunnel unhealthy ($fails/3)"
      if [ "$fails" -ge 3 ]; then
        echo "restarting forward and bouncing the Envoy proxy pod"
        kubectl --kubeconfig "$KRO_KUBECONFIG" --context "$CTX" \
          delete pod -n %s \
          -l gateway.envoyproxy.io/owning-gateway-name=$GW \
          --wait=false 2>/dev/null || true
        kill "$pf" 2>/dev/null || true
        break
      fi
    fi
  done
  wait "$pf" 2>/dev/null || true
  pf=''
  sleep 3
done
'''.strip() % (
        preview_kro_kubeconfig,
        preview_kro_context,
        preview_gateway_name,
        preview_app_base_domain,
        preview_gateway_namespace,
        preview_gateway_namespace,
        preview_gateway_port,
        preview_gateway_port,
        preview_gateway_port,
        preview_gateway_port,
        preview_gateway_namespace,
    ),
    resource_deps=['preview-gateway-up'],
    readiness_probe=probe(
        period_secs=2,
        timeout_secs=1,
        tcp_socket=tcp_socket_action(port=int(preview_gateway_port)),
    ),
    labels=['providers-app-studio'],
)

# ---------------------------------------------------------------------------
# edges — the standalone edges provider (KubernetesCluster + LinuxServer under
# one group edges.railgrid.ai) PLUS the dev agents that connect to it.
#
# The provider terminates the agent reverse tunnels and serves
# kubectl/ssh/mcp; the agents below dial it through the hub backend proxy at
# /services/providers/edges/*. Multi-replica capable: tunnels are claimed in
# a Lease registry and peer replicas relay to the owner (see the `edges-2`
# standby under the `replicas` label).
#
# Workflow:
#   1. `edges` serves automatically; click ▶ on `edges-register` then
#      `edges-init` to create its APIExport so tenants can Enable it.
#   2. Click ▶ on `edge-{kube,server}-create` to log in via static token,
#      register the edge with the hub, and write .env.edge.<type>.
#   3. Click ▶ on `edge-{kube,server}-agent` to run the agent.
#        - kubernetes: also spins up a `railgrid-agent` kind cluster on first run.
#        - server: also click ▶ on `ssh-server` so the agent has an SSH target.
#   4. Home Assistant (to exercise the Service kind + its MCP tools):
#        - kube edge:   ▶ `ha-kube-deploy`, then ▶ `ha-kube-forward` to onboard
#          and mint a long-lived token, then declare a Service against it
#          (portal → Services → Add service, or the example manifest in
#          providers/edges/contrib/manifests/homeassistant/).
#        - server edge: install HA on the host; the agent discovers it and the
#          Service appears on its own — only the token needs attaching.
# ---------------------------------------------------------------------------

# --- edges provider ---
local_resource(
    'edges',
    cmd='make build-edges-provider',
    serve_cmd='EDGES_HUB_EXTERNAL_URL=%s make run-provider-edges' % railgrid_hub_external_url,
    deps=[
        'providers/edges/main.go',
        'providers/edges/controller_manager.go',
        'providers/edges/init_cmd.go',
        'providers/edges/apis',
        'providers/edges/internal',
        'providers/edges/scheme',
        'providers/edges/portal/src',
        'providers/edges/portal/package.json',
        'providers/edges/go.mod',
    ],
    resource_deps=['hub'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=8088, path='/healthz'),
    ),
    labels=['edges'],
)

local_resource(
    'edges-register',
    cmd='make install-provider-edges',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['edges'],
)

# Creates the edges APIExport (+ endpoint slice + bind grant) so tenants can
# Enable it. Run AFTER edges-register (which applies the Provider CR → the
# controller provisions the workspace + provider-token Secret this reads).
local_resource(
    'edges-init',
    cmd='make init-provider-edges',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub', 'edges-register'],
    labels=['edges'],
)

local_resource(
    'edges-unregister',
    cmd='make uninstall-provider-edges',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['hub'],
    labels=['edges'],
)
local_resource(
    'edge-kube-create',
    # Drop any saved agent kubeconfig from a previous hub/kcp incarnation
    # before re-creating. A stale ~/.railgrid/agent-<edge>.kubeconfig points at
    # an old workspace + revoked SA token; the agent would load it, skip
    # re-registration, and fail every call with "workspace access not
    # permitted" (User ""). Clearing it forces a fresh join-token exchange.
    #
    # Same for the railgrid kubectl context: `railgrid login` deliberately keeps a
    # previously selected workspace (pkg/cli/cmd/login.go), so after a kcp
    # rebuild the kept logical-cluster ID no longer exists and every request
    # 403s — kubectl apply then dies with "failed to download openapi:
    # unknown". Deleting the context first makes login land on the fresh home
    # workspace. Dev-only trade-off: a `railgrid use` selection is reset too.
    cmd='kubectl config delete-context railgrid >/dev/null 2>&1 || true; kubectl config delete-cluster railgrid >/dev/null 2>&1 || true; rm -f ~/.railgrid/agent-dev-edge-kube-1.kubeconfig ~/.railgrid/agent-dev-edge-kube-1.json && make dev-login-static && make dev-edge-create TYPE=kubernetes DEV_EDGE_NAME=dev-edge-kube-1',
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
    # The agent dials the edges provider (through the hub backend proxy), so it
    # must be serving.
    resource_deps=['hub', 'edges'],
    labels=['edges'],
)

local_resource(
    'edge-server-create',
    # Same stale-kubeconfig + stale-workspace cleanup as edge-kube-create
    # (see note there).
    cmd='kubectl config delete-context railgrid >/dev/null 2>&1 || true; kubectl config delete-cluster railgrid >/dev/null 2>&1 || true; rm -f ~/.railgrid/agent-dev-edge-server-1.kubeconfig ~/.railgrid/agent-dev-edge-server-1.json && make dev-login-static && make dev-edge-create TYPE=server DEV_EDGE_NAME=dev-edge-server-1',
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
    # The agent dials the edges provider (through the hub backend proxy), so it
    # must be serving.
    resource_deps=['hub', 'edges'],
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

# Home Assistant inside the `railgrid-agent` kind cluster — a real target for the
# kube-edge Service path (spec.targetRef → home-assistant.home.svc:8123).
# Creates the kind cluster itself if edge-kube-agent hasn't yet, so it has no
# resource_deps on it; first run pulls a ~1.5GB image.
local_resource(
    'ha-kube-deploy',
    cmd='make dev-deploy-homeassistant',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    labels=['edges'],
)

# Home Assistant ships with no users, and a long-lived access token can only be
# minted from the UI — so onboarding needs a browser. Port-forward, open
# http://localhost:8123, then profile → Security → Long-lived access tokens.
local_resource(
    'ha-kube-forward',
    serve_cmd='make dev-homeassistant-forward',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['ha-kube-deploy'],
    links=[link('http://localhost:8123', 'Home Assistant')],
    labels=['edges'],
)

# ---------------------------------------------------------------------------
# replicas — second instances of the scalable providers (manual, click ▶)
#
# The hub proxies each provider through ONE fixed URL (localhost:{port}), so
# these standbys receive no request traffic; what they exercise is the durable
# coordination the scaling work added: leader election (code), per-edge claim
# sharding (kuery), and the tunnel-ownership registry + relay (edges — both
# instances run with replica routing enabled, POD_IP=127.0.0.1, so the standby
# sees instance 1's tunnels through the Lease registry instead of flapping
# their status). Real replicas WITH load-balanced traffic: `infrastructure-pod`
# (helm, replicaCount=2 in the railgrid-kro kind cluster).
#
# Not here on purpose:
#   - hub: Tilt runs it with --embedded-kcp; HA requires external kcp.
#   - app-studio: dev uses the in-memory message store, which is per-process —
#     replica claims need the shared Postgres store to mean anything.
# ---------------------------------------------------------------------------

local_resource(
    'code-2',
    serve_cmd='make run-provider-code CODE_PORT=18083',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['code'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=18083, path='/healthz'),
    ),
    labels=['replicas'],
)

local_resource(
    'kuery-2',
    serve_cmd='make run-provider-kuery KUERY_PORT=18084',
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['kuery'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=18084, path='/healthz'),
    ),
    labels=['replicas'],
)

local_resource(
    'edges-2',
    serve_cmd='EDGES_HUB_EXTERNAL_URL=%s POD_NAME=edges-local-2 make run-provider-edges EDGES_PORT=18088 EDGES_INTERNAL_PORT=18090' % railgrid_hub_external_url,
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['edges'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=18088, path='/healthz'),
    ),
    labels=['replicas'],
)

# ---------------------------------------------------------------------------
# external providers — built, deployed and registered by the provider
# repository's own Tilt library (docs/external-providers-tilt.md). The pods run
# in railgrid-kro and reach the host hub through host.docker.internal, which
# also relays their scoped kcp access: the admin kubeconfig's kcp address is
# loopback-only.
# ---------------------------------------------------------------------------
external_providers_dir = cfg.get('external-providers-dir', '') or os.getenv('RAILGRID_EXTERNAL_PROVIDERS_DIR', '')
if external_providers_dir:
    external_providers_dir = os.path.abspath(external_providers_dir)
    external_providers_lib = os.path.join(external_providers_dir, 'hack', 'tilt', 'providers.tilt')
    if not os.path.exists(external_providers_lib):
        fail('--external-providers-dir %s has no hack/tilt/providers.tilt; see docs/external-providers-tilt.md' % external_providers_dir)
    # Never deploy provider charts into whatever cluster happens to be current.
    if k8s_context() != preview_kro_context:
        fail('External providers run in %s, but Tilt uses context %r; start with `make tilt EXTERNAL_PROVIDERS_DIR=...`' % (preview_kro_context, k8s_context()))
    allow_k8s_contexts(preview_kro_context)
    external_providers = load_dynamic(external_providers_lib)['railgrid_providers'](
        # Empty (e.g. `make tilt EXTERNAL_PROVIDERS=`) means every provider.
        selection=cfg.get('external-providers', '') or os.getenv('RAILGRID_EXTERNAL_PROVIDERS', '') or 'all',
        context=preview_kro_context,
        hub_url='https://host.docker.internal:9443',
        hub_insecure=True,
        # The dev serving cert is self-signed, so it is its own CA. Providers
        # that refuse an unverified hub (Factory) trust it through this.
        hub_ca_file=os.path.abspath('certs/apiserver.crt'),
        lifecycle_env={
            'RAILGRID_KCP_KUBECONFIG': os.path.abspath('.kcp/admin.kubeconfig'),
            'RAILGRID_PROVIDER_KCP_SERVER': 'https://host.docker.internal:9443',
            'RAILGRID_PROVIDER_KCP_INSECURE': 'true',
        },
        resource_deps=['hub', 'kro-mgmt-up'],
        # A provider still missing local setup
        # is skipped with a warning; naming it explicitly makes it an error.
        skip_unconfigured=True,
        # The host hub cannot resolve cluster Service DNS: forward each
        # provider to a local port and point its CatalogEntry there.
        host_ports=True,
        # Embedded kcp advertises 127.0.0.1:6443 virtual-workspace URLs;
        # bridge that port in each pod to the hub, which relays them.
        kcp_loopback_proxy='host.docker.internal:9443',
    )
    print('  external providers: %s (from %s)' % (', '.join(external_providers), external_providers_dir))
