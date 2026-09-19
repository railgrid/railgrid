# Cross-provider access — unification architecture

**Status:** Largely implemented — see "Status after remediation" below. The
August 2026 audit body is retained as history.
**Owner:** TBD
**Last updated:** 2026-09-19 (status section); audit body 2026-08-08
**Reads as a delta on:** [providers.md](./providers.md),
[provider-connectivity-contract.md](./provider-connectivity-contract.md),
[provider-actions.md](./provider-actions.md)

---

## Status after remediation (2026-09-19)

> **Read this section first; everything below it is history.** The body of this
> doc is the August 2026 audit, kept verbatim as the record of *why* this work
> happened. Most of what it describes as present-tense is no longer true on
> branch `provider-contract/phase-0`. Where the two disagree, this section
> wins. The per-section landing status is in
> [roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md).

### The eight mechanisms (Part 1)

| # | Mechanism | State | Anchor |
|---|---|---|---|
| M1 | Bound CRs via APIBinding | **unchanged** — still the healthy core | — |
| M2 | Provider SA + endpoint slice + claims | **unchanged in mechanics, single-sourced in declaration.** A claim is written once, in `manifest.yaml`; `provider-sdk/cmd/apiexportgen` stamps the APIExport; `init` applies schemas + export from `RAILGRID_KCP_DIR` and adds only `identityHash` at runtime | `hack/verify-provider-contract.mjs` (`claims-parity`, `export-copy`), `providers/*/init_cmd.go` |
| M3 | Blanket `secrets` claims as a credential side-door | **narrowed, not closed.** The cross-provider *uses* are gone (see finding 1 below), but six providers still claim `secrets`; only infrastructure's is read-only | `providers/{agents,app-studio,code,edges,kuery}/manifest.yaml`; `providers/infrastructure/manifest.yaml` (`[get, list, watch]`) |
| M4 | Hub backend-proxy data-plane paths, in four dialects | **collapsed to one.** `provider-sdk/dataplane` owns the grammar, the two gates and the limits; `provider-sdk/serve` owns the server layout and refuses anything outside it. Every in-tree provider serves through `serve.New`. `/edgeproxy/…/apis/…`, `/s2s/*` and the ad-hoc `/api/*` surfaces are gone, not aliased | `provider-sdk/dataplane/{path,gate,serve}.go`, `provider-sdk/serve/serve.go`, `providers/*/main.go` |
| M5 | MCP as a third access path | **demoted to a projection.** The aggregate verifies the bearer and its right to the addressed cluster *before* fan-out, and enumerates for that verified caller; per-edge MCP is now an ordinary data-plane verb, not its own mount | `pkg/hub/mcpaggregate/verifier.go`, `pkg/hub/mcpaggregate/enumerator.go`, `providers/edges/internal/tunnel/grammar.go` (`VerbMCP`) |
| M6 | Provider Actions as a hub router | **deleted.** `pkg/hub/provideractions` no longer exists; an action is a verb under `dataplane.ActionsRoot` on the ordinary backend proxy, authorized by the same two gates. `spec.virtualWorkspace` is retired from the CatalogEntry type | `pkg/hub/server.go:431-434`, `provider-sdk/dataplane/path.go` (`ActionsRoot`) |
| M7 | Providers minting SAs/ClusterRoles over other groups | **closed for agents and edges, open for kuery and app-studio.** The hub service exists and refuses anything outside four clauses; agents and edges consume it and hold no RBAC-authoring claims. kuery and app-studio still provision their own identities through `provider-sdk/tenantaccess` and still claim `serviceaccounts`/`clusterroles`/`clusterrolebindings` | `pkg/hub/identity/policy.go` (clauses A–D), `provider-sdk/identityclient`, `providers/agents/manifest.yaml:74`, `providers/edges/manifest.yaml:57`; open: `providers/kuery/manifest.yaml`, `providers/app-studio/manifest.yaml` |
| M8 | String-literal runtime conventions | **closed for the two this doc named.** `Instance.spec.imagePullSecretRef` and `spec.oidcBridgeSecretRef` are typed, validated inputs; `<instance>-registry` survives only as the name of the Secret the infrastructure provider writes *inside its own runtime*, which is nobody else's contract. Consumers render foreign paths with `dataplane.ProviderPath` | `providers/infrastructure/controller/instance/bridge.go:66-67,87,94`, `provider-sdk/dataplane/build.go` |

### The findings (Part 2)

**2.1 Contract-3 violations**

1. **Foreign credential via the Secrets side-door — closed.** App Studio no
   longer reads code's `Connection` Secret. It invokes code's
   `mint_registry_token` action **as the caller**, then writes its own
   `dockerconfigjson` Secret and names it on the Instance's
   `spec.imagePullSecretRef`
   (`providers/app-studio/api/project_promote.go:56-61,66-68,87-103,172-185`).
2. **kuery's edge sync dead three ways — closed.** It claims
   `edges.railgrid.ai/kubernetesclusters`, and the per-edge coordinate comes
   from the edge's own `status.url`; an edge that has not published one is an
   error, not a guessed format string
   (`providers/kuery/engagement/controller.go:669-695`, `edgeProxyURL`). The
   `/services/edges-proxy/…` dial is gone.
3. **`<instance>-registry` naming convention — closed.** See M8.
4. **Providers authoring RBAC over each other's API groups — closed for
   agents.** Per-agent identities are hub-minted, TTL'd and scoped
   (`providers/agents/api/agentidentity.go:60,146`); the policy refuses any
   rule outside clauses A–D, and in particular any write on a foreign group
   and anything at all on the core group (`pkg/hub/identity/policy.go`).
   Still open for kuery and app-studio (M7).
