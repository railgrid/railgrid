# Tiltfile — local dev for railgrid hub + portal + edges
# Replaces: make run-hub-embedded-static (terminal 1) + make dev-portal (terminal 2)
# Usage: tilt up

# Persisted by the same checkout-aware launcher used by `make tilt`.
# A bare `tilt up` can load this too, but Tilt's own API port is selected
# before Tiltfile evaluation; use `make tilt` when another Tilt is running.
for provider in ['quickstart', 'infrastructure', 'code', 'kuery', 'app-studio', 'agents', 'edges']:
    watch_file('providers/' + provider + '/manifest.yaml')
# The checkout launcher resolves root .env overrides before the Tilt graph is
# evaluated. Reload the file itself so a changed preview zone is resolved
# again and reconciled through the Gateway and Infrastructure provider.
watch_file('.env')
# Re-evaluate the resolved environment when its source changes too.
watch_file('hack/scripts/tilt-checkout.py')
# Nothing below deploys to the current kube context: the hub, portal and
# providers are local_resources, and the one k8s deploy path (external
# provider charts) refuses any context other than the kro kind cluster before
# calling allow_k8s_contexts for it. `make tilt` starts Tilt on the checkout's
# kind context, but a bare `tilt up` runs on the shell's context, and Tilt
# refuses local() when that looks non-local (`kubectl ws` leaves
# workspace.kcp.io/current selected), so allow whatever is current for the
# local commands only.
allow_k8s_contexts(k8s_context())
checkout_env = decode_json(str(local(['python3', 'hack/scripts/tilt-checkout.py', 'prepare-env'], quiet=True)))
for key, value in checkout_env.items():
    os.putenv(key, value)

def dev_port(name, default):
    return int(os.getenv(name, str(default)))

hub_port = dev_port('HUB_PORT', 9443)
kcp_port = dev_port('KCP_PORT', 6443)
portal_port = dev_port('PORTAL_PORT', 3000)
hub_internal_url = 'https://host.docker.internal:%s' % hub_port

trigger_mode(TRIGGER_MODE_AUTO)

# Providers developed in another repository join this session when their
# checkout is given (docs/external-providers-tilt.md). They run as pods in the
# railgrid-kro kind cluster, so `make tilt` creates it before `tilt up` and
# selects its context. Without --external-providers every provider in the
# checkout is loaded:
#
#   make tilt EXTERNAL_PROVIDERS_DIR=../providers
#   make tilt EXTERNAL_PROVIDERS_DIR=../providers EXTERNAL_PROVIDERS=planner
config.define_string('external-providers-dir', usage='Comma-separated external provider checkout paths')
config.define_string('external-providers')
config.define_string('app-studio-sandbox-mode', usage='Private per-run sandbox mode (project runtime remains sandboxed): off, byo-only, force')
config.define_string('host-log-path', usage='Explicit Tilt console log to bound to 256 MiB plus one 8 MiB diagnostic tail')
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
# Run the code provider on its S3 bundle store against a local MinIO instead
# of a local directory, to exercise the backend a multi-replica deployment
# needs. Off by default: Tilt runs one replica, and the file store is what
# that needs.
#
#   make tilt CODE_BUNDLE_STORE=s3
config.define_string('code-bundle-store', usage='Code provider transient store: file (default) or s3 (adds a local MinIO resource)')
cfg = config.parse()

