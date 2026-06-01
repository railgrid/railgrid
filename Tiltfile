# Tiltfile — local dev for kedge hub + portal
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
)
