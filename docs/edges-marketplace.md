# Edges mini-marketplace — implementation plan (handover)

> **Status (2026-07-18): v1 implemented.** Placement carries a provider-rendered
> manifest bundle (`spec.manifests`); the agent applies it generically with SSA
> + prune. Workloads gained `spec.helm` rendered hub-side via the Helm SDK
> (`internal/render`). Edges are stamped with a self-name label so a deploy
> targets one edge. Portal Workloads page has a Marketplace card grid that
> deploys a Helm workload + auto-wires an edges Service. **Not yet live-tested
> on a cluster** — chart versions in `portal/src/marketplace.ts` and the
> gabe565 *arr values shapes need verifying against a real kind edge; see
> "Order & verification".** Remaining/known gaps kept below.

Goal: a **mini marketplace inside the Workloads page** of the edges provider
portal. One click deploys a catalog app (qBittorrent, Pi-hole, Grafana, …) as a
`Workload` onto a KubernetesCluster edge — Kubernetes style — and wires it up as
an edges `Service` so its MCP tools light up once the operator pastes a token.

This closes the loop we already built: the MCP **service catalog**
(`providers/edges/internal/tunnel/svc_catalog.go`, portal `PRESETS` in
`providers/edges/portal/src/Services.vue`) knows how to *talk to* these apps;
the marketplace makes faros able to *run* them too.

## Where things stand (read this first)

All paths relative to repo root; the edges provider is a separate Go module at
`providers/edges` (module `github.com/faroshq/provider-edges`).

### Deploy path that already works end-to-end

1. **`Workload` CR** — `providers/edges/apis/v1alpha1/types_workload.go`.
   Namespaced (portal uses ns `default`), group `edges.faros.sh`.
   `spec.simple` = `{image, ports, env, resources, command, args}` (there is
   also `spec.template` for a full PodTemplateSpec), plus `replicas`,
   `placement` (`edgeSelector` label selector + `strategy` Spread|Singleton)
   and `access` (`expose`, `dnsName`, `port` — currently mostly unused).
2. **Scheduler** — `providers/edges/internal/scheduler/` fans a Workload out
   into one `Placement` per matching KubernetesCluster edge.
3. **Agent** — `pkg/agent/reconciler/workload.go` (main faros module) watches
   Placements through the hub and materializes each as a local `Deployment` in
   ns `default` (`convertToDeployment`, `buildPodSpecFromSimple`). It does
   **not** create a k8s `Service` today. Agent RBAC is already `*` on core/apps
   (`deploy/charts/faros-agent/templates/rbac.yaml`) — no chart change needed
   to start creating Services.
4. **Portal** — `providers/edges/portal/src/Workloads.vue` (list + create via
   `Wizard.vue`-style inline form), `api.ts` `createWorkload(WorkloadDraft)`
   GraphQL mutation (`WORKLOAD_NS = 'default'`).

### Service/MCP path that already works end-to-end

- **edges `Service` CR** — `providers/edges/apis/v1alpha1/types_service.go`.
  `spec.type` enum now: `home-assistant, qbittorrent, prowlarr, sonarr, radarr,
  grafana, grafana-loki, prometheus, jellyfin, plex, portainer, adguard,
  proxmox, pihole, generic`. For a KubernetesCluster edge it needs
  `spec.targetRef {namespace, name}` — the agent dials `{name}.{namespace}.svc`
  over cluster DNS — plus `spec.port`, `spec.edgeRef`, optional
  `spec.instructions` (AI guidance) and `spec.authSecretRef` (set via the
  portal "connect" flow which stores the token as a Secret).
- Once a Service is **Ready + tokened**, `listReadyServices` +
  `registerCatalogTools` (`internal/tunnel/mcp_root.go`, `svc_catalog.go`)
  expose `<service>_*` MCP tools on the tenant MCP endpoint automatically.
- Portal `Services.vue` has the categorized preset dropdown (`PRESETS`,
  `PRESET_GROUPS`) with default port + token hint per type — **reuse this
  data** for the marketplace cards.