# ---------------------------------------------------------------------------
# External provider checkouts (loaded fully further down, once the hub is
# defined) may need the kcp SHARD configured at startup: an admission
# configuration (the global validating webhook the metering provider's
# enforcer sits behind) and an audit backend. Those are hub flags, so they
# are collected here, before the hub command is built. A checkout opts in by
# exporting `railgrid_shard_config(host_ports, kcp_loopback_proxy)` from its
# hack/tilt/providers.tilt, returning a dict with any of
# admission_config_file, audit_policy_file, audit_webhook_config_file. The
# function must only write files and must not define Tilt resources.
# ---------------------------------------------------------------------------
_shard_providers_dirs = cfg.get('external-providers-dir', '') or os.getenv('RAILGRID_EXTERNAL_PROVIDERS_DIR', '')
external_shard_flags = ''
for _shard_dir in _shard_providers_dirs.split(','):
    _shard_dir = _shard_dir.strip()
    if not _shard_dir:
        continue
    _shard_lib = os.path.join(os.path.abspath(_shard_dir), 'hack', 'tilt', 'providers.tilt')
    if not os.path.exists(_shard_lib):
        continue
    _shard_symbols = load_dynamic(_shard_lib)
    if 'railgrid_shard_config' not in _shard_symbols:
        continue
    _shard = _shard_symbols['railgrid_shard_config'](host_ports=True, kcp_loopback_proxy='host.docker.internal:%s' % hub_port) or {}
    for _key, _flag in [('admission_config_file', '--kcp-admission-config-file'),
                        ('audit_policy_file', '--kcp-audit-policy-file'),
                        ('audit_webhook_config_file', '--kcp-audit-webhook-config-file')]:
        if _shard.get(_key):
            external_shard_flags += ' %s=%s' % (_flag, _shard[_key])
    if external_shard_flags:
        print('  external shard config: %s (from %s)' % (external_shard_flags.strip(), _shard_dir))

code_bundle_store = (
    (cfg.get('code-bundle-store', '') or '').strip().lower()
    or os.getenv('CODE_BUNDLE_STORE', '').strip().lower()
    or 'file'
)
if code_bundle_store not in ('file', 's3'):
    fail('--code-bundle-store must be one of: file, s3')
code_minio_enabled = code_bundle_store == 's3'
# Same variables the Makefile's code-minio-serve reads, so an override
# reaches the readiness probe and the server alike.
code_minio_port = int(os.getenv('CODE_MINIO_PORT', '59000'))
code_minio_console_port = int(os.getenv('CODE_MINIO_CONSOLE_PORT', '59001'))

kcp_image = cfg.get('kcp-image', '') or os.getenv('RAILGRID_KCP_IMAGE', '')
# Universal sandbox stays opt-in locally. Resolve one mode for both the
# resource graph and provider process, with an explicit Tilt arg taking
# precedence over the existing environment override.
app_studio_sandbox_mode = (
    (cfg.get('app-studio-sandbox-mode', '') or '').strip().lower()
    or os.getenv('APP_STUDIO_RUN_SANDBOX_MODE', '').strip().lower()
    or 'off'
)
if app_studio_sandbox_mode not in ('off', 'byo-only', 'force'):
    fail('--app-studio-sandbox-mode must be one of: off, byo-only, force')
app_studio_sandbox_force = app_studio_sandbox_mode == 'force'
kcp_external_root = '.kcp-external'
kcp_external_kubeconfig = kcp_external_root + '/admin.kubeconfig'

# `tilt-checkout.py prepare-env` resolves the app domain once: inherited user
# setting, root .env, then checkout-specific sslip.io. Use that same value for
# the provider, Gateway listener, browser DNS, and tunnel probe. The public app
# port may be explicitly empty when the external endpoint uses normal HTTPS.
railgrid_hub_external_url = os.getenv('RAILGRID_HUB_EXTERNAL_URL', os.getenv('DEV_HUB_URL', 'https://console.127.0.0.1.sslip.io:9443'))
preview_app_base_domain = os.getenv('RAILGRID_APP_BASE_DOMAIN', '')
if not preview_app_base_domain:
    fail('Tilt setup did not resolve RAILGRID_APP_BASE_DOMAIN')
preview_gateway_port = os.getenv('PREVIEW_GATEWAY_PORT', '10443')
preview_app_public_port = os.getenv('RAILGRID_APP_PUBLIC_PORT', preview_gateway_port)
preview_app_public_port_suffix = (':' + preview_app_public_port) if preview_app_public_port else ''
preview_app_frame_source = 'https://*.' + preview_app_base_domain + preview_app_public_port_suffix
preview_hub_public_url = os.getenv(
    'RAILGRID_ACCESS_HUB_PUBLIC_URL',
    os.getenv('DEV_HUB_URL', 'https://console.127.0.0.1.sslip.io:9443'),
)
preview_hub_public_host = preview_hub_public_url.replace('https://', '').replace('http://', '').split('/')[0].split(':')[0]
# CoreDNS sends browser-worker traffic directly to Envoy's ClusterIP. Expose
# the port advertised in app URLs there even when the internal listener and
# host port-forward stay on an unprivileged development port.
preview_gateway_service_port = preview_app_public_port or '443'

