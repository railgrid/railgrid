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