5. **The Enable-time edges-proxy grant — narrowed, not removed.** It now
   writes `access` on `/` plus `create` on the per-verb subresources
   (`kubernetesclusters/k8s`, `linuxservers/ssh`, `services/proxy`,
   `services/mcp`, `services/ticket`, …). The wildcard `proxy` verb and the
   non-VW Secrets/Namespaces read-write are gone
   (`pkg/hub/kcp/bootstrap.go:2379-2433`).
   `CatalogEntry.spec.edgeProxyAccess` still exists
   (`apis/providers/v1alpha1/types_catalogentry.go:125-134`) but no in-tree
   provider sets it any more.
6. **Foreign path grammar hardcoded in consumers — closed.** Both named
   consumers render the path from the shared grammar and the provider name
   their own binding gives them: `providers/agents/tools/tools.go:157` and
   `providers/app-studio/api/dataplane_client.go:84`, both through
   `dataplane.ProviderPath`.

**2.2 Fail-open hub surfaces**

| Surface | Now | Anchor |
|---|---|---|
| `POST /api/providers/{name}/heartbeat` | Authenticated as the provider's own SA, verified by TokenReview in the provider's workspace. **Not yet fail-closed by default**: `--provider-heartbeat-auth` ships as `warn` (log and accept) this release, `enforce` next | `pkg/hub/providers/heartbeat_auth.go`, `pkg/hub/options.go:205` |
| `GET /api/providers` | Authenticated upstream of the handler; catalog visibility scoped to the verified Org | `pkg/hub/providers/api.go:29-34`, `pkg/hub/server.go:452,727` |
| MCP aggregate | Bearer verified against the addressed cluster before any fan-out; enumeration runs for the verified caller | `pkg/hub/mcpaggregate/verifier.go`, `enumerator.go` |
| UI proxy / backend proxy | Inbound `X-Railgrid-User`/`-Tenant`/`-Cluster` are **always** stripped on both. A resolver failure still forwards without identity headers, which is safe only because no provider authorizes on a header — every one of them gates on the bearer | `pkg/hub/providers/proxy.go:115-126,144` |
| `/metrics` | Off the public listener; no `/metrics` route remains on the hub router | `pkg/hub/server.go` |

**2.3 Two grant systems — closed.** There is one enforcement plane: kcp RBAC
plus the two gates, run by `provider-sdk/dataplane.Gate` as the caller, with
`create` on `{resource}/{verb}` as the only grant shape. The hub Go authorizer
is deleted with the router it served.

---

## Restore-from-reboot summary *(historical — August 2026)*

> Enough context for a fresh session (or a returning human) to pick this up
> without replaying the audit. **Superseded by "Status after remediation"
> above**; kept for the reasoning, not for the present tense.