# Observe disk headroom without deleting data or caches. A headless launcher may
# opt its console log into bounded retention with --host-log-path.
host_health_args = ['python3', 'hack/scripts/tilt-host-health.py', '--path', os.getcwd()]
host_build_tmp = os.getenv('GOTMPDIR', '')
if host_build_tmp and host_build_tmp != 'off':
    host_health_args += ['--path', host_build_tmp]
host_log_path = cfg.get('host-log-path', '') or os.getenv('TILT_LOG_PATH', '')
if host_log_path:
    host_health_args += ['--log', host_log_path]
local_resource(
    'host-health',
    serve_cmd=host_health_args,
    deps=['hack/scripts/tilt-host-health.py'],
    readiness_probe=probe(period_secs=10, exec=exec_action(host_health_args + ['--check'])),
    labels=['hub'],
)

# ---------------------------------------------------------------------------
# portal — Vue.js SPA dev server on :3000
# ---------------------------------------------------------------------------
local_resource(
    'portal',
    # Reconcile the locked dependencies on startup and manifest changes. A
    # node_modules directory can exist even when an install is incomplete.
    cmd='cd portal && npm ci --include=dev --no-audit --no-fund',
    serve_cmd='make dev-portal ARGS="--strictPort --port %s"' % portal_port,
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
        serve_cmd='hack/kcp-external.sh %s %s %s %s' % (kcp_image, kcp_external_root, kcp_port, os.getenv('KCP_EXTERNAL_CONTAINER', 'railgrid-kcp-external')),
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
    kcp_server_flags = ' --embedded-kcp --kcp-root-dir=.kcp --kcp-secure-port=%s --kcp-etcd-client-port=%s --kcp-etcd-peer-port=%s' % (kcp_port, dev_port('KCP_ETCD_CLIENT_PORT', 2379), dev_port('KCP_ETCD_PEER_PORT', 2380)) + external_shard_flags
    hub_deps = ['portal']

# ---------------------------------------------------------------------------
local_resource(
    'hub',
    cmd='''
make certs && \
make build-hub
''',
    serve_cmd=('''./bin/railgrid-hub \
  --serving-cert-file=certs/apiserver.crt \
  --serving-key-file=certs/apiserver.key \
  --hub-external-url=%s \
  --dev-mode -v 4 \
  --static-auth-token=$DEV_STATIC_TOKEN \
  --static-auth-token=dev-token2 \
  --admin-users=$DEV_STATIC_ADMIN \
%s \
  --listen-addr=:%s \
  --data-dir=%s \
  --portal-dev-url=http://localhost:%s \
  --portal-frame-source=%s \
  --published-apps-domain=%s \
  --kubeconfig=.railgrid-kro.kubeconfig \
  --hub-internal-url=%s
''' % (
        railgrid_hub_external_url,
        kcp_server_flags,
        hub_port,
        os.getenv('HUB_DATA_DIR', '/tmp/railgrid-data'),
        portal_port,
        preview_app_frame_source,
        preview_app_base_domain,
        hub_internal_url,
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
preview_kro_kind_name = os.getenv('KRO_KIND_NAME', 'railgrid-kro')
preview_kro_context = 'kind-' + preview_kro_kind_name
preview_kro_node = preview_kro_kind_name + '-control-plane'
access_proxy_image = os.getenv('ACCESS_PROXY_IMAGE', 'ghcr.io/railgrid/railgrid-access-proxy:dev')
access_proxy_image_repository = 'ghcr.io/railgrid/railgrid-access-proxy'
dev_agent_image = os.getenv('DEV_AGENT_IMAGE', 'ghcr.io/railgrid/railgrid-dev-agent:latest')
dev_agent_image_repository = 'ghcr.io/railgrid/railgrid-dev-agent'
universal_dev_image = os.getenv('UNIVERSAL_DEV_IMAGE', 'ghcr.io/railgrid/railgrid-universal-dev:latest')
universal_dev_image_repository = 'ghcr.io/railgrid/railgrid-universal-dev'
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
        http_get=http_get_action(port=dev_port('QUICKSTART_PORT', 8081), path='/healthz'),
    ),
    labels=['providers-quickstart'],
)

local_resource(
    'quickstart-register',
    cmd='make install-provider-quickstart',
    allow_parallel=True,
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
# Optional local MinIO for the code provider's transient stores (commit
# bundles + staged git snapshots), only with --code-bundle-store=s3. The
# server is built from source into hack/tools (make code-minio-serve).
if code_minio_enabled:
    local_resource(
        'code-minio',
        serve_cmd='make code-minio-serve',
        deps=['Makefile'],
        readiness_probe=probe(
            period_secs=2,
            http_get=http_get_action(port=code_minio_port, path='/minio/health/live'),
        ),
        links=[link('http://127.0.0.1:%d' % code_minio_console_port, 'MinIO console')],
        labels=['providers-code'],
    )

# Each update builds the binary and initializes its APIs/credentials before
# Tilt replaces the running process. Registration is an explicit prerequisite.
# Watch the input admin credential, never the runtime kubeconfig written by
# init: watching our own output would schedule another update.
local_resource(
    'code',
    cmd='make init-provider-code',
    serve_cmd='make serve-provider-code',
    serve_env={'CODE_BUNDLE_STORE': code_bundle_store},
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
    resource_deps=['hub', 'code-register'] + (['code-minio'] if code_minio_enabled else []),
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=dev_port('CODE_PORT', 8083), path='/readyz'),
    ),
    labels=['providers-code'],
)

local_resource(
    'code-register',
    cmd='make install-provider-code',
    allow_parallel=True,
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
    'app-studio-portal',
    cmd='make build-app-studio-provider-portal',
    deps=[
        'Makefile',
        'providers/app-studio/portal/src',
        'providers/app-studio/portal/index.html',
        'providers/app-studio/portal/icon.svg',
        'providers/app-studio/portal/env.d.ts',
        'providers/app-studio/portal/package.json',
        'providers/app-studio/portal/package-lock.json',
        'providers/app-studio/portal/vite.config.ts',
        'providers/app-studio/portal/tsconfig.json',
        'providers/app-studio/portal/scripts',
    ],
    # Node tests do not enter the Vite bundle. Editing them should leave the
    # running provider and its in-flight turns available.
    ignore=['providers/app-studio/portal/**/*.test.mjs'],
    labels=['providers-app-studio'],
)

local_resource(
    'app-studio',
    cmd='make build-app-studio-provider-go',
    serve_cmd=('APP_STUDIO_HUB_PUBLIC_URL=%s ' +
               'APP_STUDIO_RUN_SANDBOX_MODE=%s ' +
               'APP_STUDIO_DEVELOPMENT_MODE=true APP_STUDIO_REPLICA_COUNT=1 ' +
               'make run-provider-app-studio-prebuilt') % (preview_hub_public_url, app_studio_sandbox_mode),
    deps=[
        'providers/app-studio/main.go',
        'providers/app-studio/assets.go',
        'providers/app-studio/api',
        'providers/app-studio/apis',
        'providers/app-studio/controller',
        'providers/app-studio/scaffold',
        'providers/app-studio/client',
        'providers/app-studio/store',
        'providers/app-studio/tenant',
        'providers/app-studio/workspace',
        'providers/app-studio/internal',
        'providers/app-studio/go.mod',
        'providers/app-studio/go.sum',
        'providers/app-studio/portal/dist',
        'providers/app-studio/deploy/chart/templates/catalogentry.yaml',
        'providers/app-studio/deploy/chart/values.yaml',
        'providers/app-studio/.env',
        'Makefile',
    ],
    # Go excludes these files from the provider binary.
    ignore=['providers/app-studio/**/*_test.go'],
    resource_deps=(
        ['hub', 'app-studio-portal', 'app-studio-db', 'dev-agent-image', 'app-studio-preview-bridge-key']
        + (['universal-dev-image'] if app_studio_sandbox_force else [])
    ),
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=dev_port('APP_STUDIO_PORT', 8085), path='/healthz'),
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
    allow_parallel=True,
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

# serve needs the provider's OWN kubeconfig (.kcp/agents-provider.kubeconfig),
# minted by ▶ agents-init AFTER ▶ agents-register has been applied and
# reconciled: a data-plane verb runs as the provider, so no other identity will
# do. The file is in deps, so Tilt restarts the serve process once init writes
# it; until then the Makefile target refuses to start and names the target to
# run.
local_resource(
    'agents',
    cmd='make build-agents-provider',
    serve_cmd='make run-provider-agents',
    deps=[
        'providers/agents/main.go',
        'providers/agents/assets.go',
        'providers/agents/catalogentry.go',
        'providers/agents/controller_manager.go',
        'providers/agents/init_cmd.go',
        'providers/agents/api',
        'providers/agents/backend',
        'providers/agents/channels',
        'providers/agents/apis',
        'providers/agents/client',
        'providers/agents/controller',
        'providers/agents/engine',
        'providers/agents/executor',
        'providers/agents/internal',
        'providers/agents/tools',
        'providers/agents/llm',
        'providers/agents/scheme',
        'providers/agents/store',
        'providers/agents/tenant',
        'providers/agents/go.mod',
        'providers/agents/go.sum',
        'providers/agents/portal/src',
        'providers/agents/portal/package.json',
        'providers/agents/portal/vite.config.ts',
        'providers/agents/.env',
        '.kcp/agents-provider.kubeconfig',
    ],
    resource_deps=['hub', 'agents-db'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=dev_port('AGENTS_PORT', 8087), path='/healthz'),
    ),
    labels=['providers-agents'],
)