## Decision: Helm is a PREREQUISITE, not a follow-up

Simple mode (image+port+env) cannot deploy most of the catalog for real:

- **Prometheus / Loki / AdGuard** need a config file (ConfigMap + mount) —
  simple mode has neither → useless without.
- **Pi-hole / AdGuard as actual DNS** need port 53 exposure (NodePort /
  hostNetwork) — inexpressible in simple mode; web-UI-only is a toy.
- ***arr / qBittorrent** are pointless without persistent config (indexers,
  library state), and media apps want hostPath/NFS mounts to real libraries.
- **Portainer** (kube mode) needs its own SA + ClusterRole.

Only Grafana and a demo-grade qBittorrent survive on simple mode. So the
marketplace deploys via **Helm-backed Workloads**, and the phases below build
that first. Upstream charts (grafana, pihole, adguard, qbittorrent, the *arr
family via TrueCharts/community, portainer, prometheus, loki) already solve
persistence, config, Services and RBAC — do not grow `SimpleWorkloadSpec`
into a chart substitute.

Architecture rule (agreed): **render at the provider, never on the agent.**
The provider runs `helm template` (charts fetched/cached hub-side — edges may
have no registry egress); the `Placement` carries the rendered **manifest
bundle**; the agent's only new primitive is "apply/prune a labeled manifest
set with server-side apply". Release state stays in kcp, not on edges.

## What to build (in order)

### Phase 1 — agent: generic manifest-bundle apply/prune

`pkg/agent/reconciler/workload.go` (main faros module, ships in the
faros-agent image — rebuild/rollout needed; Tiltfile.cluster covers dev kind):

- Placement gains a rendered-manifests payload (list of objects or one
  multi-doc YAML string — pick with an eye on etcd object size; prune
  whitespace/comments from rendered output).
- Agent applies the set with SSA (field manager `faros-agent`), labels every
  object `edges.faros.sh/workload=<name>`, prunes labeled objects that
  vanished from the bundle, and deletes the set on Placement deletion.
  Agent RBAC is already `*` on core/apps/rbac/networking — no chart change.
- Migrate simple mode to the same path: the **scheduler/provider** renders
  `spec.simple` into Deployment (+ ClusterIP Service when ports are set)
  manifests at Placement-creation time. One agent code path for everything;
  `convertToDeployment` moves provider-side and the k8s Service for simple
  workloads falls out for free.

### Phase 2 — provider: `spec.helm` Workload mode

- `WorkloadSpec` gains `helm {repoURL, chart, version, values}` (mutually
  exclusive with `simple`/`template`); `make codegen-edges-provider`.
- Provider-side renderer (edges provider module): fetch + cache the chart,
  `helm template <name>` with `fullnameOverride=<workload name>` forced into
  values — this pins the chart's Service name to the workload name, which the
  marketplace needs for deterministic `targetRef` wiring.
- Rendering rules: `--include-crds` off by default (skip charts needing CRDs
  in v1), drop `helm.sh/hook` resources, namespace everything into `default`.
- Values hygiene: no secrets in `values` (rendered bundles are stored in kcp);
  apps that mint admin passwords should do it chart-side and surface where to
  find it in the card copy.
