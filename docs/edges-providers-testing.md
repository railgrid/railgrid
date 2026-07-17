# Testing the edges provider

The edge connectivity plane is one standalone, optional provider (`edges`)
holding both connectable kinds under a single group `edges.kedge.faros.sh`:

| Kind | Resource | Data plane |
|---|---|---|
| `KubernetesCluster` | `kubernetesclusters` | kubectl / MCP down the tunnel |
| `LinuxServer` | `linuxservers` | SSH / MCP down the tunnel |

It is **self-contained**: one pod terminates the agent reverse tunnel (revdial)
for both kinds with a single in-process ConnManager, owns the API + its
APIExport, runs the token/RBAC/lifecycle controllers (once per kind), and proxies
the data plane. The tunnel Server dispatches to the right kind by the resource
segment in the URL path. Shared connectivity code lives once in `provider-sdk`
(`revdial`, `ssh`, `wsutil`, `tunnel` (multi-kind), `edgectrl`, `edgeapi`,
`identity`); the provider instantiates it.

> **Single-replica constraint.** revdial registers tunnel dialers in a
> process-global map, so an agent's control connection and every later pickup
> connection must reach the same process. The provider runs **one replica**
> (chart `replicaCount: 1`, Deployment `strategy: Recreate`). HA is a v1 non-goal.

## What is testable today

The full **connectivity** path is wired and builds (core + `provider-sdk` +
`providers/edges`, under `go.work` and standalone/`GOWORK=off`):

- register a `KubernetesCluster` / `LinuxServer`, run the agent, tunnel connects
- `kubectl` streams through `/edgeproxy/.../kubernetesclusters/.../k8s`
- `ssh` streams through `/edgeproxy/.../linuxservers/.../ssh`
- the CLI verbs (`kedge edge|list|ssh|kubeconfig edge|agent|mcp`) address the
  `edges.kedge.faros.sh` group

Not yet rebuilt (feature substance, tracked separately): workload placement +
scheduler, the per-kind MCP tool families, the portals, and the agent workload
reconciler. These are **not** required to test connectivity.

## Dev loop (local, no containers)

Prereqs: a running hub with embedded kcp (`make run-hub-embedded-static`), which
writes `data/kcp/admin.kubeconfig`.

```sh
make install-provider-edges   # admin: apply Provider + CatalogEntry
# wait for the hub Provider controller to provision the workspace + SA token
make init-provider-edges      # bootstrap APIExport/schemas/slice/grant
make run-provider-edges       # serve (SINGLE process) on :8084

# in another terminal, register + connect a Kubernetes edge
kedge edge create my-cluster --type kubernetes
kedge edge join-command my-cluster       # prints the agent invocation
# run the agent (--type kubernetes), then:
kedge edge list                          # my-cluster → Connected/Ready
kedge kubeconfig edge my-cluster > /tmp/edge.kubeconfig
kubectl --kubeconfig /tmp/edge.kubeconfig get nodes   # streams down the tunnel

# a server edge is the same provider, different resource:
kedge edge create host1 --type server
kedge ssh host1
```

## Container / Helm deploy

```sh
# Build (context = REPO ROOT, so the local provider-sdk replace resolves):
make docker-build-edges-provider

# Install the chart. Requires a provider-kubeconfig Secret (workspace-admin
# kubeconfig minted via /bonkers) named per values.providerKubeconfig.secretName.
helm install edges providers/edges/deploy/chart \
  --namespace kedge \
  --set image.repository=ghcr.io/faroshq/kedge-edges-provider \
  --set hub.url=https://kedge-hub.kedge.svc.cluster.local:9443 \
  --set hub.externalURL=https://<public-hub-url>
```

The init container bootstraps the APIExport (both KubernetesCluster + LinuxServer
schemas baked at `/etc/kedge/schemas`); the serve container terminates tunnels +
runs controllers. The chart renders the `CatalogEntry` into a ConfigMap the init
container applies into the provider workspace (it is a kcp resource, not a
host-cluster one).

## The one thing only a live run can prove (runtime spike)

Everything above is verified at build/lint level. The **unproven** runtime
invariant is the reverse-tunnel handshake surviving the extra hop through the hub
backend proxy:

1. Agent dials `…/services/providers/edges/agent/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/{kubernetesclusters|linuxservers}/{name}/proxy`.
2. The hub backend proxy (`NewBackendProxy`, `FlushInterval:-1`) forwards the
   `Connection: Upgrade` / `101` to the single provider replica.
3. The provider upgrades, calls `revdial.NewDialer(conn, /services/providers/edges/agent/proxy)`,
   and the agent re-enters via `…/services/providers/edges/agent/proxy?revdial.dialer=<id>`.
4. The `X-Kedge-Agent-Kubeconfig` / `X-Kedge-Agent-Token` handshake headers ride
   the `101` and must survive the proxy hop (and any CDN in front of the hub).

**If a CDN strips the `101` headers**, fall back to the agent fetching the
kubeconfig via a normal follow-up request instead of on the upgrade response.

**403 on the data plane** means the tenant workspace is missing the edge-proxy
grant — `EnsureProviderEdgeProxyGrant` grants the provider SA `proxy` on
`edges.kedge.faros.sh` (resources `kubernetesclusters` + `linuxservers`). It
fires on tenant Enable when the CatalogEntry has `spec.edgeProxyAccess`.