local_resource(
    'agents-register',
    cmd='make install-provider-agents',
    allow_parallel=True,
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

# --- Published-app access-proxy image ---
# The Infrastructure provider templates every published app's access gate with
# this locally built image. Tilt loads it into the preview kind cluster and
# gives the provider an immutable containerd image reference below.
access_proxy_image_deps = [
    'providers/infrastructure/Dockerfile.access-proxy',
    'providers/infrastructure/accessproxy',
    'providers/infrastructure/cmd/access-proxy',
    'providers/infrastructure/go.mod',
    'providers/infrastructure/go.sum',
    # The Dockerfile copies this module into the build context.
    'provider-sdk',
]
local_resource(
    'access-proxy-image',
    cmd=('''
set -eu
preview_context="$(kubectl --kubeconfig={kro_kubeconfig} config current-context)"
if [ "$preview_context" != "{kro_context}" ]; then
  echo "preview kubeconfig context mismatch: expected {kro_context}, got $preview_context" >&2
  exit 1
fi
preview_nodes="$(kubectl --kubeconfig={kro_kubeconfig} get nodes -o name)"
if [ "$preview_nodes" != "node/{kro_node}" ]; then
  echo "preview kubeconfig node mismatch: expected node/{kro_node}, got $preview_nodes" >&2
  exit 1
fi
make load-access-proxy-image VERSION=dev KRO_KIND_NAME={kind_name}
access_proxy_digest="$(docker exec {kro_node} ctr -n k8s.io images ls 'name=={access_proxy_image}' | awk 'NR == 2 {{ print $3 }}')"
case "$access_proxy_digest" in
  sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "local access-proxy image has no immutable sha256 manifest digest" >&2; exit 1 ;;
esac
# kind imports the local tag but not the digest-qualified name admitted by the
# Infrastructure template. Add that exact alias in the target node's CRI.
docker exec {kro_node} ctr -n k8s.io images tag --force \\
  {access_proxy_image} {access_proxy_repository}@$access_proxy_digest
''').format(
        kind_name=preview_kro_kind_name,
        kro_kubeconfig=preview_kro_kubeconfig,
        kro_context=preview_kro_context,
        kro_node=preview_kro_node,
        access_proxy_image=access_proxy_image,
        access_proxy_repository=access_proxy_image_repository,
    ),
    deps=access_proxy_image_deps,
    resource_deps=['kro-mgmt-up'],
    labels=['providers-kro'],
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
        http_get=http_get_action(port=dev_port('KUERY_PORT', 8084), path='/healthz'),
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
    allow_parallel=True,
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
        'Tiltfile',
        '.env',
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
        '.env',
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
infrastructure_access_proxy_digest_script = ('''
access_proxy_digest="$(docker exec {kro_node} ctr -n k8s.io images ls 'name=={access_proxy_image}' | awk 'NR == 2 {{ print $3 }}')"
case "$access_proxy_digest" in
  sha256:????????????????????????????????????????????????????????????????) ;;
  *) echo "local access-proxy image has no immutable sha256 manifest digest" >&2; exit 1 ;;
esac
''').format(
    kro_node=preview_kro_node,
    access_proxy_image=access_proxy_image,
)
infrastructure_access_proxy_env = 'RAILGRID_ACCESS_PROXY_IMAGE="{repository}@$access_proxy_digest" \\'.format(
    repository=access_proxy_image_repository,
)
infrastructure_resource_deps = [
    'hub',
    'access-proxy-image',
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
{access_proxy_digest_script}
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
RAILGRID_ACCESS_HUB_URL="https://$host_gateway:{hub_port}" \
RAILGRID_ACCESS_HUB_PUBLIC_URL={hub_public_url} \
RAILGRID_ACCESS_HUB_INSECURE=true \
{access_proxy_env}
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
                   hub_port=hub_port,
                   hub_public_url=preview_hub_public_url,
                   access_proxy_digest_script=infrastructure_access_proxy_digest_script,
                   access_proxy_env=infrastructure_access_proxy_env,
                   sandbox_digest_script=infrastructure_sandbox_digest_script,
                   sandbox_env=infrastructure_sandbox_env,
               ),
    # resource_deps orders the image load before this provider starts; these
    # shared file deps also restart it when a rebuilt proxy gets a new digest,
    # so serve re-renders the Infrastructure RGD with that immutable image.
    deps=[
        'providers/infrastructure/main.go',
        'providers/infrastructure/assets.go',
        # These files are compiled into the provider's root main package too;
        # watching only main.go/assets.go left edits here running as stale code.
        'providers/infrastructure/catalogentry.go',
        'providers/infrastructure/coding_sandbox.go',
        'providers/infrastructure/controller_manager.go',
        'providers/infrastructure/init_cmd.go',
        'providers/infrastructure/instance_controller.go',
        'providers/infrastructure/operator.go',
        'providers/infrastructure/serve_dataplane.go',
        'providers/infrastructure/serve_identity.go',
        'providers/infrastructure/server',
        'providers/infrastructure/kro',
        'providers/infrastructure/tenant',
        'providers/infrastructure/mcpserver',
        'providers/infrastructure/dataplane',
        'providers/infrastructure/instancespec',
        'providers/infrastructure/networkpolicy',
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
        # The same root .env domain is used by preview-gateway-up. Reload this
        # provider when it changes so emitted Application hosts stay aligned.
        '.env',
        # Restart whenever init writes/updates the runtime kubeconfig:
        # serve refuses to start until RAILGRID_PROVIDER_KUBECONFIG resolves
        # to a real file (see the Makefile target), so the provider must be
        # restarted once `infrastructure-init` has written it.
        '.kcp/infrastructure-runtime.kubeconfig',
    ] + access_proxy_image_deps,
    # hub for CatalogEntry registration target, kro-mgmt-up for the
    # backend cluster the catalog reads from. Both must be green
    # before the provider starts; otherwise it boots in stub mode
    # which is fine but confusing for the dev who just ran `tilt up`.
    resource_deps=infrastructure_resource_deps,
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=dev_port('KROMC_PORT', 8082), path='/healthz'),
    ),
    labels=['providers-kro'],
)