- Persistence comes from the charts (PVCs) — requires a **default
  StorageClass on the edge cluster** (kind's local-path in dev). Per-app
  preset values must set persistence sizes; card copy notes the requirement.

### Phase 3 — marketplace UI in Workloads page

`Workloads.vue`: a "Marketplace" card-grid section *inside* the page (not a
new tab). Cards join a new `providers/edges/portal/src/marketplace.ts` table
with the existing `PRESETS` (category/label/port/tokenHint — join on `type`,
don't duplicate):

```ts
interface MarketplaceApp {
  type: string                    // edges Service spec.type — join key
  chart: { repoURL: string; name: string; version: string }
  values?: Record<string, unknown> // minimal preset values (persistence size, TZ…)
  description: string
  needsToken: 'api-key' | 'user-pass' | 'password' | 'optional'
}
```

Deploy form: name (prefilled), target edge (KubernetesCluster dropdown →
Singleton strategy + selector on that edge's labels — check what labels edges
carry), optional tiny values overrides (TZ, storage size). On submit, two
creates (both APIs exist in `api.ts`):

1. `createWorkload` — extended for the `helm` mode fields.
2. `createKubeEdgeService` — type, edgeName, targetRef
   `{namespace:'default', name:<workload name>}` (guaranteed by
   fullnameOverride), port from the preset, preset instructions if any.

Then surface "next: paste the API key" using the preset `tokenHint` — tokens
are minted in each app's own UI and can't be auto-provisioned;
Prometheus/Loki (`tokenOptional`) are Ready without one. Plex and
proxmox/home-assistant cards render as **connect-only** (create the Service
against an existing install — `Services.vue` flow) since they aren't sensibly
chart-deployed on an edge.

### Phase 4 — seed the app table

Charts to start with (verify current repo URLs/versions at build time):
grafana + loki + prometheus (grafana.github.io / prometheus-community),
pihole (mojo2600), adguard (community), portainer (portainer.github.io),
qbittorrent + *arr + jellyfin (TrueCharts or gabe565/utkuozdemir community
charts — pick one family for consistent values shape). Start with 3 that
exercise all auth styles — **grafana** (Bearer), **qbittorrent** (cookie
login), **pihole** (session) — then fan out.

## Order & verification

1. **Phase 1** (agent bundle applier + provider-side simple rendering) stands
   alone: verify with the existing dev kind edge (Tiltfile.cluster runs an
   in-cluster kube edge agent) that a simple Workload with ports still
   materializes — now as Deployment **and** ClusterIP Service
   (`kubectl get deploy,svc -n default` on the edge cluster), and that
   deleting the Workload prunes both.
2. **Phase 2**: create a helm Workload by hand (kubectl/GraphQL) — e.g.
   grafana — confirm the Placement carries the rendered bundle, the chart's
   objects (incl. PVC) appear on the edge, and the Service name equals the
   workload name (fullnameOverride).
3. **Phase 3+4** end-to-end in local kind: marketplace card → deploy
   qbittorrent → Workload Running → edges Service exists with targetRef →
   paste `user:pass` token → `qb_torrents` MCP tool answers on the tenant MCP
   endpoint (hub aggregate → edges provider federation is already
   live-tested). Repeat for pihole (session auth) and grafana (Bearer).
4. `make codegen-edges-provider` after any `apis/v1alpha1` change. Do NOT run
   root `make crds` (see memory: it guts core.faros.sh).
5. Build gates: `cd providers/edges && go build ./... && go vet ./internal/...`;
   portal `npx vue-tsc --noEmit && npm run build`; main module `go build
   ./pkg/agent/...` for Phase 1. Agent changes need the faros-agent image
   rebuilt/rolled on the dev edge cluster.

## Gotchas / context for the next model

- Shell emits `setValueForKeyFakeAssocArray … _encode` noise on every command —
  pipe through `grep -vE '_encode|_decode'`.
- Edges provider portal talks GraphQL (`graphql()` helper in `api.ts`) to the
  hub gateway at `/graphql/{cluster}`; mutations need explicit `namespace`.
- The catalog auth kinds (Basic, PVEAPIToken, Pi-hole session) are coded to
  documented APIs but **not live-tested** — marketplace testing will exercise
  qbittorrent/pihole/grafana for real; fix `svc_catalog.go` if the wire format
  disagrees.
- Current branch `fix/edges-provider-image-build`, pushed through commit
  `4156988` (categorized dropdown). PR #437 covers the earlier
  instructions/Services-tab work.
- Commit style: `feat(edges): …` + `Co-Authored-By: Claude Opus 4.8 (1M
  context) <noreply@anthropic.com>` (update model name as appropriate).
