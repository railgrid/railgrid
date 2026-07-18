# Home Assistant control through kedge edges + AI agents

Status: **implemented** (phases 1–4); live end-to-end demo (phase 5) not yet run
Owner: mjudeikis
Scope: edges provider, edge agent (server mode), portal. **No agents-provider changes.**

## 1. What this does

Install the kedge linux agent on the server that runs Home Assistant (HA). The
agent auto-discovers HA (where and how it runs), the edges provider exposes it
through the existing reverse tunnel, and AI agents get HA tools — so a user can
type "open the gates" in chat (portal / Telegram / Discord) and it actuates.

The same machinery works for HA (or any HTTP service) running **inside a
KubernetesCluster edge**: declare a Service pointing at a Kubernetes Service and
the proxy, tools and credential handling are identical — see §3.

```
"open the gates" (portal chat / Telegram)
  → agents provider Eino loop (edges tool family — unchanged)
  → hub MCP aggregate → edges provider /mcp (merged)
  → <service>_ha_call_service → ConnManager dialer → agent /svc proxy
  → http://127.0.0.1:8123/api/services/cover/open_cover
     (Authorization: Bearer <HA token> injected provider-side)

discovery (control plane, separate loop):
  servicectrl discovery reconciler → tunnel → agent GET /api/v1/services
  → detectors run on host → upserts Service CRs in the tenant workspace
```

## 2. Design decisions (as built)

1. **All HA intelligence lives in the edges provider.** The agents provider's
   `edges` tool family already consumes the hub MCP aggregate, so the HA tools
   appear to AI agents with **zero agents-provider code changes**.
2. **A dedicated `Service` kind, not new fields on `LinuxServer`.** The
   connectable kinds stay lean (same split as Workload/Placement vs
   KubernetesCluster); services get their own lifecycle, reconcilers and RBAC,
   and one edge can carry many services without status bloat.
3. **The kind is `Service` (resource `services`), not `EdgeService`** — it
   already lives under group `edges.kedge.faros.sh`, so an "Edge" prefix is
   redundant. There is no core `services` to collide with: kcp workspaces are a
   control plane and don't serve core v1 workload resources. Short name
   `edgesvc`; fully-qualified `services.edges.kedge.faros.sh`.
4. **Discovery is provider-pulled** via a new agent `/api` management endpoint,
   not heartbeat-pushed — so `LinuxServerStatus` and the edge reporter are
   untouched, and no agent config sync is needed.
5. **Typed enums** for `spec.type` / `spec.scheme` (kubebuilder Enum), matching
   the existing `SSHUserMappingMode` convention.
6. **Credentials are injected provider-side.** The HA long-lived access token
   lives in a tenant Secret; it never reaches the agent host or the agent's
   config.

## 3. The `Service` API

`providers/edges/apis/v1alpha1/types_service.go` — cluster-scoped, in group
`edges.kedge.faros.sh/v1alpha1`.

```go
type ServiceSpec struct {
    EdgeRef       ServiceEdgeRef          // {kind: LinuxServer | KubernetesCluster, name}
    TargetRef     *KubeServiceRef         // {namespace, name} — required for KubernetesCluster, ignored for LinuxServer
    Type          ServiceType             // enum: home-assistant | generic  (generic = proxy-only, no tools)
    Scheme        ServiceScheme           // enum: http | https (default http)
    Port          int32                   // host loopback port (LinuxServer) or Service port (KubernetesCluster)
    AuthSecretRef *corev1.SecretReference // tenant Secret, key "token"; injected provider-side
}

type ServiceStatus struct {
    Phase       string             // "" | Detected | Ready | Unreachable
    Discovered  bool               // created/confirmed by the discovery reconciler
    Version     string             // image tag before validation; exact version after
    InstallType string             // container | core | haos | supervised
    URL         string             // externalized proxy-subresource base
    LastSeen    metav1.Time
    Conditions  []metav1.Condition // Detected, CredentialsValid, Ready
}
```

Discovery-created objects are named `<edge>-<type>` (e.g. `ha-box-home-assistant`)
and labelled `edges.kedge.faros.sh/edge=<edge>` and
`edges.kedge.faros.sh/discovered=true`. Users may also create Services by hand.

**Ownership contract** (load-bearing): the discovery reconciler owns
discovery-derived status only and never clobbers user spec (port overrides,
`authSecretRef`). A service that stops being detected goes `Detected=False` /
`phase=Unreachable`; it is deleted **only** if the user never configured it
(discovered label set and `authSecretRef == nil`).