local_resource(
    'infrastructure-register',
    cmd='make install-provider-infrastructure',
    allow_parallel=True,
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
    -n %s port-forward "svc/$svc" %s:%s >/dev/null &
  pf=$!
  fails=0
  probe_ticks=0
  while kill -0 "$pf" 2>/dev/null; do
    # A failed forwarded stream can terminate kubectl without killing Envoy.
    # Detect exit promptly instead of leaving the public listener down until
    # the next health probe.
    sleep 1
    if ! kill -0 "$pf" 2>/dev/null; then
      break
    fi
    probe_ticks=$((probe_ticks + 1))
    if [ "$probe_ticks" -lt 15 ]; then
      continue
    fi
    probe_ticks=0
    # Any HTTP response (including 404) means the listener is programmed.
    if curl -sk --max-time 5 -o /dev/null \
         --resolve "$PROBE_HOST:%s:127.0.0.1" \
         "https://$PROBE_HOST:%s/"; then
      fails=0
    else
      fails=$((fails + 1))
      echo "preview tunnel unhealthy ($fails/3)"
      if [ "$fails" -ge 3 ]; then
        # A host-side tunnel failure is not evidence of an unhealthy gateway.
        # Restart only this connection; deleting Envoy disrupts every preview.
        echo "restarting preview port-forward"
        kill "$pf" 2>/dev/null || true
        break
      fi
    fi
  done
  wait "$pf" 2>/dev/null || true
  pf=''
  sleep 1
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
    ),
    # PROBE_HOST uses the resolved application domain; restart the tunnel when
    # that shared config changes.
    deps=['Tiltfile', '.env'],
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
        http_get=http_get_action(port=dev_port('EDGES_PORT', 8088), path='/healthz'),
    ),
    labels=['edges'],
)

