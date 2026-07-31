# Platform-internal networking — provider → tenant workload

Status: **proposed** · Date: 2026-07-31 · Author: design note
Related: [`providers.md`](./providers.md) (isolation rules),
[`provider-connectivity-contract.md`](./provider-connectivity-contract.md),
[`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md) (the
data-plane contract this extends), [`agent-web-access.md`](./agent-web-access.md)
(the flow that exposed the gap), [`home-assistant-edge-control.md`](./home-assistant-edge-control.md)
(the edges `Service` CR, the shape worth copying).

## The problem

A provider that needs to call a tenant workload has exactly one address today:
the workload's **public** HTTPRoute hostname. In production that resolves to the
Cloudflare edge, so a call from a provider pod to a workload *in the same
cluster* leaves the cluster, crosses the public internet, and re-enters through
the outbound tunnel.

That is not a rough edge, it is the only path. It forces every internally-called
workload to be publicly exposed, to carry its own authentication because it is
on the internet, and to wait on DNS + TLS provisioning before a provider can
talk to it. In local development it does not work at all: the gateway has only
cluster-internal addresses, so the published URL is unreachable from the host
where providers run.

The `searxng` and `browser` templates made this concrete. Everything built
around them — public HTTPRoute, an nginx bearer-token sidecar, a token bridged
from the tenant workspace into the runtime namespace, and (briefly) a
`connection.spec.allowedHosts` field asking users to authorise their own
configuration — exists **only** to compensate for the absence of an internal
path. None of it is inherent to running a search backend.

## What already exists

Evidence gathered from the codebase; `file:line` references are as of
2026-07-31.

**The transport is already there and it is not pod-to-pod.** The infrastructure
provider's data-plane proxy reaches a workload through the runtime
kube-apiserver's `services/proxy` subresource
(`providers/infrastructure/dataplane/proxy.go:117-130`), authenticating with the
provider's runtime credential (`dataplane/runtime.go:30-50`). No pod routing, no
service mesh, no shared pod CIDR — a kubeconfig is sufficient, which is exactly
why it keeps working if the runtime cluster is a different cluster.

Verified live against a running instance:

```
GET /api/v1/namespaces/<tenant>-default/services/search:8080/proxy/healthz  → OK
GET /api/v1/namespaces/<tenant>-default/services/search:8080/proxy/search   → 401 (the workload's own gate)
```

**The contract is already there.** `Template.spec.dataPlane` declares named
verbs that resolve to a Service in the instance's runtime namespace
(`apis/v1alpha1/types_template.go:391-405`), served by one generic handler:
"the verb set is declared, not hardcoded" (`app-studio-runtime-decoupling.md:74`).
Its security invariant is namespace confinement — every resolved Service and
Secret must live in the instance's own runtime namespace
(`dataplane/resolver.go:24-31,212-227`), so a forged status cannot redirect the
proxy elsewhere. App Studio already uses it for logs, sync, and restart.

**The isolation rule that constrains any design** (`providers.md:169-191`): a
provider's backend is private; cross-provider interaction goes through the
owning provider's *published* API surface, as the caller, routed by binding —
never a URL or credential into another provider's backend. The data-plane proxy
is a published API surface, so calling it is sanctioned. Addressing a workload's
ClusterIP directly is not, even though nothing on the network would stop it
(there are no NetworkPolicies anywhere in the repo).

## Why the existing path does not close the gap

Three specific things are missing. They are independent, and the first two are
what make internal traffic impossible today.

### 1. No production data-plane verb reaches a workload's own port

No shipped template declares a `proxy` verb. `application.yaml`,
`simple-webapp.yaml`, and `worker.yaml` declare `status` + per-component
`sync/restart/env/log`, all pointed at a **`control`** Service that exists only
in development mode (`backend/kro/devoverlay.go:243-268`); the template says so
outright: "in production mode these verbs answer 409 instance-not-ready — the
data plane is a development-mode surface" (`application.yaml:361-364`).
`searxng.yaml` and `browser.yaml` — whose entire purpose is being called by
another provider — have **no `dataPlane` block at all**.

The `proxy` verb was designed (`app-studio-runtime-decoupling.md:104-121`) and
its constant exists (`app-studio/api/dataplane_client.go:41`), referenced only
from a test. Nothing about the apiserver hop is development-specific. The
contract simply never declared a production endpoint.

### 2. No identity a provider can hold to act as itself

The data-plane handler requires a caller bearer token and rejects the request
without one (`dataplane/handler.go:82-86`); its authorisation gate is a
tenant-scoped `GET` of the instance CR **as the caller**. That is a good gate
for request-driven work — App Studio relays the browser user's token — but it
closes the path to anything not driven by a live human:

- background agent runs carry no user token by construction
  (`agents/api/toolset.go:170-174`);
- so do controllers, schedulers, and webhooks.

Worse, a provider cannot authenticate to the hub as itself at all: the hub's
`IdentifyUser` accepts only static dev tokens and OIDC ID tokens
(`pkg/server/proxy/proxy.go:809-849`), so a kcp ServiceAccount token arrives
with **no** `X-Kedge-*` headers.

Prior art exists for the missing half: the MCP aggregator already makes direct
in-cluster calls to providers while **self-asserting** the identity headers the
backend proxy would normally inject (`pkg/hub/mcpaggregate/federation.go:236-241`).
The hub can vouch for a caller on its own authority; there is simply no
equivalent for a provider.

Also proven in-repo: the edges provider authorises tunnel callers with a
delegated **TokenReview + SAR through the consumer's APIExport virtual
workspace** (`providers/edges/internal/tunnel/auth.go:116-189`) — the standard
auth-delegator pattern, which kcp's own e2e endorses for exactly this case.

### 3. Hub → provider addressing assumes one cluster

Every `CatalogEntry` registers `http://<name>.<ns>.svc.cluster.local`
(e.g. `providers/agents/deploy/chart/templates/catalogentry.yaml:19,25`), and in
operator mode the infra provider registers a `.svc.cluster.local` name for a pod
deliberately placed on the *runtime* cluster
(`providers/infrastructure/operator/catalogentry.go:84-89`,
`operator/serve.go:32-52`). Provider→hub is configurable; hub→provider is not.

So the claim in `agent-web-access.md` that "the agents provider may run on a
different cluster than the instance" is **aspirational**: if that were true today
the hub could not reach the provider at all. The public-exposure decision was
justified by a topology the platform does not yet support.

## Options considered

| Option | Verdict |
|---|---|
| **Direct ClusterIP / pod DNS** from provider to workload | Rejected. Works on the network (one flat cluster, no NetworkPolicy) but violates `providers.md:184-191`, encodes another provider's backend topology, and breaks the moment the runtime cluster is separate. |
| **Service mesh** (Istio/Linkerd multicluster, Cilium ClusterMesh) | Rejected as a requirement. Operators must not have to install a mesh; local dev on one kind cluster must work with zero setup. A mesh may sit *under* this design later, but the platform cannot depend on it. |
| **MCS API** (ServiceExport/ServiceImport) | Rejected for now — implementation-dependent, needs cooperating CNIs and non-overlapping CIDRs, and buys nothing in the single-cluster case that dominates today. |
| **New reverse tunnel** per workload | Rejected. Issue #351 already ruled: "do not build a second tunnel system." revdial also pins its terminator to a single replica (`provider-sdk/revdial/revdial.go:88-91`) — a constraint worth containing to edges, not spreading. |
| **kcp APIExport custom subresource** as transport | Already rejected once (`app-studio-runtime-decoupling.md:165-169`) as unproven; the provider mux behind the hub proxy is "proven in-repo". No new evidence to reopen it. |
| **Extend the existing data-plane contract** | **Recommended.** The transport, the namespace confinement, the generic handler, and the declarative verb model all exist and are in production use. |

## What comparable platforms do

Surveyed July 2026: Argo CD + argocd-agent, Backstage, Port, Cortex, Humanitec,
Radius.

**None of them HTTP-call the workload.** Every one reaches workloads through the
**kube-apiserver**, and where they need live access — Argo CD's pod terminal,
Backstage's shell-into-pod, argocd-agent's log streaming — it is expressed as
`pods/log` / `pods/exec` **subresource** calls, not a request to the
application's own port. Radius's control plane has no edge to a workload at all;
its application graph is computed from stored resource records rather than by
probing anything. Backstage's generic `proxy-backend` *can* be pointed at an
in-cluster Service, but that is configuration, not architecture.

This is direct support for the transport recommended here. One caveat worth
carrying: the industry precedent is heavy on `pods/log` and `pods/exec` and thin
on `services/proxy`, so our use of the latter is less well-trodden — see open
question 1.

**For multi-cluster, the industry converged on outbound agents**, not meshes and
not routed access: argocd-agent (v0.9.0, now shipping in OpenShift GitOps 1.19),
Port's execution agent, Cortex's Kubernetes agent, the Humanitec Agent. The
stated motives are identical each time — no inbound firewall holes, no cluster
credentials held centrally, tolerance of NAT and intermittent links, and hub
scalability. argocd-agent's **resource proxy** is the closest analogue to
kedge's problem: a hub-side UI operating on data-plane pods with zero inbound
connectivity, riding the agent-initiated stream back down.

kedge already has that machinery in revdial + the edges `Service` CR. This
strengthens both the Phase 3 recommendation (generalise what exists) and issue
&#35;351's instruction not to build a second tunnel system.

**Hosted control planes are unanimous, and their reasoning is ours.** Gardener,
HyperShift, k0smotron, Kamaji and vcluster all invert the connection: the data
plane dials out, the control plane never dials in. Gardener documented the
migration cost that forced it — a **public load balancer per tenant cluster, at
roughly €20/month each**, plus security policies that simply forbade
inbound — which is the same bill kedge is signing up for with a public HTTPRoute
per workload.

Two ideas from that group are worth stealing:

- **The apiserver proxy subresource is the "free tunnel."** Once *one*
  control-plane→data-plane path exists, `nodes/<n>/proxy` and `pods/…` turn it
  into general-purpose reach for any component that can talk to the apiserver.
  Gardener uses it to keep seed-side Prometheus out of the VPN entirely;
  vcluster builds its whole logs/exec story on the host apiserver's
  `pods/log|exec|attach|portforward` and `nodes/proxy`. This is the strongest
  outside validation of Phase 1.
- **Multiplex a fleet behind one ingress by CONNECT header, not per-tenant
  routes.** Gardener runs a single L4 gateway per seed and selects the tenant
  from an `X-Gardener-Destination` header mapped straight to an Envoy cluster
  name, with a regex allowlist so the port cannot be used as an open proxy —
  far cheaper than HyperShift's per-tenant Route or Kamaji's per-tenant LB. If
  kedge ever does need public exposure at fleet scale, that is the shape.

Note the one dissenter: Gardener evaluated Konnectivity, ran it in alpha for
about a year, and replaced it with a home-grown reversed OpenVPN over concerns
about maturity — a 2020/21 judgement worth re-testing rather than inheriting.

**Cluster API is the precedent that matters most for the transport.** Its
controllers reach **etcd static pods** in a workload cluster through that
cluster's own apiserver `pods/portforward` subresource
(`controlplane/kubeadm/pkg/proxy` → `pkg/etcd`), precisely so the management
cluster never needs pod-network reachability. Same shape as this proposal:
make the workload's apiserver the relay.

**And kcp already built the Phase 3 design, then deleted it.** The TMC syncer
tunnel modelled the reverse tunnel as an **RBAC-gated subresource on a
first-class object** — `synctargets/<name>/tunnel` — with the far end
re-proxying into the physical cluster's *own* apiserver so authorisation
composed and no kubelet was ever touched directly. It went per-shard (kcp
&#35;2946), the same sharding problem kedge has hit. It was removed in June 2023
for **lack of maintainers, not because the design failed**, and the code is
intact and Apache-2.0 on `main-pre-tmc-removal:pkg/tunneler/`. Nothing in the
kcp ecosystem replaced it: neither api-syncagent nor multicluster-provider nor
KubeStellar/OCM transport offers logs/exec. That hole is exactly what kedge's
edges provider fills.

One caution inherited from that history: extending the tunnel to
`services/proxy` was attempted in kcp &#35;2910 and abandoned, its unresolved
question being *what a Service means when it maps to several targets*. If the
edges `svc/` proxy is ever generalised beyond one edge, that question returns.

## How `services/proxy` actually behaves

Properties that decide where this mechanism may and may not be used. These are
architectural, not version-specific.

- **It resolves to a random ready Endpoint (a pod IP), not the ClusterIP.**
  kube-proxy is bypassed entirely: no session affinity, no topology-aware
  routing, and "load balancing" is a fresh random pick per request. Fine for a
  single-replica instance; **wrong for anything stateful across replicas** — the
  browser template is session-oriented and must stay at one replica, or move off
  this path.
- **The apiserver must be able to route to pod IPs.** True on kind, kubeadm, and
  standard managed clusters. Where it is not, **Konnectivity is the drop-in
  fix** — it and `services/proxy` are not alternatives, they *stack*: the
  subresource is the API surface, Konnectivity supplies reachability. Adopting
  this path therefore does not paint us into a corner on network topology.
- **WebSocket and SPDY upgrades work** (`UpgradeAwareHandler`), and streaming
  works with `FlushInterval: -1` — which `dataplane/proxy.go` already sets. The
  browser template's MCP endpoint is viable.
- **gRPC does not work.** The upstream leg is HTTP/1.1-shaped; h2c
  prior-knowledge and trailers do not survive. This must be stated in the
  `Template.spec.dataPlane` contract so template authors do not discover it in
  production.
- **`proxy` is a long-running subresource, so it is exempt from
  `--request-timeout`** — long-lived streams are fine.

### The volume boundary — the most important operational fact here

Because `proxy` is long-running, **it is also exempt from API Priority and
Fairness.** APF will not throttle these calls, and will not protect the
apiserver from them either. The traffic is unaccounted: it still consumes
apiserver goroutines, memory, file descriptors and TLS connections.

So the line is:

- **Control-plane-shaped calls** — health, logs, sync, restart, an occasional
  API call, a search query — are entirely fine and widely deployed this way.
- **Sustained end-user data traffic** through the apiserver is considered abuse
  of the control plane, and nothing will stop it degrading the cluster for
  everything else.

Search queries from agents sit on the correct side of that line at expected
volumes; a preview iframe streaming video does not. Any verb that could carry
bulk user traffic belongs on the Gateway path, and the provider should rate-limit
its own data-plane calls rather than relying on the apiserver to push back.

Security note: the apiserver-as-confused-deputy is a **recurring** bug class on
this surface (CVE-2018-1002105, CVE-2020-8562), not a one-off — which is why the
resolver's namespace confinement should be backed independently by scoping the
runtime credential's RBAC, rather than being the only control. Scope it to
`services/proxy` on the instance namespaces and **never** grant `nodes/proxy`:
a GET on that plus node write is a known cluster-admin RCE path that upstream
has closed as working-as-intended.

Two more properties that shape the contract:

- **The caller's credential does not reach the backend** — the apiserver strips
  `Authorization` on the proxy hop. Our data plane already accounts for this
  (it authenticates to the apiserver with the runtime credential and injects the
  workload token under a separate header), but it means a workload can never see
  the end user; it sees the platform.
- **HTTP/HTTPS only**, and the apiserver does not validate the backend's
  certificate on the `https:` form — fine for an in-cluster hop, not something
  to extend across an untrusted network.

### Is this exotic? No — but it is not a bulk data path either

Shipped, in production, on the proxy subresource: **Rancher's UI** (its
Grafana/Longhorn/monitoring links are `services/proxy` URLs), **kubeseal**,
which fetches the sealing certificate through `services/proxy` on *every*
invocation in CI pipelines everywhere, the **AWS VPC CNI metrics helper**
(`pods/proxy`), **Sonobuoy** (`nodes/proxy`), and `minikube dashboard`. It is
also a **CNCF conformance test** for both `services/proxy` and `pods/proxy`, so
every certified cluster must support it — this is a portability guarantee, not a
vendor promise.

The counterweight is equally concrete. Prometheus shipped `nodes/…/proxy`
scraping as its documented default from 2017 and **deleted it in July 2020** in
favour of scraping kubelets directly; the prometheus-operator maintainer's
verdict on the pattern was "scalability and correctness issue… an additional
indirection". And Kubernetes excludes `proxy` from its own API responsiveness
SLO in code, grouped with `exec`/`log`/`portforward` as arbitrarily long.

There is, notably, **no formal upstream statement** that the proxy subresource
must not be a data path — the strongest evidence is that SLO exclusion plus two
maintainer comments. So this is a judgement about volume, not a rule being
broken. Request/response, low-rate, admin-plane HTTP where you want the
apiserver's authn/authz and don't want to run a tunnel is exactly the shape that
survives in production.

**The escalation path, if this outgrows the proxy subresource**, is an
aggregated API server exposing custom subresources — what metrics-server and
KubeVirt do (KubeVirt serves `/console` and `/vnc` this way). That buys purpose-
built RBAC verbs instead of the blunt `get services/proxy`, your own auth to the
backend, and a latency profile you own. It is a bigger build, and nothing here
needs it yet.

## Recommendation

Three phases. Phase 1 is small and unblocks interactive use immediately; Phase 2
is the real gap; Phase 3 is only needed for genuine multi-cluster.

### Phase 1 — declare production data-plane endpoints

Add a `dataPlane` block with a `proxy` verb to `searxng.yaml` and `browser.yaml`
pointing at the instance's own Service (not a dev control sidecar), so a caller
can reach:

```
{hub}/services/providers/infrastructure/dataplane/clusters/{clusterID}/searxngs/{name}/proxy/search?q=…
```

The agents provider's `websearch` connection then takes an **instance
reference** instead of a URL, and the search client calls the data-plane path.
Consequences, all simplifications:

- **no public HTTPRoute** for internally-called instances;
- **no auth sidecar and no token** — the data-plane gate (kcp RBAC on the
  instance) replaces the bearer gate, so the nginx sidecar, the pwgen Job, and
  the tenant-Secret token bridge all become unnecessary;
- **local development works unmodified** — the path goes hub → provider →
  runtime apiserver, none of which needs a public hostname;
- `allowedHosts` stays deleted; there is no private address for a user to
  authorise because the caller never dials the workload directly.

Public exposure becomes opt-in for instances a *human* needs to reach, which is
what it was always for.

### Phase 2 — a platform identity for provider-initiated calls

Give a provider a way to say "I am the agents provider, acting within tenant
workspace X" without a human's token. Two candidate mechanisms, both with
in-repo precedent:

1. **Hub-vouched provider identity** — the hub accepts a provider's SA token in
   `IdentifyUser` and asserts `X-Kedge-Provider` alongside the tenant headers,
   exactly as `mcpaggregate` already does on its own authority.
2. **Delegated SAR at the receiving provider** — the caller presents its provider
   SA token and the target provider authorises with TokenReview + SAR through
   the tenant's APIExport VW, the pattern `edges/internal/tunnel/auth.go`
   already implements.

(2) is preferable: authorisation stays with the resource owner and kcp RBAC
remains authoritative, consistent with RFC 010's "no platform component in the
request path re-deriving identity". The provider identity must be **scoped to
workspaces the provider is actually bound to**, and the audit trail must record
both the provider and the tenant.

**The credential should be an audience-scoped bound ServiceAccount token**, not
a static secret. The calling provider mints one via the `TokenRequest`
subresource (or a projected volume) with `audience` set to the *receiving*
provider, and the receiver validates it with `TokenReview` **passing
`spec.audiences`**. This is the mechanism Kubernetes documents for
service-to-service auth, and it has three properties that matter here:

- it is short-lived and auto-rotated, so no long-lived credential is stored or
  bridged anywhere;
- the audience makes it **non-replayable against the control plane** — a token
  minted for the infrastructure provider cannot be used against the apiserver;
- `TokenReview` is the only validation path that honours bound-object
  revocation (a JWT verified offline stays valid until `exp` regardless).

Two rules to encode, both easy to get wrong:

- **Always verify the audience on the receiving side.** Sending a
  default-projected token instead hands the receiver a credential that can talk
  to your apiserver.
- **Authorise on the `(issuer, subject)` tuple, never the subject alone.**
  `system:serviceaccount:default:default` exists in every cluster on earth.

If the platform ever spans clusters, the same token validates cross-cluster via
OIDC discovery against the issuing apiserver's JWKS — the mechanism every cloud
IAM federation already uses — at the cost of losing revocation, so keep
`expirationSeconds` near the 600s floor there. Longer term, KEP-4317 **Pod
Certificates** (beta 1.35, stable targeted 1.37) gives mTLS with kubelet-managed
identity and is a strictly better east-west story than bearer tokens; worth
tracking, not worth waiting for.

**kcp already models "acting for" and we should use it rather than invent a
claim scheme.** kcp's authorization carries two extras:
`authorization.kcp.io/warrant` and `authentication.kcp.io/scopes`
(`cluster:<name>`). The semantics are exactly the distinction this design needs:
*a warrant lets the bearer act **under the permissions of** another identity,
but not **as** that identity* — audit and admission still see the primary user.
Warrants nest, and each carries its own scope, so a delegated grant is confined
to one logical cluster. The APIExport virtual workspace already uses this
(`pkg/virtual/apiexport/builder/build.go`): it impersonates the consumer's user
and appends a warrant for a synthetic service account, so the tenant's identity
drives audit while the platform's warrant supplies the extra capability.

Two rules to carry over from that design:

- **A delegation marker must not be self-assertable.** kcp's synthetic groups
  (`system:kcp:initializer:<path>`) are stripped at the front proxy and injected
  only by the in-process component that has already performed the authorisation.
  Any header or extra kedge uses to say "acting for tenant X" needs the same
  treatment at the hub boundary — and the hub already strips and re-injects
  `X-Kedge-*` for exactly this reason.
- **Do not reach for impersonation as the primary mechanism.** It makes the
  caller *become* the target, losing the audit trail and requiring a very
  powerful grant; kcp has already shipped a security advisory where
  impersonation reached global administrative groups
  (GHSA-c7xh-gjv4-4jgv).

This phase is what lets a scheduled agent run search at 3am. Until it lands,
Phase 1 serves interactive runs only — which is a strict improvement on today,
where nothing works internally at all.

### Phase 3 — make hub → provider addressable (only for real BYO compute)

Registration must carry a reachable URL rather than assuming in-cluster DNS.
This is the same problem the edges tunnel already solves for clusters the
platform cannot route to, and the `Service` CR
(`providers/edges/apis/v1alpha1/types_service.go:139-171`) is the right shape to
generalise: a declarative `{targetRef|host, port, scheme, authSecretRef}` that
resolves to a proxied endpoint, SAR-gated, with credential swap. Do not start
here — nothing needs it until a runtime cluster is genuinely separate.

**Do not buy a tunnel for this.** Every commercial option meters an axis that
scales with tenant count — inlets ~$25/tunnel/month, ngrok per-GB plus
per-100k-requests, Cloudflare and Tailscale per-seat plus per-tagged-resource —
so a per-tenant tunnel is a per-tenant bill. Teleport is technically the closest
fit (its reverse tunnel and machine identity are one system) but its Community
licence forbids embedding in a product. frp is cleanly licensed but has **no
server clustering at all** (open feature request since 2025).

Two things to inherit from that landscape when Phase 3 comes:

- **The single-replica constraint is not a kedge failing, it is the shape of the
  problem.** A tunnel is one long-lived connection, so an L4 balancer pins it to
  one backend for its lifetime and scaling out does nothing until connections
  churn. Teleport hit the extreme version of this — every agent dialling every
  proxy exhausted ephemeral ports behind a NAT gateway, so *adding* proxies
  reduced capacity — and solved it by changing the fan-out (proxy peering plus a
  configurable per-agent connection count), not by rebalancing. That is the
  known-good pattern if revdial's single replica ever has to go.
- **Design for the boring failure modes up front**: jittered backoff (reconnect
  storms after a control-plane restart), application-level keepalives (NAT drops
  an idle mapping and both ends still believe the tunnel is alive), and MTU
  (double encapsulation silently blackholes large packets — TLS handshakes hang
  while small requests succeed). kedge's existing 18s ping / 60s read deadline
  already covers the second.

**On tunnel HA there are exactly two options, and no library solves it for
you.** Either the agent opens one tunnel *per frontend replica* (Konnectivity's
`--server-count`; kcp's per-shard tunneler, added in kcp &#35;2946 for precisely
this reason), or the ingress does *connection-aware routing* to the replica that
owns the tunnel (Gardener routes by SNI to the right control-plane pod). revdial
cannot escape this: `NewDialer` closes over the accepted socket, so only the
process holding that file descriptor can dial back.

If Phase 3 ever happens, **`sigs.k8s.io/apiserver-network-proxy/konnectivity-client`
is worth evaluating before extending revdial**. `Tunnel.DialContext` has exactly
the `func(ctx, network, addr) (net.Conn, error)` shape, so it drops into
`http.Transport`/`rest.Config.Dial`; the client is its own lightweight module
(gRPC + protobuf + klog, no client-go); and OCM `cluster-proxy` and
`cluster-gateway` both embed it this way. Countervailing facts: KEP-1281 never
reached GA and its tracking issue was **closed as `not_planned` in September
2025** with no owner (the repo is healthy — v0.36.0, June 2026 — so this is a
governance signal, not a code signal); tunnels are single-use, so you pay a dial
per logical connection; there is no tenancy model, you build that layer; OCM
documents roughly **50% throughput overhead**; and an open head-of-line-blocking
bug (&#35;881, filed July 2026 by a maintainer) means one slow reader can stall
every connection on an agent stream. HyperShift sidesteps the library entirely
by running the server in `http-connect` mode and dialling it with ~40 lines of
`tls.Dialer` + `CONNECT` — language-agnostic and zero module weight.

Note also that kcp's tunneler depends on `github.com/aojea/rwconn`, last
published in 2022 — a supply-chain consideration if that variant is copied.

## Consequences for what was just built

**Status: Phase 1 is implemented.** The `searxng`/`browser` scaffolding is
deleted — the HTTPRoute, the nginx auth sidecar and its ConfigMap, the
`tokenSecretRef`/`tokenSecretName` inputs, `status.url`/`status.host`/
`status.mcpURL`, and `tokenbridge.go` with its finalizer. Both templates declare
a `dataPlane` `proxy` verb over their Service instead; both apps bind `0.0.0.0`;
`searxng`'s pwgen Job survives only to mint SearXNG's own `server.secret_key`.
On the agents side a `websearch` or `mcp` Connection names `config.instance`
instead of a URL and a token, and `tools.DataPlane.ProxyURL` composes the call.
See [agent-web-access.md](agent-web-access.md).

This note said the templates should keep **optional** public exposure for
instances a human wants to open in a browser. They do — but gated by
**oauth2-proxy**, not by the bearer token that was deleted. That distinction is
what makes it not a second path: the OIDC gate and its client-secret bridge
already existed for the `application` template, so exposure reuses shared
machinery instead of resurrecting a mechanism that existed only because the
internal path was missing. `expose.enabled` defaults to false, every exposure
resource hangs off that one condition, and `oidc.mode: none` is not offered —
a public instance without a gate is an open metasearch proxy or a remotely
driven browser.

Templates now declare **`spec.exposure`** (`internal` | `optional` | `public`),
which is what the portal and the MCP tools read to decide whether to promise a
URL. The kro backend rejects a template whose marker contradicts its graph — an
`internal` template carrying an HTTPRoute, or an `optional` one whose route has
no `includeWhen` — so the marker cannot drift away from what is deployed.

The caller-authorizes property initially left background and scheduled runs
unable to use an instance-backed tool at all. That is now closed, not by giving
the provider an identity of its own, but by giving **each agent** a ServiceAccount
in the tenant workspace and having background runs act as it — the same shape
the hub already uses for per-MCPServer identities. Interactive runs still act as
the human. See the background-runs section of
[agent-web-access.md](agent-web-access.md) for what is created and the two
properties worth knowing: the grant is workspace-wide rather than
connection-scoped, and the token is long-lived because kcp has no TokenRequest
API.

## Open questions

1. **Does `services/proxy` handle everything we need?** Verified for plain HTTP
   GET. Not yet verified: streaming/SSE, WebSocket upgrade (the browser
   template's MCP endpoint needs it), and large bodies. The data-plane path
   already traverses two proxies, and `app-studio-runtime-decoupling.md:177-179`
   flags exactly this as unvalidated. Note the industry precedent is almost
   entirely `pods/log` / `pods/exec`, not `services/proxy` — the subresource we
   depend on is the less-exercised one, so validate it rather than assuming
   parity. If it disappoints, addressing a **pod** subresource instead (the
   path everyone else uses) is the fallback, at the cost of losing Service-level
   load balancing.
2. **Rate and size limits of the apiserver proxy** under agent-scale traffic —
   this puts tenant data-plane traffic through the kube-apiserver, which is a
   control-plane component. Needs a load sanity-check before it carries search
   traffic for many agents.
3. **Should the data plane be the transport for *all* internal calls**, or only
   provider→workload? Provider→provider already has the backend proxy; keeping
   two mechanisms is fine if the boundary is stated.
4. **Per-user identity downstream** (issue #398) is unresolved for the tunnel and
   would be equally unresolved here: after Phase 2, a workload sees a platform
   identity, not the human behind it.
5. **Egress policy.** With no NetworkPolicy anywhere, "internal" is a convention
   rather than an enforced boundary. Phase 1 removes the *need* for workloads to
   be public but does not stop them being reachable if exposed.