### Two edge kinds, one data path

A Service can live on either connectable kind. Everything downstream — token
injection, the `proxy` subresource, the MCP tools, the validation reconciler —
is identical; only the address the agent dials differs.

| | LinuxServer | KubernetesCluster |
|---|---|---|
| Declared by | discovery (auto) | the user (`targetRef`) |
| Agent dials | `127.0.0.1:{port}` | `{targetRef.name}.{targetRef.namespace}.svc:{port}` |
| Agent guard | loopback only | loopback + cluster DNS |

**Why not the apiserver's `services/proxy` subresource?** It looks like the
obvious route for kube edges — the agent SA already holds `resources: ["*"]` on
the core group, and the existing `/k8s/` agent route would need no new code. It
doesn't work: that route **overwrites `Authorization` with the agent's SA token**
([pkg/agent/tunnel/server.go](../pkg/agent/tunnel/server.go)) because the hop has
to authenticate to the apiserver. There is one Authorization header and two
consumers, so Home Assistant's token could never reach HA behind it. Dialing the
Service directly keeps the apiserver out of the data path, so the
provider-injected header arrives untouched.

**Why kube services aren't auto-discovered.** A host has a handful of listening
ports; a cluster has hundreds of Services. Scanning them would be noisy and slow,
so kube services are declared. Declared objects are safe from the discovery
reconciler by construction: it only ever lists and prunes objects labelled
`discovered=true`.

## 4. Agent side (`pkg/agent/`)