local_resource(
    'edges-register',
    cmd='make install-provider-edges',
    allow_parallel=True,
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
    # Checkout-specific names keep saved agent credentials separate.
    cmd='make dev-edge-clean-credentials TYPE=kubernetes && make dev-login-static && make dev-edge-create TYPE=kubernetes',
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
    cmd='make dev-edge-clean-credentials TYPE=server && make dev-login-static && make dev-edge-create TYPE=server',
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
    serve_cmd='docker rm -f %s >/dev/null 2>&1; make dev-run-ssh-server' % os.getenv('DEV_SSH_CONTAINER', 'openssh-server'),
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
    links=[link('http://localhost:%s' % dev_port('HA_FORWARD_PORT', 8123), 'Home Assistant')],
    labels=['edges'],
)

# ---------------------------------------------------------------------------
# replicas — second instances of the scalable providers (manual, click ▶)
#
# The hub proxies each provider through ONE fixed URL (localhost:{port}), so
# these standbys receive no request traffic; what they exercise is the durable
# coordination the scaling work added: leader election (code; with
# --code-bundle-store=s3 both instances also share the MinIO bundle store,
# which is what a real second replica needs), per-edge claim
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
    serve_cmd='make run-provider-code CODE_PORT=%s' % dev_port('CODE_REPLICA_PORT', 18083),
    serve_env={'CODE_BUNDLE_STORE': code_bundle_store},
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['code'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=dev_port('CODE_REPLICA_PORT', 18083), path='/healthz'),
    ),
    labels=['replicas'],
)