**Where we are:** a full audit of every cross-provider access channel in the
tree (branch `provider-actions`, PR #499 included) is complete. Roughly **40
distinct channels** exist, reducible to **eight mechanisms**, **three separate
identity-minting implementations**, and **two competing grant systems**. This
doc records the inventory, the violations found, and the target architecture
that collapses all of it into **three primitives + one identity service**.

**Goal in one sentence:** a provider integrates with the platform through
exactly three things — its **bound APIs**, its **declared verbs**, and **MCP
projections of both** — with one grant vocabulary (kcp RBAC) and one identity
minter.

**Key facts discovered by the audit (don't re-derive):**

- railgrid's "VW subresources" are **not** kcp virtual workspaces. The
  infrastructure data plane is a plain HTTP handler behind the hub backend
  proxy, addressed `/services/providers/infrastructure/dataplane/clusters/{clusterID}/{resource}/{name}/{verb}`,
  authorized by re-reading the resource **as the caller** so kcp RBAC decides
  ([dataplane/handler.go](../providers/infrastructure/dataplane/handler.go)).
- The exec verb adds an explicit `SelfSubjectAccessReview` for `create` on the
  virtual subresource `<resource>/exec`
  ([dataplane/authorizer.go](../providers/infrastructure/dataplane/authorizer.go)) —
  i.e. **kcp RBAC on virtual subresources is the platform's existing verb-grant
  mechanism**. Provider Actions (PR #499) re-implemented this in Go at the hub.
- `CatalogEntry.spec.virtualWorkspace.url` is not a VW: providers set it to the
  same address as `spec.backend.url`; the hub uses it only as a raw dial target
  for `/actions/*` and `/workload-identities/review`. Decision #6 in
  providers.md (`/vw/*` proxying) was never implemented and the field docs are
  stale.
- Six providers hold blanket `secrets` permission claims; this is the
  side-door behind every implicit cross-provider credential hand-off.

**Next concrete step:** phase 0 (close fail-open hub surfaces). **Phase 1 is
implemented on this branch** — Provider Actions now ride the data-plane
grammar through the backend proxy, grants are kcp RBAC rules on action
subresources, the hub action router/authorizer and `virtualWorkspace.url`
dial targets are deleted, `/workload-identities/*` is a hub-only reserved
prefix, and App Studio re-verifies grant digests against the live catalog at
invoke time (the drift 409 moved from hub to consumer). See §"Migration".

---

## Why this doc exists

The provider isolation principle
([providers.md §Provider isolation](./providers.md)) and the connectivity
contract ([provider-connectivity-contract.md](./provider-connectivity-contract.md))
define three contracts. In practice the tree has grown far past them: the
audit found eight distinct mechanism families answering the same two
questions — *"how does A reach B's data"* and *"who may run this verb on this
resource"*. Each new feature (dataplane, edges tunnel, MCP federation,
provider actions, agent identities) added its own transport, its own
authorization code, and in several cases its own identity minter.

The result is not just conceptual load. The audit found real defects that
exist *because* of the duplication: fail-open hub surfaces, foreign-credential
reads, dead channels pointing at removed mounts, and string-literal contracts
between providers.

---

## Part 1 — Inventory: the eight mechanisms *(historical)*

Compressed here; the full per-channel tables with anchors are in the Appendix.

| # | Mechanism | Used by | Identity | Who authorizes |
|---|---|---|---|---|
| M1 | Bound CRs via APIBinding over `/clusters/…` (hub kcp proxy, kube REST) | everyone | caller bearer | kcp RBAC |
| M2 | Provider SA + APIExportEndpointSlice VW + permission claims | every provider's controllers | provider SA | claims accepted at Enable |
| M3 | Blanket `secrets` claims used as a credential side-door | app-studio, code, databricks, edges, agents, infrastructure | provider SA | claims only — no owning-provider consent |
| M4 | Hub backend-proxy data-plane paths (`dataplane/`, `edgeproxy/`, `agent/`, `s2s/`, kuery REST) | app-studio, agents, kuery, portal | caller bearer | provider-side caller-scoped GET + SSAR |
| M5 | MCP (hub aggregate + per-provider `/mcp` + controller discovery) | assistants, agents | caller bearer (unverified at the hub) | each provider independently |
| M6 | Provider Actions (PR #499: hub router → `virtualWorkspace.url`, Project grants, workload identity) | app-studio ↔ databricks | caller bearer or workload SA | hub Go authorizer (workload SAs only) |
| M7 | Cross-provider RBAC authoring (providers minting SAs/ClusterRoles over other providers' groups) | agents, edges, Enable-time edges-proxy grant | minted SAs (mostly non-expiring) | whoever holds `clusterroles` claims |
| M8 | String-literal runtime conventions (`<instance>-registry` pull secret, OIDC bridge, shared Gateway) | app-studio, infrastructure | provider runtime credential | none — the contract is a name |

M1 and M2 are the healthy core. M4 is sound in shape but has three
incompatible dialects. M3, M6, M7, M8 are the mechanisms this doc removes or
absorbs. M5 is kept but demoted to a projection.

---

## Part 2 — Findings *(historical — see "Status after remediation" for what is closed)*

### 2.1 Contract-3 violations, ranked

1. **Foreign credential via the Secrets side-door.** App Studio
   ([api/project_promote.go:47](../providers/app-studio/api/project_promote.go))
   reads the code provider's git PAT out of its `Connection` Secret and re-mints
   it as a registry `dockerconfigjson`. Contract 3 says the owning provider is
   the single holder of its backend credential; code publishes no such API and
   is never consulted. Works only because of M3.
2. **kuery's edge sync is dead three ways.** It claims `railgrid.ai/edges`
   (the core export no longer exports Edge), dials `/services/edges-proxy/…`
   (unmounted since edges was extracted to a standalone provider —
   [server.go:325](../pkg/hub/server.go)), and composes the path by hand
   instead of reading the `status.URL` the edges provider stamps
   ([engagement/controller.go:363](../providers/kuery/engagement/controller.go),
   [edges/internal/tunnel/edge_status.go:107](../providers/edges/internal/tunnel/edge_status.go)).
3. **`<instance>-registry` naming convention** couples App Studio and
   infrastructure through a duplicated string literal
   ([project_promote.go:57](../providers/app-studio/api/project_promote.go) vs
   [bridge.go:56](../providers/infrastructure/controller/instance/bridge.go)).
   No schema, no validation, no versioning.
4. **Providers authoring RBAC over each other's API groups.** Agents'
   per-agent SAs get read on the whole `infrastructure.railgrid.ai`
   group with non-expiring tokens
   ([agentidentity.go:61](../providers/agents/api/agentidentity.go)).
5. **The Enable-time edges-proxy grant** gives a provider SA direct, non-VW
   Secrets/Namespaces read-write in tenant workspaces
   ([bootstrap.go:1731](../pkg/hub/kcp/bootstrap.go)) — consented, but the one
   standing grant that bypasses the VW envelope.
6. **Foreign path grammar hardcoded in consumers.** `"infrastructure"` and the
   whole dataplane URL shape are string-built in app-studio
   ([dataplane_client.go:36](../providers/app-studio/api/dataplane_client.go)),
   and agents ([tools/tools.go:143](../providers/agents/tools/tools.go)).
   Sanctioned shape, but routed by string, not by binding or status.

### 2.2 Fail-open hub surfaces

| Surface | Posture | Anchor |
|---|---|---|
| `POST /api/providers/{name}/heartbeat` | **unauthenticated and state-changing** — anyone can keep a dead provider Ready, or set `HeartbeatRequired` on a non-beating provider and force it not-Ready within 90s | [heartbeat.go:41](../pkg/hub/providers/heartbeat.go) |
| `GET /api/providers` | **unauthenticated**; now returns full action schemas, consent prompts, and up to 512 KiB of inline skill content per provider; both new docs wrongly say "authenticated" | [api.go:164](../pkg/hub/providers/api.go), [server.go:355](../pkg/hub/server.go) |
| MCP aggregate | bearer required but **never verified**; cluster ID is caller-asserted from the URL and injected as `X-Railgrid-Tenant`/`X-Railgrid-Cluster` | [mcpaggregate/handler.go:73](../pkg/hub/mcpaggregate/handler.go), [federation.go:242](../pkg/hub/mcpaggregate/federation.go) |
| UI proxy | does **not** strip inbound `X-Railgrid-*` identity headers (the backend proxy does) | [proxy.go:53](../pkg/hub/providers/proxy.go) |
| Backend proxy identity | fail-open: resolver failure forwards the request without identity headers | [proxy.go:120](../pkg/hub/providers/proxy.go) |
| ~~GraphQL gateway~~ | ~~accepted the token from the `?token=` query parameter (log/referrer leak path)~~ — **resolved by removal**: the embedded GraphQL gateway no longer exists | — |
| `/metrics` (new in PR #499) | unauthenticated on the public listener, serving the entire `legacyregistry` | [server.go:206](../pkg/hub/server.go) |

### 2.3 Two grant systems for one question

"May this identity run this verb on this resource" is answered by:

- **kcp RBAC + SSAR on a virtual subresource** — the dataplane exec pattern
  ([dataplane/authorizer.go:58](../providers/infrastructure/dataplane/authorizer.go)),
  uniform for humans and SAs, auditable with `kubectl get clusterrole`.
- **A hub Go authorizer reading Project bindings** — Provider Actions
  ([provideractions/authorizer.go](../pkg/hub/provideractions/authorizer.go)),
  enforced *only* for workload SAs; human bearers skip it entirely.

Same question, two enforcement planes, different coverage. This is the single
largest source of conceptual confusion the audit found.

---

## Part 3 — Target architecture *(historical; this is what was built)*

### The three primitives

```
┌──────────────────────────────────────────────────────────────────┐
│ P1  CONTROL PLANE — bound CRs                                    │
│     APIBinding + caller-scoped access; provider SA via claims    │
│     only for the provider's own reconciliation. Credential       │
│     hand-offs are published APIs, never Secret reads.            │
├──────────────────────────────────────────────────────────────────┤
│ P2  DATA PLANE — one resource-addressed verb grammar             │
│     /services/providers/{name}/<root>/clusters/{clusterID}/      │
│         {resource}/{rname}/{verb}                                │
│     Declared verbs, caller identity, kcp-RBAC verb grants,       │
│     shared server-kit enforcement.                               │
├──────────────────────────────────────────────────────────────────┤
│ P3  AGENT PLANE — MCP as a projection                            │
│     Tools are thin adapters over P1/P2 executors. Discovery is   │
│     hub-authored from the validated registry. Never a third      │
│     access path.                                                 │
└──────────────────────────────────────────────────────────────────┘
          all three ride ONE identity model (below)
```

### P1 — Control plane

Unchanged in mechanics (APIBinding, the hub kcp proxy as the caller, provider
SA + endpointslice for reconciliation). Two changes in policy:

1. **Blanket `secrets` claims stop being a cross-provider API.** A provider
   may claim Secrets it *owns* (its own credential material, its own minted
   artifacts). Reading another provider's Secrets is a violation, full stop.
   Databricks' `secrets: [get]` shows claims can be narrow; the goal is that
   narrow is the norm.
2. **Credential hand-offs become published capabilities of the owning
   provider.** The registry pull-secret flow becomes a code-provider API — a
   `RegistryCredential` CR (or a declared action) that mints a scoped pull
   token on request. Consumers bind and reference it; the `<instance>-registry`
   string convention becomes a typed field on the Instance spec
   (`spec.imagePullSecretRef`), validated by infrastructure, instead of a name
   two repos keep in sync by luck.

### P2 — Data plane: the unified verb grammar

This generalizes the infrastructure dataplane pattern (the healthiest M4
dialect) and absorbs Provider Actions, the edges proxies, and agents s2s.

**Addressing.** One grammar, always cluster-ID addressed, resource in the
path, no body-carried identity:

```
POST|GET /services/providers/{name}/<root>/clusters/{clusterID}/{resource}/{rname}/{verb}[/{tail}]
         /services/providers/{name}/<root>/clusters/{clusterID}/{resource}/{rname}/components/{c}/{verb}[/{tail}]
```

`<root>` is per-provider (`dataplane`, `edgeproxy`, `actions`, `s2s`) but the
grammar after it is fixed. Transport is the ordinary backend proxy — no
dedicated hub routers, no second URL field, no reserved-path denials needed
(there is no parallel route to bypass).

**Declaration.** Verbs are declared, never implicit:

- Streaming/proxy verbs: on the resource contract, as today
  (`Template.spec.dataPlane.endpoints{}` —
  [types_template.go:459](../providers/infrastructure/apis/v1alpha1/types_template.go)).
- Typed request/response verbs ("actions"): in `CatalogEntry.spec.actions`
  exactly as PR #499 built it — schemas, canonical digest, limits, consent,
  deprecation, validated fail-closed by the hub registry
  ([apis/providers/v1alpha1/actions.go](../apis/providers/v1alpha1/actions.go)).
  These are the same concept at different `executionMode`s, and should
  converge on one declaration type over time.

**Authorization — the two-gate pattern, uniform for every caller:**

1. *Visibility gate*: the provider re-reads (or SSARs `get` on) the addressed
   resource **as the caller**, using the credential-dropping tenant client
   ([tenant/client.go:141](../providers/infrastructure/tenant/client.go)).
   kcp answers "can you see this".
2. *Verb gate*: `SelfSubjectAccessReview` as the caller for `create` on the
   **virtual subresource** `{resource}/{verb}` (name-scoped) — the exec
   pattern. kcp answers "may you do this". Version pinning stays in the
   digest/schema layer, not in RBAC.

Humans and workload SAs pass the identical gates. There is no hub-side grant
authorizer; **grants are kcp RBAC rules**:

```yaml
# "this workload may run query_table on Table trips"
- apiGroups: ["databricks.railgrid.ai"]
  resources: ["tables/query_table"]
  verbs: ["create"]
  resourceNames: ["trips"]
```

App Studio's Project integration grant flow (catalog verification, digest
pinning, consent UX, revoke audit) is unchanged — but *materializing* a grant
means writing the RBAC rule via the identity service, and revoking means
removing it.

**Enforcement quality — the server-kit.** What PR #499's hub router did well
(strict decode, schema validation, byte/item/time caps, typed sanitized error
envelope, redirect refusal) moves into one reusable Go package
(`pkg/providerkit/dataplane` or the provider-sdk): path parsing, the two
SSAR gates, declared-schema validation, ceilings, error envelope. A provider
implements one executor callback — the interface databricks already has
(`ActionExecutor.QueryTable`, shared today between its MCP tool and its
action handler). All four current M4 dialects migrate onto it.

**Routing by publication, not by string.** The URL grammar lives in exactly
one consumer-side package; providers additionally stamp their data-plane
endpoint into resource `status` (as edges does with `status.URL`) so consumers
follow the resource, not a memorized path. The hardcoded grammars in
app-studio and agents are replaced by that package.

**What P2 keeps from PR #499 unchanged:** the CatalogEntry action schema and
its fail-closed validation/compilation; the App Studio grant UX and digest
pinning; the `@railgrid/actions-node` SDK (its target URL changes shape only);
the Databricks executor and its SQL bounding.

**What P2 deletes from PR #499:** the hub invoke router and its Go authorizer
(~1,400 lines), `spec.virtualWorkspace.url` as a dial target, the `/actions`
proxy reservation, the unauthenticated hub exchange endpoint's special
routing, and the human/workload authorization asymmetry.

### P3 — Agent plane: MCP as a projection

- Every provider MCP tool wraps a P1 read or a P2 executor. No tool may hold
  a credential or path that P1/P2 would not grant the same caller.
- The hub aggregate verifies the bearer and its right to the addressed
  cluster **before** fan-out (today it forwards unverified —
  [mcpaggregate/handler.go:73](../pkg/hub/mcpaggregate/handler.go)).
- Capability discovery is **hub-authored**: the aggregate serves a
  `railgrid://providers/actions` MCP resource generated from the same validated
  registry the data plane enforces, so advertised capabilities cannot drift
  from enforceable ones, and no provider URL ever appears. Federated tools
  that back a declared action carry the action id + digest in `_meta`.

### One identity service

**Status: the hub service has landed** on branch `provider-contract/phase-0`
(§10 of [provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)).
`pkg/hub/identity` is the service, `pkg/hub/serviceaccounts/scoped_identity.go`
is the single minter, `apis/tenancy/v1alpha1/types_scoped_identity.go` is the
record, and `provider-sdk/identityclient` is the client. The hub workload
exchange is already an adapter over it and its identities are recorded and
collected for the first time. The three consumers below have **not** migrated
yet: agents (§8.6), edges (§5.7) and factory each still mint their own, and
each drops its `serviceaccounts` / `clusterroles` / `clusterrolebindings`
claims when it moves. See
[provider-connectivity-contract.md §"Scoped identities"](./provider-connectivity-contract.md)
for the API, the policy and the client.

One adjustment to the design as sketched: `list` and `watch` on **another**
provider's group are refused rather than minted. RBAC does not apply
`resourceNames` to collection requests, so "get/list/watch on named resources"
is not expressible — the rule either authorizes nothing or authorizes reading
every object of that kind in the workspace. A consumer that needs a collection
reads it by name, or the owning provider publishes a list API.

A second, smaller gap worth recording: a CatalogEntry declares **catalog
actions** but not **data-plane verbs**, so the verb-subresource clause can only
admit declared actions. `exec`, `proxy` and `delegate` exist nowhere
machine-readable. A provider needing a data-plane verb on its *own* group gets
it from clause A; a foreign one would need a `spec.dataPlane.verbs`
declaration the contract does not yet have.

Three identity minters existed before it:

| Minter | Scope | TTL | GC |
|---|---|---|---|
| Hub workload exchange ([workload_identity.go](../pkg/hub/serviceaccounts/workload_identity.go)) | GET-only, resourceNames-scoped | 10 min | ~~none~~ → owner Project |
| Agents per-agent SAs ([agentidentity.go](../providers/agents/api/agentidentity.go)) | read on the whole infra group | **never expires** | manual |
| edges per-edge SAs ([rbac_reconciler.go](../providers/edges/internal/edgectrl/rbac_reconciler.go)) | proxy on one edge | never expires | edge deletion |

Consolidate into one hub-owned **scoped-identity service** — the PR #499
workload-identity machinery generalized:

- Deterministic SA per (owner-kind, tuple) with labeled, reconciled,
  resourceNames-scoped ClusterRoles — including P2 verb subresources.
- TokenRequest-minted, TTL'd tokens only; no legacy token Secrets.
- Attestation pluggable per owner kind (pod attestation for workloads, as
  built; provider-asserted for agent/session identities, since the requesting
  provider is already authenticated).
- **Garbage collection** keyed on the owning object (Project, Agent, Session,
  Edge) — nothing collects any of these identities today.
- Providers then drop their `serviceaccounts` / `clusterroles` /
  `clusterrolebindings` claims entirely, which ends M7 (providers authoring
  RBAC over each other's groups) structurally rather than by review vigilance.

---

## Part 4 — Decisions proposed for pinning

Status column added 2026-09-19 against the tree; the decision and rationale
columns are as pinned in August 2026.

| # | Decision | Rationale | Status (2026-09-19) |
|---|---|---|---|
| X-1 | **One data-plane grammar** (`…/clusters/{clusterID}/{resource}/{rname}/{verb}`) behind the backend proxy; no dedicated hub routers per capability | One transport to secure, one to document; deletes the bypass-prevention machinery a parallel route requires | **Implemented.** `provider-sdk/dataplane.ParsePath` is the only parser in the tree and `provider-sdk/serve` mounts only the contract's route classes; the hub-side action router is deleted (`pkg/hub/server.go:431-434`). |
| X-2 | **Verb grants are kcp RBAC on virtual subresources**, uniform for humans and SAs; the two-gate (visibility + verb SSAR) pattern is the only provider-side authz shape | Removes the dual grant system; auditable; the exec precedent already proves it | **Implemented.** `dataplane.Gate` runs a real GET then an SSAR for `create` on `{resource}/{verb}` as the caller, for every verb of every provider (`provider-sdk/dataplane/gate.go`, `SSARVerb = "create"`). |
| X-3 | **`spec.virtualWorkspace.url` is retired** (or re-scoped to a real, implemented `/vw/*` design). Hub-only provider endpoints are reserved path prefixes on `spec.backend.url`, denied by the proxy from a single list | Ends the fake-VW confusion and the stale decision #6; one URL per provider | **Implemented.** The field is gone from `apis/providers/v1alpha1/types_catalogentry.go`, and `/workload-identities` is the one hub-only reserved prefix (`provider-sdk/serve/serve.go:85-90`). |
| X-4 | **Cross-provider Secret reads are forbidden.** Claims on core Secrets are for provider-owned material only; credential hand-offs are published APIs of the owning provider | Closes the side-door behind violations #1 and #3 | **Partially.** Every known cross-provider read is gone (finding 1), and `pkg/hub/identity/policy.go` will not mint *any* rule on the core group. Not done: narrowing the six surviving `secrets` claims to provider-owned material. |
| X-5 | **All scoped identities come from the hub identity service**: TTL'd, attested, GC'd, resourceNames-scoped. Providers hold no RBAC-authoring claims | Ends M7; one place to audit standing credentials | **Partially.** Service, record and client shipped (`pkg/hub/identity`, `apis/tenancy/v1alpha1/types_scoped_identity.go`, `provider-sdk/identityclient`); agents and edges migrated and dropped their RBAC-authoring claims. kuery and app-studio have not. |
| X-6 | **MCP is a projection.** Tools wrap P1/P2 executors; aggregate verifies bearer↔cluster; discovery is hub-authored from the registry | Prevents MCP becoming a fourth access path with its own trust model | **Partially.** Bearer/cluster verification before fan-out and caller-scoped enumeration are done; per-edge MCP is a declared data-plane verb rather than its own mount. Open: the hub-authored capability resource — the aggregate still serves only `railgrid://about` (`pkg/hub/mcpaggregate/handler.go:361`). |
| X-7 | **Discovery and state-changing hub surfaces are authenticated** (`/api/providers`, heartbeat, metrics off the public listener) | Prerequisite for org-scoped catalogs (provider-scoping P-1..P-12) and basic hygiene | **Partially.** `GET /api/providers` is authenticated and Org-scoped, and `/metrics` is off the public listener. The heartbeat is authenticated but ships `--provider-heartbeat-auth=warn`, so it is not yet fail-closed. |
| X-8 | **Consumers route by publication** (shared grammar package + `status`-stamped endpoints), never by hand-built foreign paths | Ends violation #6; provider renames stop breaking callers silently | **Implemented.** `dataplane.ProviderPath` renders every foreign path (agents, app-studio), and kuery follows `KubernetesCluster.status.url`. |

---

## Part 5 — Migration

Each phase is independently shippable and none blocks the others except as
noted.

**Phase 0 — close the fail-open surfaces (small, urgent).**
Authenticate heartbeat (providers already hold `RAILGRID_HUB_TOKEN`); move
`GET /api/providers` under the authenticated subrouter; verify bearer↔cluster
in the MCP aggregate; strip `X-Railgrid-*` in the UI proxy; move `/metrics` off
the public listener. (The GraphQL `?token=` item is closed: the gateway was
removed.)
Anchors: [server.go:206,355,358](../pkg/hub/server.go),
[heartbeat.go](../pkg/hub/providers/heartbeat.go),
[mcpaggregate/handler.go:73](../pkg/hub/mcpaggregate/handler.go),
[proxy.go:53](../pkg/hub/providers/proxy.go).

**Phase 1 — Provider Actions onto the P2 grammar (reshapes PR #499). DONE on
this branch.**
Move the databricks action route to
`/services/providers/databricks/actions/clusters/{clusterID}/tables/{name}/query_table/v1`;
add the verb SSAR; extend
[project_scope.go](../pkg/hub/workloadidentity/project_scope.go) to collect
`allowedActions` and [ensureWorkloadRBAC](../pkg/hub/serviceaccounts/workload_identity.go)
to emit `tables/query_table` rules; delete
[provideractions/handler.go](../pkg/hub/provideractions/handler.go) +
[authorizer.go](../pkg/hub/provideractions/authorizer.go) and the `/actions`
proxy reservation; retire `virtualWorkspace.url` (X-3) and put
`/workload-identities/review` on the reserved-prefix list; keep the exchange,
SDK, grant UX, and catalog validation. Fix the doc claims (production vs
dev-mode chain; human-caller enforcement).

**Phase 2 — extract the server-kit; migrate the M4 dialects.**
Factor the enforcement kit out of the databricks actions handler + infra
dataplane handler; migrate edges `edgeproxy/` and agents `s2s/` onto it.
Introduce the consumer-side grammar package and `status`-stamped endpoints;
replace the hardcoded paths in app-studio and agents (X-8). Fix
kuery: claim `edges.railgrid.ai/{kubernetesclusters,linuxservers}`, follow
`status.URL`, delete the dead `/services/edges-proxy` dial.

**Phase 3 — the identity service.**
Generalize the workload-identity minter (owner kinds, attestation plug,
TokenRequest-only, GC controller). Migrate agents and edges
identity minting onto it; remove their RBAC-authoring claims (X-5). Add GC
for existing `railgrid-wi-*` identities.

**Phase 4 — credential APIs replace the Secrets side-door.**
code provider publishes `RegistryCredential` (CR or declared action);
app-studio consumes it; infrastructure takes
`spec.imagePullSecretRef` as a typed input; delete the `<instance>-registry`
convention and the PAT-reading path; narrow every provider's `secrets`
claim to owned material (X-4).

**Phase 5 — docs.**
Rewrite [provider-connectivity-contract.md](./provider-connectivity-contract.md):
remove the deleted built-ins (`mcp`/`kubernetesedges`/`serveredges` dirs are
empty), correct kuery's conformance row (it syncs as the provider SA over a
dead mount), fold contracts 3's two data-plane shapes into the single P2
grammar. Amend [providers.md](./providers.md) decision #6 per X-3. Rewrite
[provider-actions.md](./provider-actions.md) for the P2 transport.

### What gets deleted (the payoff)

- The hub Provider Actions router + Go grant authorizer (~1,400 lines) and
  its human/workload asymmetry.
- `spec.virtualWorkspace.url` as a dial target, its stale docs, and the
  `/actions` reserved-path machinery.
- Two of three identity minters and every `clusterroles`/`clusterrolebindings`
  provider claim.
- The git-PAT-reading code path and the `<instance>-registry` string
  convention.
- kuery's dead edges channel and its dangling claim.
- Four hand-built foreign-URL grammars in consumer providers.

---

## Appendix — full channel inventory *(historical — August 2026 snapshot; many anchors no longer exist)*

Identities: **C** = caller bearer, **P** = provider SA, **W** = minted
workload/agent SA, **H** = hub-privileged, **R** = provider's runtime
credential, **X** = external credential owned by the tenant connection.

### A. Consumer-side channels (app-studio, agents)

| Ch | Source → target | Mechanism | Id | Anchor |
|---|---|---|---|---|
| A1 | app-studio → infra dataplane (log/sync/restart/env/process/exec/proxy) | M4 | C | [dataplane_client.go:36](../providers/app-studio/api/dataplane_client.go) |
| A2 | app-studio → infra Templates (read) | M1 (kube REST) | C | [project_template.go:608](../providers/app-studio/api/project_template.go) |
| A3 | app-studio → infra Instances (CRUD; generic to any bound GVR) | M1 | C | [provider_resources.go:148](../providers/app-studio/api/provider_resources.go) |
| A4 | app-studio → code CRs (Connections/Repos/Commits/Packages, incl. writes) | M1 | C | [code_repository.go:38](../providers/app-studio/api/code_repository.go) |
| A5 | app-studio → code tools (checkout/commit/build) via hub aggregate | M5 | C | [llm.go:1572](../providers/app-studio/api/llm.go) |
| A6 | app-studio → infra/databricks tools via aggregate (allowlisted) | M5 | C | [llm.go:2287](../providers/app-studio/api/llm.go) |
| A7 | app-studio → hub `/api/providers` (action catalog + skills) | M6 | C | [provider_action_catalog.go:266](../providers/app-studio/api/provider_action_catalog.go) |
| A8 | app-studio → hub `/api/provider-actions/invoke` | M6 | C/W | [integrations.go:635](../providers/app-studio/api/integrations.go) |
| A9 | app-studio auto-integration LIST of all action-bound GVRs | M1+M6 | C | [automatic_integrations.go:83](../providers/app-studio/api/automatic_integrations.go) |
| A10 | app-studio reads code PAT Secret → mints `<instance>-registry` | **M3** | C+claim | [project_promote.go:47](../providers/app-studio/api/project_promote.go) |
| A11 | app-studio writes actions env (exchange URL/CA) into Instance spec | M1 | C | [project_template.go:423](../providers/app-studio/api/project_template.go) |
| A12 | app-studio preview probe / browser-worker fetch of `status.previewURL` | direct HTTP | none | [preview_edge.go:105](../providers/app-studio/api/preview_edge.go) |
| A13 | agents → infra dataplane proxy (instance MCP servers, SearXNG) | M4 | C or W | [tools/tools.go:118](../providers/agents/tools/tools.go) |
| A14 | agents → aggregate MCP (edges family; interactive only) | M5 | C | [agents.go:661](../providers/agents/api/agents.go) |
| A15 | agents mints per-agent SA (read on all of infra group, no expiry) | **M7** | P→W | [agentidentity.go:61](../providers/agents/api/agentidentity.go) |
| A16 | agents inbound s2s (`/s2s/clusters/{c}/agents/{name}/runs`) | M4 | caller SA | [s2s.go:37](../providers/agents/api/s2s.go) |

### B. Hub-mediated surfaces

| Ch | Surface | Posture | Anchor |
|---|---|---|---|
| B1 | Backend proxy `/services/providers/{name}/*` | fail-open identity, strips+reinjects `X-Railgrid-*`, forwards bearer | [proxy.go:100](../pkg/hub/providers/proxy.go) |
| B2 | `/actions` denial on backend proxy (PR #499) | fail-closed; case-sensitive; backend proxy only | [proxy.go:326](../pkg/hub/providers/proxy.go) |
| B3 | `POST /api/provider-actions/invoke` → `virtualWorkspace.url` | fail-closed; grants enforced for workload SAs only | [handler.go:217](../pkg/hub/provideractions/handler.go) |
| B4 | `POST /api/provider-actions/workload/exchange` → infra attestation | fail-closed; unauthenticated route by design (attestation is authn) | [workloadidentity.go:132](../pkg/hub/workloadidentity/workloadidentity.go) |
| B5 | MCP aggregate `/services/mcpserver/{cluster}/…/mcp` | **fail-open**: token unverified, cluster caller-asserted | [handler.go:65](../pkg/hub/mcpaggregate/handler.go) |
| B6 | MCPServer controller tool discovery (background) | hub-minted per-MCPServer SA token | [controller.go:163](../pkg/hub/controllers/mcpserver/controller.go) |
| B7 | ~~GraphQL gateway `/graphql/{cluster}`~~ | removed (with its `?token=` acceptance); tenant traffic goes through B8 | — |
| B8 | kcp front door `/clusters/…` (static/SA/OIDC dispatch) | fail-closed; Org-path and root-path refusals; membership check | [proxy.go:260](../pkg/server/proxy/proxy.go) |
| B9 | UI proxy `/ui/providers/{name}/*` | **does not strip `X-Railgrid-*`** | [proxy.go:53](../pkg/hub/providers/proxy.go) |
| B10 | `GET /api/providers` | **unauthenticated** | [api.go:164](../pkg/hub/providers/api.go) |
| B11 | `POST /api/providers/{name}/heartbeat` | **unauthenticated, state-changing** | [heartbeat.go:41](../pkg/hub/providers/heartbeat.go) |
| B12 | Provider provisioning (workspace, SA, kubeconfig, non-expiring token) | admin-gated | [provision.go:98](../pkg/hub/providers/provision.go) |
| B13 | Enable/disable (server-side APIBinding, dependency closure) | tenant-gated | [providers_enable.go:75](../pkg/hub/restapi/providers_enable.go) |
| B14 | Enable-time edges-proxy grant (non-VW Secrets/Namespaces write) | consented at Enable; broadest standing grant | [bootstrap.go:1731](../pkg/hub/kcp/bootstrap.go) |
| B15 | MCPServer connect (returns long-lived SA token to portal) | tenant-gated issuance; pairs with B5's non-verification | [mcp.go:135](../pkg/hub/restapi/mcp.go) |

### C. Provider claims and remaining channels

| Ch | Channel | Note | Anchor |
|---|---|---|---|
| C1 | kuery claims `railgrid.ai/edges` | **dangling** — resource no longer exported | [manifest.yaml:52](../providers/kuery/manifest.yaml) |
| C2 | Six providers hold `secrets` claims (4 with write) | the M3 side-door | providers/*/manifest.yaml |
| C3 | Edge agent tunnel join/reconnect (join token → per-edge SA, revdial) | healthy M4+identity pattern | [agent_proxy_builder_v2.go:361](../providers/edges/internal/tunnel/agent_proxy_builder_v2.go) |
| C4 | Edges consumer egress `edgeproxy/…/{k8s\|ssh\|mcp}` (TokenReview+SAR `proxy`) | healthy; stamps `status.URL` for consumers | [auth.go:116](../providers/edges/internal/tunnel/auth.go) |
| C5 | Edges `svc/` proxy (Service CR + authSecret read as caller; agent host allowlist) | confused-deputy-safe by design | [service_proxy.go:282](../providers/edges/internal/tunnel/service_proxy.go) |
| C6 | kuery edge sync via `/services/edges-proxy/…` as provider SA | **dead mount + wrong group + hardcoded path** | [engagement/controller.go:363](../providers/kuery/engagement/controller.go) |
| C7 | kuery query API scoped only by `X-Railgrid-Tenant` header, no per-object RBAC re-check | read amplification: provider-SA-synced data served on a header check | [queryapi/handler.go:44](../providers/kuery/queryapi/handler.go) |
| C8 | code → GitHub (Connection token; exclusive holder) | conforming external egress | [tenant/credentials.go:28](../providers/code/tenant/credentials.go) |
| C9 | infra imagePullSecret bridge (`<instance>-registry` name convention → runtime SA) | **M8** string contract across 2 providers | [bridge.go:53](../providers/infrastructure/controller/instance/bridge.go) |
| C10 | infra OIDC client-secret bridge into runtime namespace | M8, finalizer-guarded | [bridge.go:59](../providers/infrastructure/controller/instance/bridge.go) |
| C11 | infra Gateway/HTTPRoute emission against the shared platform Gateway | M8, RGD-validated | [kro/rgd.go:209](../providers/infrastructure/backend/kro/rgd.go) |
| C12 | databricks: narrowest posture in the repo (`secrets: [get]`, no foreign consumers beyond actions/MCP) | the model citizen | [manifest.yaml:391](https://github.com/railgrid/providers/blob/main/providers/databricks/manifest.yaml) |

---

Implementation anchors for the healthy patterns this design generalizes:
[infra dataplane handler](../providers/infrastructure/dataplane/handler.go),
[exec SSAR authorizer](../providers/infrastructure/dataplane/authorizer.go),
[credential-dropping tenant client](../providers/infrastructure/tenant/client.go),
[CatalogEntry action validation](../apis/providers/v1alpha1/actions.go),
[workload identity minter](../pkg/hub/serviceaccounts/workload_identity.go),
[edges consumer authorization](../providers/edges/internal/tunnel/auth.go),
[shared databricks executor](https://github.com/railgrid/providers/blob/main/providers/databricks/tenant/action.go).