- **[pkg/agent/discovery/](../pkg/agent/discovery/)** — `Detector` interface +
  `Run`. HA detector (`homeassistant.go`) probes cheapest-first: loopback
  `GET /api/` (HA's 401/200 is a reliable fingerprint) → docker/podman
  container matching `ghcr.io/home-assistant/home-assistant` (gives image tag +
  mapped port) → systemd units (`home-assistant*`/`hass*`) → HAOS/Supervised
  observer on `:4357`. Every probe failure means "not detected"; detectors never
  error the endpoint, and a panicking detector is isolated.
- **[pkg/agent/tunnel/server.go](../pkg/agent/tunnel/server.go)** — two new mux
  routes alongside `/ssh`, `/k8s/`, `/status`:
  - `GET /api/v1/services` — runs the detectors, returns JSON. First endpoint of
    a general agent-management surface (host facts, future config can join).
  - `/svc/` — [svc.go](../pkg/agent/tunnel/svc.go): reverse-proxies to the
    service named by the provider-set `X-Kedge-Svc-Target` header. WebSocket
    upgrades are hijacked and piped (HA uses `/api/websocket`).

    This is the **SSRF boundary**, and it is mode-aware (`isAllowedSvcHost`).
    Server mode permits loopback only. Kubernetes mode additionally permits
    cluster-DNS Service names (`{name}.{namespace}.svc[.cluster.local]`), which
    only resolve inside the cluster — so node IPs, the LAN and the internet stay
    out of reach either way. IPs are parsed rather than string-matched
    (`127.0.0.2` is loopback, `evil.svc.example.com` is not cluster DNS), and a
    bare `.svc` with no `{name}.{namespace}` in front is rejected. See
    `TestIsAllowedSvcHost` for the pinned cases.

Both routes work in server mode and kubernetes mode.

## 5. Provider side (`providers/edges/`)

- **[internal/tunnel/service_proxy.go](../providers/edges/internal/tunnel/service_proxy.go)** —
  two new subresources, dispatched in `buildEdgesProxyHandler` **before**
  `parseEdgesProxyPath` (that parser validates against `gvrForResource`, and
  `services` is deliberately not a tunnel Kind since a Service isn't
  connectable):

  ```
  /services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/services/{name}/proxy[/...]
  /services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/services/{name}/mcp
  ```

  `proxy` authorizes via kcp SAR (same delegated call as `ssh`), loads the
  Service, resolves the LinuxServer tunnel from the `ConnManager`, reads the
  token from the tenant Secret, and forwards with `X-Kedge-Svc-Target` set and
  the caller's hub token **replaced** by the service token. Upgrades are
  hijacked and piped.
- **[internal/haclient/](../providers/edges/internal/haclient/)** — issues HTTP
  to a host service through the agent's `/svc` handler over the tunnel. Used by
  both the MCP tools and the validation reconciler, so loopback enforcement has
  exactly one choke point.
- **[internal/servicectrl/](../providers/edges/internal/servicectrl/)** — two
  multicluster reconcilers, wired in `controller_manager.go` with the shared
  `ConnManager`:
  - `discovery_reconciler.go` (For `LinuxServer`): on connected edges, pulls
    `/api/v1/services` and upserts Services. 5-minute resync.
  - `validation_reconciler.go` (For `Service`): when `authSecretRef` is set,
    calls HA `GET /api/config`, fills exact `version`, flips
    `CredentialsValid`/`Ready`, and stamps `status.URL`. 10-minute resync;
    30s retry while the edge is disconnected.

## 6. MCP

- **[internal/tunnel/mcp_service.go](../providers/edges/internal/tunnel/mcp_service.go)** —
  the per-Service `mcp` subresource. Tool bundle keyed by `spec.type`;
  `home-assistant` gets:

  | Tool | HA API | Notes |
  |---|---|---|
  | `ha_states` | `GET /api/states` | optional `domain` filter, `limit` (default 100), **trimmed** to entity_id/state/friendly_name |
  | `ha_get_state` | `GET /api/states/{entity_id}` | full attributes for one entity |
  | `ha_call_service` | `POST /api/services/{domain}/{service}` | the actuator — `cover.open_cover` for "open the gates" |

  `generic` serves no tools (proxy-only).
- **[internal/tunnel/mcp_root.go](../providers/edges/internal/tunnel/mcp_root.go)** —
  the provider root `/mcp` (what the hub aggregate federates) is now a merge:
  HA tools from every Ready `home-assistant` Service in the caller's tenant
  (prefixed `<service>_`), plus the existing kube toolset federated **in-process**
  over JSON-RPC via `httptest`. The merge exists because the two toolsets use
  different SDKs — kube tools come from `containers/kubernetes-mcp-server`, HA
  tools from `modelcontextprotocol/go-sdk` (promoted to a direct dep). Federation
  only needs `tools/list` + `tools/call`.

Net effect for an AI agent with the `edges` family granted: it sees
`edges__<service>_ha_call_service` and `edges__<kube tools>` with no
agents-provider change.

## 7. Portal

`providers/edges/portal/` (Vue). Both edge detail views gained a **Services**
section: cards per Service (type, version, target, install type, phase), plus a
Connect/Update-token flow that writes the Secret `kedge-edges-svc-<name>` in
`kedge-system` and patches `spec.authSecretRef`. The validation reconciler does
the rest.

On kube edges the section also gets **Add service** (name, type, target
namespace, target service, port) and a delete action, since kube services are
declared rather than discovered. The created object carries the edge label so it
lists alongside discovered ones but **not** the discovered label, so the
discovery reconciler leaves it alone.

GraphQL wire identifiers follow the gateway convention for kind `Service`:
list `Services`, input `EdgesKedgeFarosShV1alpha1Service_Input`, mutation
`updateService`.

## 8. Using it

**HA on a bare host (LinuxServer):**

1. Install the CLI, create the LinuxServer, then
   `kedge install --type server --name ha-box ...` (systemd unit joins the tunnel).
2. Within a reconcile, `kubectl get edgesvc` shows `ha-box-home-assistant`
   phase `Detected`.

**HA in a cluster (KubernetesCluster):** declare it — portal → Add service, or:

```yaml
apiVersion: edges.kedge.faros.sh/v1alpha1
kind: Service
metadata:
  name: ha-cluster
  labels:
    edges.kedge.faros.sh/edge: kube-1
spec:
  edgeRef: {kind: KubernetesCluster, name: kube-1}
  targetRef: {namespace: home, name: home-assistant}   # → home-assistant.home.svc
  type: home-assistant
  port: 8123
```

**Then, either way:**

3. Attach a token (portal → Connect, or by hand): create a Secret with key
   `token` holding an HA long-lived access token (HA → profile → Security →
   Long-lived access tokens) and set `spec.authSecretRef`. Phase → `Ready`.
4. Verify the data plane:
   ```
   curl -H "Authorization: Bearer $USER_TOKEN" \
     https://<hub>/services/providers/edges/edgeproxy/clusters/<cluster>/apis/edges.kedge.faros.sh/v1alpha1/services/ha-box-home-assistant/proxy/api/config
   ```
5. Create an Agent with `spec.tools.interactive.families: [core, edges]` and
   `requireApproval: ["*ha_call_service*"]`. Chat: "open the gates" → the model
   calls `ha_states domain=cover`, finds `cover.gate`, calls
   `ha_call_service(cover, open_cover, cover.gate)` → approval → gate opens.

**Interactive-only caveat**: the agents provider's `edges` family forwards the
caller's user token, and background/scheduled runs have none — so "close the
gates at 22:00" needs the follow-up in §9.

## 9. Not done / follow-ups

- **Live end-to-end demo (phase 5)** — the code path is complete but has not
  been exercised against a real HA host.
- **Background & scheduled HA runs** — agents-provider workstream: mint a scoped
  per-tenant token (or teach an `edges` Connection to carry one) for background
  MCP dials.
- **HA event → agents Trigger** ("doorbell rang, ask the agent…"). The agents
  webhook plumbing exists; needs an HA-side automation template.
- **Portal re-scan button** and generic port-scan discovery beyond named
  detectors (server edges).
- **Kube service discovery** — optional convenience: scan a cluster's Services
  for the HA image/port instead of declaring. Deliberately not done (see §3).
- **Native HA MCP passthrough** — a later `spec.type` reusing the same `mcp`
  subresource to proxy HA's own MCP server integration instead of our
  REST-backed tools. (Rejected for now: it needs a user-enabled HA integration,
  exposes the Assist-shaped surface rather than raw service calls, and adds an
  HA-version dependency.)

## 10. Gotchas worth knowing

- **Single-replica invariant** — everything here rides the in-memory
  `ConnManager` (`providers/edges/main.go` documents it). Do not add replicas.
- **Never run the aggregate `make crds`** — use `make codegen-edges-provider`.
  The aggregate target currently guts the core.faros.sh export and hangs the hub
  bootstrap. The edges target regenerates CRDs, kcp APIResourceSchemas, **and**
  copies them into `deploy/chart/files/schemas/` (that copy is mandatory — the
  chart is what installs them).
- **Actuation safety** — `ha_call_service` can do anything the HA token can,
  including unlocking doors. Use per-agent `requireApproval`, and prefer a
  scoped HA user for the token. The agents provider already audit-logs every
  tool call.
- **SSRF posture** — the `/svc` proxy is agent-side host-restricted (loopback,
  plus cluster DNS in kubernetes mode) and Service-allowlisted provider-side. It
  must never become "reach any host on the LAN". If you touch
  `isAllowedSvcHost`, `TestIsAllowedSvcHost` is the contract.
- **A kube Service with no `targetRef` must never fall back to loopback** — that
  would silently proxy to the agent pod itself. The CRD's CEL rule rejects it at
  admission, and the proxy, the MCP tool registration and the validation
  reconciler each re-check independently (an object written before the rule
  existed would otherwise slip through).
- **Entity volume** — `ha_states` is trimmed and capped server-side; a real HA
  install has thousands of entities and raw payloads would blow the model
  context.
- **Unstructured projection trap** — `serviceView` (and `sshEdgeView`) are
  decoded with `runtime.DefaultUnstructuredConverter.FromUnstructured`, which
  reflects over every field and panics on unexported ones. All fields must be
  exported, with non-object-sourced fields tagged `json:"-"`.
- **MCP DNS-rebinding guard** — every MCP handler behind the hub proxy sets
  `r.Host = "localhost"` before serving, or the SDK 403s with "invalid Host
  header".
- **Two MCP SDKs in one binary** — don't try to unify them; merge at JSON-RPC
  (§6).
- **`apiurl` naming** — the root-module helpers keep the `EdgeServiceProxyPath`
  /`EdgeServiceProxyURL` names even though the resource is `services`: in that
  shared package a bare `ServiceProxyPath` would read as a core-Kubernetes
  Service. The emitted path is `/services/`.

## Key files

| Area | Path |
|---|---|
| API type | `providers/edges/apis/v1alpha1/types_service.go` |
| Registration | `apis/v1alpha1/{groupversion_info,connectable}.go` |
| Agent detectors | `pkg/agent/discovery/{discovery,homeassistant}.go` |
| Agent routes | `pkg/agent/tunnel/{server,svc}.go` |
| Proxy subresources | `providers/edges/internal/tunnel/service_proxy.go` |
| HA client | `providers/edges/internal/haclient/haclient.go` |
| Reconcilers | `providers/edges/internal/servicectrl/` |
| Edge-kind target resolution | `providers/edges/internal/servicectrl/target.go` |
| Per-service MCP | `providers/edges/internal/tunnel/mcp_service.go` |
| Root MCP merge | `providers/edges/internal/tunnel/mcp_root.go` |
| Portal | `providers/edges/portal/src/{api.ts,types.ts,Detail.vue}` |
| Codegen | `Makefile` target `codegen-edges-provider` |