local_resource(
    'kuery-2',
    serve_cmd='make run-provider-kuery KUERY_PORT=%s' % dev_port('KUERY_REPLICA_PORT', 18084),
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['kuery'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=dev_port('KUERY_REPLICA_PORT', 18084), path='/healthz'),
    ),
    labels=['replicas'],
)

local_resource(
    'edges-2',
    serve_cmd='EDGES_HUB_EXTERNAL_URL=%s POD_NAME=edges-local-2 make run-provider-edges EDGES_PORT=%s EDGES_INTERNAL_PORT=%s' % (railgrid_hub_external_url, dev_port('EDGES_REPLICA_PORT', 18088), dev_port('EDGES_REPLICA_INTERNAL_PORT', 18090)),
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
    resource_deps=['edges'],
    readiness_probe=probe(
        period_secs=5,
        http_get=http_get_action(port=dev_port('EDGES_REPLICA_PORT', 18088), path='/healthz'),
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
# Comma-separated checkout roots; preserve spaces inside individual paths.
external_providers_dirs = cfg.get('external-providers-dir', '') or os.getenv('RAILGRID_EXTERNAL_PROVIDERS_DIR', '')
external_providers_seen = []
for external_providers_dir in external_providers_dirs.split(','):
    external_providers_dir = external_providers_dir.strip()
    if not external_providers_dir:
        continue
    external_providers_dir = os.path.abspath(external_providers_dir)
    if external_providers_dir in external_providers_seen:
        continue
    external_providers_seen.append(external_providers_dir)
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
        hub_url=hub_internal_url,
        hub_insecure=True,
        # The dev serving cert is self-signed, so it is its own CA. Providers
        # that refuse an unverified hub (Factory) trust it through this.
        hub_ca_file=os.path.abspath('certs/apiserver.crt'),
        lifecycle_env={
            'RAILGRID_KCP_KUBECONFIG': os.path.abspath('.kcp/admin.kubeconfig'),
            'RAILGRID_PROVIDER_KCP_SERVER': hub_internal_url,
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
        kcp_loopback_proxy='host.docker.internal:%s' % hub_port,
    )
    print('  external providers: %s (from %s)' % (', '.join(external_providers), external_providers_dir))
