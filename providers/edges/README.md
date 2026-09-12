# edges provider

Connectivity core for faros. Owns `edges.faros.sh`: `KubernetesCluster` and
`LinuxServer` edges, the agent reverse tunnel, `Service` connectors, and
`Workload` / `Placement` scheduling.

An **edge** is a cluster or host you connect to faros. The agent you install
there dials *out* to the platform and holds open a WebSocket reverse tunnel
(revdial), so nothing needs an inbound firewall hole, a VPN, or a public IP. The
provider terminates those tunnels and re-exposes each edge as data-plane
subresources on its CR (`…/k8s`, `…/ssh`, `…/mcp`).

## APIs

| Kind | Scope | What it is |
|---|---|---|
| `KubernetesCluster` (`kc`) | Cluster | A connected Kubernetes cluster. |
| `LinuxServer` (`ls`) | Cluster | A connected host, reached over SSH. |
| `Service` (`edgesvc`) | Cluster | A host/LAN app on an edge, surfaced as an MCP tool. |
| `Workload` (`wl`) | Namespaced | Manifests to deploy onto edges. |
| `Placement` | Namespaced | Binds a `Workload` to an edge. |

APIExport: `edges.providers.faros.sh`, in `root:faros:providers:edges` (or your
own workspace when self-hosted).

## Connecting an edge

1. Create the CR — `faros edge create`, or apply a `KubernetesCluster`.
2. The provider mints a one-time **join token** into `status.joinToken` and sets
   `Registered=False/AwaitingAgent`.
3. Install the agent with that token. It dials the tunnel endpoint, and the
   upgrade response hands back a kcp ServiceAccount kubeconfig scoped to your
   workspace. The agent then swaps to that credential and the join token is
   cleared.
4. Reconnects authenticate with the ServiceAccount token, authorized per-edge:
   the SubjectAccessReview checks the `proxy` verb on *that* edge by name, so one
   edge's credential cannot drive another.

Revoking access is deleting the edge — that garbage-collects the ServiceAccount
and its grants.

## Workloads

A `Workload` (namespaced on the hub) is rendered by the provider into a
manifest bundle and fanned out as one `Placement` per selected
`KubernetesCluster`; the edge agent applies the bundle with server-side apply
and prunes what disappears. `Placement.spec.manifests[]` is the rendered
output — read it to see exactly what lands on the edge.

```yaml
apiVersion: edges.faros.sh/v1alpha1
kind: Workload
metadata:
  name: kiosk-whoami
  namespace: kiosk            # hub namespace only; NOT where it runs on the edge
spec:
  targetNamespace: kiosk      # edge namespace (default "default"); created if missing
  placement:
    strategy: Spread          # or Singleton
    edgeSelector:
      matchExpressions:
        - {key: edges.faros.sh/name, operator: In, values: [home, minis]}
  replicas: 1                 # per edge
  simple:
    image: ghcr.io/example/app:1.0
    ports: [{name: http, containerPort: 80}]
    imagePullSecrets: [{name: ghcr-pull}]   # must already exist in targetNamespace on each edge
```

- `spec.targetNamespace` — the edge namespace for every mode (`simple`,
  `template`, `helm`). The hub namespace is never carried over. Any value
  other than `default` makes the bundle start with the `Namespace` object, so
  the agent creates it when missing; an existing namespace is reused and is
  never deleted with the Workload.
- Exactly one of `simple`, `template`, `helm`. `simple` renders a Deployment
  (+ a ClusterIP Service named after the Workload when `ports` are set);
  `simple.imagePullSecrets` names docker-registry Secrets that must already
  exist in the target namespace on each edge — the Workload ships no Secrets.
- `template` is a pod template: `template.metadata.labels` / `.annotations`
  land on the pods (the provider's `edges.faros.sh/workload` selector label is
  always added and cannot be overridden) and `template.spec` is a full PodSpec
  (so `imagePullSecrets`, volumes, sidecars, …).
- `helm` templates the chart hub-side into the target namespace
  (`{{ .Release.Namespace }}`); objects the chart leaves namespace-less are
  stamped with it, cluster-scoped kinds are not. `fullnameOverride` is forced
  to the Workload name.

## Scaling

The provider is horizontally scalable. Each agent holds exactly one control
connection; the replica that terminates it claims ownership in a `Lease`, and
any other replica receiving a request relays it to the owner over a pod-to-pod
internal port that is deliberately not on the Service. Agents treat the pickup
path as opaque, so scaling needs no agent change.

## Self-hosting

You can run edges in your own cluster instead of using the platform's — see
[docs/byo-providers.md](../../docs/byo-providers.md) and
[deploy/chart/README.md](deploy/chart/README.md). faros creates the workspace,
mints the credential, and generates the install commands under
**Providers → Self-Hosting** in the portal.

`hub.externalURL` is derived from the hub's own address: it is what this provider
bakes into the agent kubeconfigs it mints, i.e. where agents reach kcp *through*
the hub — not where they reach this provider.

## Further reading

- [docs/macos-edges.md](../../docs/macos-edges.md) — macOS `MacOSServer` service-edge test and runbook
- [docs/platform-internal-networking.md](../../docs/platform-internal-networking.md) — tunnel design and HA survey
- [docs/edges-marketplace.md](../../docs/edges-marketplace.md) — workload catalog
- [docs/provider-connectivity-contract.md](../../docs/provider-connectivity-contract.md)
