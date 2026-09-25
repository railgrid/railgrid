# Providers — extending railgrid with provider UIs, virtual workspaces, and APIs

**Status:** Design draft (ready for phase-1 implementation)
**Owner:** TBD
**Last updated:** 2026-05-22

---

## Restore-from-reboot summary

> This section exists so a fresh Claude Code session (or a returning human)
> can pick up the work without re-reading the conversation history.

**Where we are:** design phase complete. No code written yet. The branch is
`mcp.example` on a clean tree apart from this doc and a stray `bob` file.

**Goal in one sentence:** make railgrid pluggable so third parties can ship
"providers" that bring an `APIExport`, optional UI, optional backend HTTP
service, and optional controllers — all installed via Helm, discovered and
wired up by hub controllers, surfaced in the portal under
`/providers/{name}`, proxied to avoid CORS.

**Decisions already pinned** (don't re-litigate; jump to §"Hub changes" for
the how):

| # | Decision | Rationale |
|---|---|---|
| 0 | API group = **`providers.railgrid.ai`** (separate from `railgrid.ai`) | Catalog entries and bindings are platform-owner-only. Excluding them from the `core.railgrid.ai` merged APIExport keeps them out of tenant workspaces. Tenants interact via portal/hub mediation, not raw CR access |
| 1 | Terminology = **provider** (not "addon") | `root:railgrid:providers` already exists; first-party railgrid `APIExport`s already live there |
| 2 | UI embedding = **custom element loaded from the hub proxy** (`<railgrid-provider-{name}>` from `/ui/providers/{name}/main.js`, SRI-pinned) | Same-origin → no CORS, shared stylesheet, no iframe seams. Any frontend stack that can register a custom element. The bundle runs as trusted code in the portal document; a sandboxed iframe + postMessage bridge is the (L) follow-up if untrusted third-party providers become a goal. Module Federation rejected (Vue lock-in + build coupling) |
| 3 | Provider workspace = `root:railgrid:providers:{name}`, **auto-created by hub** on `CatalogEntry` admission | Chart needs no kcp credentials |
| 4 | Distribution = **one Helm chart per provider**, targets *host cluster only* | All kcp work owned by hub catalog controller |
| 5 | Registration = **hybrid**: chart creates `CatalogEntry` shell; provider pod heartbeats every 30s (`POST /api/providers/{name}/heartbeat`, TTL 90s) | Declarative install + runtime liveness |
| 6 | VW = **APIExport-only.** `spec.virtualWorkspace.url` is **removed** from the `CatalogEntry`: the hub never routed `/services/providers/{name}/vw/*`, so the field and its `/vw/*` story are gone rather than deprecated | Custom verbs belong on the resource itself. Each declared verb is published as a kcp **custom subresource** `{resource}/{verb}` on the provider's own APIExport and reached at `/clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}` on the kcp front door — the only transport. The hub-proxied grammar `/services/providers/{name}/{dataplane,actions}/clusters/…` is gone |
| 7 | Provider→kcp identity = SA `provider` in the provider's workspace; hub mints kubeconfig and writes it as Secret `railgrid-provider-kubeconfig` in the provider's host namespace; a legacy non-expiring ServiceAccount token, rotated on demand via the credentials-rotate endpoints (see §"Credentials and rotation") | Reuses existing exec-credential pattern from `pkg/server/proxy/proxy.go` |
| 8 | Schema delivery = **inline** in `CatalogEntry.spec.apiExport.schemas[].body`; hub parses + applies | Solves chicken-and-egg of "chart can't apply to workspace that doesn't exist yet" |
| 9 | PermissionClaim acceptance = **auto-accept-all** at Enable time, but ONLY for claims marked `tenantScoped: true`. Non-tenant-scoped claims refused unless admin sets `railgrid.ai/accept-untrusted-claims=true` on the `CatalogEntry` | Simplest safe default; per-claim toggles deferred to v2 |
| 10 | Tenant Enable = **a kcp `APIBinding` in the tenant workspace, created by the hub**. No `ProviderBinding` CRD — kcp-native. The portal never talks to kcp for this: it `POST`s the hub's Enable endpoint (`pkg/hub/restapi/providers_enable.go`), which checks workspace membership and that the provider's `dependencies` are already enabled, then creates the `APIBinding` **as kcp-admin**. Permission-claim safety enforced by `MaximalPermissionPolicy` on the APIExport (kcp). | Simpler, kcp-native; fewer moving parts. The hub's kcp user-proxy pre-checks the cluster path against the user's default workspace, so a user-credentialed create would 403 on every other workspace — the membership check moves into the Enable handler instead. Audit/inventory queries fan out across tenant workspaces (acceptable). |

**Deferred (do NOT block phase 1):**

- Bound provider CRs reachable through the hub's kcp proxy
  (`/clusters/{cluster}/apis/{group}/…`) after `APIBinding` lands — **must
  work by end of phase 3**; the proxy forwards to kcp as the caller, so this
  is kcp's own binding semantics and expected to "just work", but needs
  validation. If it doesn't, file follow-up; do not gate phase 1–2.
- Cross-provider dependencies — **explicitly out of scope** for v1. A
  provider's controller can error out if its prerequisite APIExport isn't
  bound.
- Heartbeat over kcp leases instead of HTTP — possible v2 simplification.
- Per-permission-claim UI toggles — v2.

**Next concrete step:** implement phase 1. See §"Phase 1 implementation
plan" for the backend file-by-file checklist; phase 2 (portal wiring) is
detailed under §"Portal changes" + §"Phase 2 implementation plan".

**Portal integration anchors** (referenced throughout):

- Layout + side nav: [portal/src/components/AppLayout.vue](../portal/src/components/AppLayout.vue) — hardcoded `navItems` at lines 48-53 becomes computed
- Bootstrap point: [portal/src/App.vue](../portal/src/App.vue) — auth detect + load providers store before render
- Static routes: [portal/src/router/index.ts](../portal/src/router/index.ts)
- Catalog + bindings data: hub REST (`GET /api/providers`, `/api/orgs/{org}/…`) consumed by [portal/src/stores/providers.ts](../portal/src/stores/providers.ts)
- Dev proxy: [portal/vite.config.ts](../portal/vite.config.ts)
- CSP injection point: [pkg/hub/portal.go](../pkg/hub/portal.go) — middleware around the embedded SPA handler

---

## Goal

Make railgrid a pluggable platform. A *provider* is a self-contained extension
that brings:

1. An **`APIExport`** in kcp that user tenants bind to consume the
   provider — the *one required piece*.
2. A **UI** (micro-frontend, any stack) shown inside the railgrid portal —
   optional.
3. Optional **controllers** reconciling the provider's resources.
4. Optional **custom HTTP backend** (REST/WebSocket) for the UI
   to talk to, proxied through the hub.
5. Optional **virtual workspace** (advanced) for non-CRD verbs.

A user opens the portal, browses the "Providers" view (catalog), clicks
**Enable** on a provider, and:

- The provider's APIs become available in their tenant workspace via an
  `APIBinding`.
- The provider's UI (if any) appears under `/providers/{name}` in the
  portal — proxied through the hub, so it is same-origin and there are no
  CORS concerns.

## Why "provider" (terminology)

The kcp workspace `root:railgrid:providers` already exists and is where
railgrid's own `APIExport`s live (`railgrid.ai`, `tenancy.railgrid.ai`,
`core.railgrid.ai`). See
[config/kcp/workspace-providers.yaml](../config/kcp/workspace-providers.yaml)
and [config/kcp/embed.go](../config/kcp/embed.go).

A third-party provider therefore lives at `root:railgrid:providers:{name}` —
sibling to the first-party providers, with identical mechanics. No new
top-level workspace, no new vocabulary.

## Non-goals (v1)

- Hot-reloading provider controllers inside the hub process (providers run
  as separate Deployments).
- Cross-provider dependency resolution / version compatibility matrices.
- A public provider marketplace / registry. Distribution is Helm chart +
  `kubectl apply`.
- Per-provider auth policies (single OIDC at the hub).
- Per-permission-claim consent UI (v1: accept-all on Enable; per-claim
  toggles deferred to v2).

---

## Architecture overview

```
┌──────────────────────────────────────────────────────────────────────┐
│                            railgrid-hub                                  │
│                                                                       │
│  /ui/*                       → embedded SPA (Vue portal)              │
│  /ui/providers/{p}/*         → reverse proxy → catalog.spec.ui.url    │
│  /services/providers/{p}/*   → reverse proxy → catalog.spec.backend.url│
│      …/mcp, …/mcp/sse             federated by the MCP aggregate      │
│      …/oauth/*, …/webhooks/*, …/agent/*, …/healthz — no verbs         │
│  /clusters/*                 → kcp front door (CR traffic AND every  │
│      …/{resource}/{name}/{verb}   declared verb: a custom subresource │
│                                   the shard proxies to the provider)  │
│  /services/mcpserver/*       → aggregate MCP endpoint                 │
│  /api/providers/{p}/heartbeat (POST, provider-SA-authed)              │
│                                                                       │
│  Catalog controller: watches CatalogEntry                     │
│    - auto-creates root:railgrid:providers:{p} sub-workspace              │
│    - creates `provider` ServiceAccount in that workspace              │
│    - writes railgrid-provider-kubeconfig Secret to provider's namespace  │
│      (the provider's own `init` applies schemas + APIExport)          │
│    - rebuilds proxy routing table; tracks heartbeats                  │
│                                                                       │
│  Tenants APIBind to provider APIExports DIRECTLY in their workspace   │
│    - Portal POSTs the hub Enable endpoint; hub creates the APIBinding │
│      as kcp-admin after a membership + dependency check               │
│    - Catalog controller pre-grants tenants `bind` verb cluster-wide   │
│    - Permission safety = MaximalPermissionPolicy on the APIExport     │
└──────────────────────────────────────────────────────────────────────┘
        │ kcp                              │ HTTP (in-cluster Service)
        ▼                                  ▼
┌────────────────────────────┐   ┌────────────────────────────────────┐
│ root:railgrid:providers:cost  │   │ Provider pod (e.g. cost)           │
│   APIExport cost.railgrid.ai  │◄──│   - mounts railgrid-provider-kubeconfig│
│   APIResourceSchema(s)     │   │   - runs controllers against kcp    │
│   SA: provider             │   │   - serves UI on :3000 (optional)   │
└────────────────────────────┘   │   - serves backend HTTP on :8080    │
        ▲                        │   - heartbeats hub every 30s        │
        │ APIBinding (kcp        └────────────────────────────────────┘
        │ serves natively)
┌────────────────────────────┐
│ root:railgrid:tenants:alice   │
│   (user workspace — sees   │
│    cost CRs natively)      │
└────────────────────────────┘
```

Single origin from the browser's perspective: every request goes to
`railgrid.example.com`. The hub fans out to providers internally.

**Key clarification on traffic flow:** provider CRs are served by kcp via
the normal `/clusters/...` path on the hub — the same flow as railgrid's own
CRDs today — and so are the provider's **verbs**, as custom subresources on
those CRs. The `/services/providers/{name}` proxy is *only* for the
non-kube classes (MCP, browser OAuth, signed webhooks, the agent tunnel,
health); it carries no CR traffic and no verbs.

---

## Provider isolation (the cross-provider boundary)

**A provider's backend layer is private. Another provider may never reach
into it. All cross-provider interaction goes through the owning provider's
*published* API surface.**

This is the single rule that keeps the provider plane composable. State it
as three parts:

1. **Each provider owns a backend layer, and that layer is private to it.**
   The "backend layer" is everything behind the provider's published API:
   its controllers, its runtime/target clusters and the credentials to
   them, its databases and object stores, its internal Services, its kro
   RGDs / Terraform / cloud SDK calls — every implementation detail of
   *how* it materializes and operates what it exports. No other component
   holds a handle to any of it.

2. **A provider must NOT touch another provider's backend layer.**
   Concretely, a provider must never:
   - hold a second credential (kubeconfig, DB DSN, API key) to another
     provider's runtime cluster, database, or internal service;
   - call another provider's internal Service / pod / REST endpoint
     directly, or share its datastore;
   - encode another provider's backend topology (cluster URLs, namespaces,
     service names) in its own config.

3. **Cross-provider interaction goes only through the other provider's
   published interface, as the tenant/caller:**
   - **kcp `APIExport` resources** — the other provider's CRDs, consumed by
     binding to its `APIExport` (an `APIBinding` in the tenant workspace)
     and reading/writing its CRs over the normal `/clusters/...` path.
     Control-plane state (spec/status) flows this way. A provider that
     *composes* another's kind — creates and manages it as part of its own
     product — reaches it through an identity-agnostic **permission claim** on
     its own APIExport, generated from `spec.dependencies[].composes[]`; see
     §"Composition" below.
   - **Data-plane verbs** on those resources, for streams, proxies and other
     verbs that aren't plain CRUD. Every verb is declared
     (`spec.dataPlane.verbs` or `spec.actions`), and every declared verb is
     published on the owning provider's APIExport as a kcp **custom
     subresource** named `{resource}/{verb}`. It is therefore reachable as an
     ordinary API path, discoverable with `kubectl`:

     ```
     /apis/{group}/{version}/{resource}/{rname}/{verb}
     ```

     which kcp authorizes as RBAC on the `{resource}/{verb}` noun and the
     serving shard reverse-proxies to the provider with the caller's identity
     stamped in requestheader headers. That is the only spelling; there is
     no hub-proxied grammar, no hub-side authorizer and no second URL field —
     `spec.virtualWorkspace` is retired. A component of a multi-component
     object is the `component` query parameter. The owning provider serves
     them against *its* backend; the caller never sees that backend.

   Both are invoked **as the identity kcp authenticated** — a tenant user or
   ServiceAccount on the hub's `/clusters/{id}`, or the calling provider
   itself through its own export virtual workspace on a `composes[]` claim
   (see contracts 2 and 3 in
   [`provider-connectivity-contract.md`](./provider-connectivity-contract.md))
   — and **routed by binding, never by a hardcoded backend URL**. The calling
   provider resolves *which* provider backs a workspace from the
   binding/APIExport, not from its own configuration, and renders the path with
   `dataplane.Callers.ExportVerbURL` / `dataplane.SubresourcePath` rather than
   building the string — or follows a coordinate the owning provider published
   in `status` (as kuery does with `KubernetesCluster.status.url`).

**Why the rule pays off:**

- **Substitutability / BYO.** Because the caller addresses a *bound
  resource* and not a backend URL, the workspace can be backed by a
  *different instance* of the owning provider — its own runtime cluster,
  its own APIExport — with **zero change** in the caller. A provider that
  reached a backend directly would be welded to one deployment.
- **Single owner per backend.** Exactly one provider holds the credential
  and the operational responsibility for a given runtime/datastore. No
  duplicated clients, no two-writers-one-cluster ambiguity.
- **Contained blast radius.** A provider's compromise or outage is bounded
  by its published claims, not by who else happens to hold a key into its
  cluster.

**Reference implementation.** App Studio used to hold
`APP_STUDIO_RUNTIME_KUBECONFIG` — a direct credential into the
infrastructure provider's runtime cluster. That is exactly the violation
this rule forbids. The current implementation selects an infrastructure
Template, creates its development instance through the tenant API, and calls
the infrastructure provider's published data-plane subresources (for example
`…/instances/{name}/sync?component={component}` on its own export virtual
workspace). App Studio carries no runtime credential and
does not know the provider's backend topology. See the current boundary in
[`app-studio-sandbox-runtime.md`](./app-studio-sandbox-runtime.md) and the
retained historical proposal in
[`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md).

> Cross-provider *dependency resolution* (ordering, version-compatibility
> matrices) remains out of scope for v1 (see Non-goals). The isolation rule
> is about
> the *access boundary*, not orchestration: a provider may consume another
> provider's published API, but it owns the failure handling when a
> prerequisite binding isn't present.

### Provider Actions

Authenticated workload callers can read `GET /api/providers` to validate action
grants against the live catalog. The hub verifies their bearer online with the
workload audience in the selected tenant workspace and checks the backing
ServiceAccount. Delegated user identities also require the hub's signed proof.
Catalog visibility is scoped to the verified organization; this read does not
grant a membership role, mutation access, or bypass action authorization. The
only hub REST calls a delegated token can make beyond it are the hub-access
capabilities a tenant accepted for the provider (see *Hub access* below).
Anonymous callers remain rejected, and human catalog discovery retains its
optional-organization behavior.

Provider Actions extends the isolation boundary with catalog-declared,
versioned capabilities addressed in the same resource-addressed shape as the
infrastructure `dataplane/` verbs. An action's id is `{verb}/v{n}` and its
`boundResource` names the resource it hangs off, so it occupies one
`{resource}/{verb}` coordinate — `repositories/mint-clone-token` for the code
provider's `mint-clone-token/v1` — exactly like a data-plane verb, and like
one it is published on the owning provider's APIExport as a custom
subresource. App Studio stores a non-owning `providerReference`, grants an
exact provider action, resource reference, and schema digest, and
**materializes the grant as kcp RBAC** (verbs `*` on that coordinate,
name-scoped — kcp maps the HTTP method onto the RBAC verb) on the workload
identity. Invocations are the kube path
`/clusters/{clusterID}/apis/{group}/{version}/{resource}/{rname}/{action}` —
the `/v<n>` of the action id is not in the path — and kcp authorizes the
subresource noun before the shard proxies the request; the owning provider's
gate then runs a SubjectAccessReview for `get` on the parent as the caller
and acts as itself. Uniform for humans and workloads, mirroring how
data-plane exec is authorized. There is no dedicated hub action router and no
hub-proxied action route; the backend proxy reserves only the hub-internal
`/workload-identities/*` prefix. The generic catalog also carries schemas,
execution mode, read-only/risk/idempotency policy, limits, consent, and
deprecation metadata.
See the [Provider Actions contract and verification
guide](./provider-actions.md) for the workload exchange, SDK, provider
boundary, and verification commands, and
[cross-provider-simplification.md](./cross-provider-simplification.md) for
how this pattern generalizes (decision #6's `spec.virtualWorkspace.url` dial
target is retired in favor of reserved prefixes on `spec.backend.url`).

### Hub access

A provider that receives a delegated user token in place of the caller's bearer
(always for org-owned providers; for platform providers per
`--provider-delegated-tokens`) cannot use it on the hub REST surface — except
for the capabilities it declares in `CatalogEntry.spec.hubAccess` **and** a
tenant accepted for it. Declaring grants nothing on its own.

```yaml
spec:
  hubAccess:
    - capability: memberships.read     # read the member list
      scope: org                        # org | workspace
      reason: "Check who an app is shared with."
    - capability: memberships.invite    # add someone to the org
      scope: org                        # org only
      maxRole: member                   # the only role a provider can grant
      allowInvite: true                 # may pre-provision an unknown email
      reason: "Invite the people you share an app with."
```

The capability set is closed and owned by the hub (`pkg/hub/hubaccess`); a
provider never names routes. Today it maps to:

| Capability | Scope | Route |
|---|---|---|
| `memberships.read` | `org` | `GET /api/orgs/{org}/memberships` |
| `memberships.read` | `workspace` | `GET /api/orgs/{org}/workspaces/{ws}/memberships` |
| `memberships.invite` | `org` | `POST /api/orgs/{org}/memberships` |

Removals, role changes, workspace membership writes and org or workspace
management are not in the set and are refused for delegated tokens.

**Consent.** The Enable dialog lists each requested capability with its
reason. Accepting an org-scoped one needs an org admin; a workspace-scoped one
a workspace or org admin. `POST …/providers/{name}/enable` takes the choice as
`acceptedHubAccess: [{capability, scope}]` and records it in a `Grant`
(`tenants.railgrid.ai`, subject kind `Provider`) in `root:railgrid:system:tenants`,
which no tenant or provider identity can reach. Each capability the enabler may
decide is recorded as accepted (ticked) or declined (not); the ones they may
not decide keep their earlier decision, or stay undecided — so a workspace
admin enabling a provider never turns off something only an org admin can
decide. Disable deletes the grant. `GET …/providers/enabled` reports,
per provider, which capabilities are granted and which are still pending (new
in its catalog entry, or declined); the Providers page offers *Review access*
for the latter.

**Enforcement.** A gate in front of the tenant routes admits a delegated call
only when the route maps to a capability the provider currently declares and
the grant for that provider in that workspace includes. The narrower of
declaration and grant wins, so a catalog update never widens access until
someone accepts it. The call then goes through the normal tenant middleware as
the person the token stands for — their own role still applies, so a provider
can never do more than that person — and the membership handler caps the role
at `member`, never changes an existing member's role, and honours
`allowInvite`. Invitations are rate-limited per provider and organization, and
every delegated mutation is logged with provider, person, target and role.

**Provider identity.** The hub's proof on a delegated account covers the
provider's name and, since proof v2, its owner org, so an org-owned provider
that shadows a platform provider of the same name never uses the platform
provider's grant (or vice versa).

**Upgrade default.** `--provider-hub-access-platform-default` (default `true`)
lets a *platform* provider use a capability it declares that nobody entitled to
decide it has decided yet, so existing workspaces keep working; a decision
(accepted or declined) always wins, and org-owned providers always need an
acceptance. Rollout order: the hub first
(the CatalogEntry schema gains `hubAccess`), then providers that declare it.

### Composition — one provider building on another's kinds

A product is rarely one provider. An App Studio project IS an infrastructure
`Instance` plus a code `Repository`: App Studio's reconciler has to CREATE and
MANAGE those objects, in the tenant's workspace, as part of doing its job.

That is not something a provider may take for itself. It is declared, consented
to, and minted:

```yaml
spec:
  dependencies:
    - name: infrastructure
      composes:
        - group: infrastructure.railgrid.ai
          resource: instances
          verbs: [get, list, watch, create, update, delete]
    - name: code
      composes:
        - group: code.railgrid.ai
          resource: repositories
          verbs: [get, list, watch, create, update]
        - group: code.railgrid.ai
          resource: repositorycommits
          verbs: [get, list, watch]
```

**Declaring grants nothing.** The catalog controller validates the declaration
fail-closed — no wildcards, only ordinary Kubernetes verbs, and the group must
be the one the named dependency actually exports — and a malformed entry drops
the provider out of the registry rather than leaving a half-read declaration
behind. The declaration is then projected into `/api/providers` so the Enable
dialog and a consumer can read it.

**Three things are generated from that one declaration**, and all three have
to agree before a composed object can be touched:

1. **A permission claim on the composing provider's own APIExport, with no
   `identityHash`.** `provider-sdk/cmd/apiexportgen` appends one claim per
   `composes[]` entry that `spec.apiExport.permissionClaims` does not already
   cover (`MergeCompositionClaims`), always — there is no opt-in. The claim
   carries no identity hash **on purpose**: kcp resolves an identity-agnostic
   claim per consumer workspace, against whatever APIExport *that* workspace
   bound for the claimed group, which is exactly what keeps working when an
   organization runs its own copy of the dependency. Where the manifest
   already hand-writes a claim on the same `(group, resource)`, the
   hand-written one wins and nothing is appended.
2. **The tenant's consent, on the APIBinding.** The claim reaches the tenant
   as a claim to accept at Enable, and accepting it is what lets the
   composing provider see and write those objects — through its own APIExport
   virtual workspace, on the manager it already runs
   (`providers/app-studio/controller/project/controller.go`,
   `providers/kuery/engagement/edgewatch.go`). A rejected or un-accepted claim
   is the visible cause of "enabled, but it never builds anything".
3. **The platform whitelist:** a cluster-scoped `PermissionClaimPolicy` in
   the `admin.kcp.io` group. kcp admits an identity-agnostic claim only when
   this policy pairs the claimer with the claimed group. It is generated from
   the same `composes[]` entries by
   `hack/generate-permission-claim-policy.mjs` into
   [`config/kcp/permissionclaimpolicy.yaml`](../config/kcp/permissionclaimpolicy.yaml),
   checked in CI (`make verify-provider-contract` re-runs the generator with
   `--check`), and applied by the hub at bootstrap through the **admin virtual
   workspace** at `/services/admin/clusters/root`
   ([`pkg/hub/bootstrap/permissionclaimpolicy.go`](../pkg/hub/bootstrap/permissionclaimpolicy.go)),
   only when discovery says kcp serves the API.

Three facts about that policy are easy to get wrong:

- **The claimer is the API group the export itself exports, not the provider's
  name.** App Studio's export `ai.railgrid.ai` is the claimer, not
  `app-studio`; the generator reads it off `spec.resources[].group` on the
  generated APIExport because the manifest names the export, not the group it
  serves, and for most providers the two differ. A provider whose export has
  no resources at all therefore cannot hold such a claim — there is no
  claimer to name.
- **Naming a pairing RESERVES both groups.** As soon as a group appears in the
  policy, as claimer or as claimed, only the subjects in `spec.providers` may
  export it — including against cluster admins. A group is not merely allowed
  to be claimed; it is spoken for.
- **The subject today is the provider ServiceAccount**
  `system:serviceaccount:default:provider`, the identity the hub mints in each
  `root:railgrid:providers:<name>` workspace and the one every provider's
  `init` applies its APIExport with. The known caveat, recorded in the
  generated file's header: a bare ServiceAccount username is
  **logical-cluster-scoped** in kcp, so that string also matches a
  `default/provider` ServiceAccount a tenant creates in their own workspace.
  kcp's disambiguated form `system:kcp:serviceaccount:{cluster}:{ns}:{name}`
  cannot be written statically, because `{cluster}` is assigned at runtime.

**The scoped identity is not how composed kinds are reached at all.** The
hub's identity policy has no composition clause any more: a composing
reconciler reads, watches and writes the composed kinds through its own
APIExport virtual workspace under the accepted claim (App Studio's dependency
watch included, `providers/app-studio/controller/tenantwatch`), and a verb on
another provider's object is a claimed custom subresource reached the same way
(kuery's `kubernetesclusters/k8s`). What a scoped identity still carries is
clauses A–D: the provider's own kinds, named foreign reads, declared foreign
verbs where a claim is not (yet) used — App Studio's `repositories/commit` and
its >1 MiB bundle staging, which kcp will not carry — and the platform list.
See
[provider-connectivity-contract.md §"Scoped identities"](./provider-connectivity-contract.md#scoped-identities--asking-the-hub-instead-of-minting).

**Upgrade default.** Composition *decisions* follow
`--provider-hub-access-platform-default` exactly as hub access does: a
*platform* provider composes what it declares in a workspace where nobody
entitled to decide has, so existing workspaces keep working; a recorded
decision always wins, and an org-owned provider always needs an explicit
acceptance. The claim itself is a separate consent, on the APIBinding, and an
existing binding does not acquire a new claim on its own — see
[provider-contract-migration.md](./provider-contract-migration.md).

### Provider assistant skills

`CatalogEntry.spec.assistantSkills` is an inline, versioned package contract
for read-only App Studio guidance. Each package carries `packageName`,
`version`, a complete raw `SKILL.md`, optional package-relative resources, and
a canonical `sha256:` digest. The digest covers the package identity, version,
document bytes, and resources in deterministic path order; it is provenance
and integrity, not an authority grant. The authenticated hub
`/api/providers` catalog distributes validated inline bytes. It never follows
a provider URL and does not grant credentials, tools, models, permissions, or
runtime authority.

Publication is bounded: at most 64 packages per `CatalogEntry`, each document
is at most 32 KiB, each supporting resource at most 64 KiB, and all documents
and resources for one provider entry are at most 512 KiB. Invalid packages are
isolated with bounded sanitized warnings where possible. These limits apply to
the published artifact; they do not make a skill trusted instructions.

App Studio projects expose valid packages as read-only, provider-qualified
system skills (`providers/<provider>/<packageName>`). They use the existing
catalog progressive-disclosure flow: metadata discovery, explicit
`load_skill`, bounded `read_skill_resource`, activation policy, and immutable
catalog/digest snapshots for a turn. Distribution is not provider or action
enablement: a published provider package follows the system-skill default of
enabled, and each project may disable or re-enable the qualified skill. A
transient provider heartbeat/readiness change does not revoke declared
guidance. If the request has no bearer or the provider skill catalog is
temporarily unavailable, App Studio preserves bundled and project skills and
emits only a bounded sanitized warning where applicable. Provider Actions and
their grants remain the authoritative, fail-closed path for data access or
other effects; a skill can never create or widen that authority.

---

## CRDs

Two new CRDs, both in the railgrid API group, both first-party (added to the
existing `railgrid.ai` `APIExport`).

### `CatalogEntry` (cluster-scoped, in `root:railgrid:providers`)

Installed by an administrator via the provider's Helm chart, which targets
the host Kubernetes cluster API. The hub's catalog controller projects it
into kcp.

```yaml
apiVersion: providers.railgrid.ai/v1alpha1
kind: CatalogEntry
metadata:
  name: cost-insights
spec:
  displayName: "Cost Insights"
  description: "Per-edge cost attribution and forecasting."
  vendor: "Acme Cloud"
  version: "1.2.0"
  iconURL: "/ui/providers/cost-insights/icon.svg"  # served via UI proxy

  # Host-cluster namespace where the provider Deployment runs. Hub writes
  # the railgrid-provider-kubeconfig Secret here.
  serviceAccountNamespace: "cost-insights"

  # OPTIONAL: micro-frontend. Omit if provider has no UI.
  ui:
    url: "http://cost-insights-ui.cost-insights.svc.cluster.local"
    indexPath: "/"

  # OPTIONAL: custom HTTP backend (NOT for CR traffic — CRs go via kcp).
  # Omit if provider only exposes CRs.
  backend:
    url: "http://cost-insights.cost-insights.svc.cluster.local:8080"
    healthPath: "/healthz"

  # REQUIRED: the APIExport the provider owns. Hub creates the workspace,
  # applies the inline schema(s), then creates the APIExport.
  apiExport:
    name: "cost.railgrid.ai"
    # Inline APIResourceSchema docs the hub applies on first reconcile.
    # Multiple schemas allowed; one APIExport references them all.
    schemas:
      - groupResource: "greetings.cost.railgrid.ai"
        # The full v1alpha1 APIResourceSchema body as a string. Hub parses
        # and applies. Kept inline so the chart needs no kcp access.
        body: |
          apiVersion: apis.kcp.io/v1alpha1
          kind: APIResourceSchema
          metadata:
            name: v260522-abc.greetings.cost.railgrid.ai
          spec: { ... }
    # PermissionClaims declared on the APIExport itself (kcp-enforced).
    # Mirrored here as informational for the Enable dialog.
    permissionClaims:
      - resource: configmaps
        verbs: [get, list, watch]
        # Tenant-scoped flag tells the binding controller this is safe to
        # auto-accept. Out-of-tenant claims are refused.
        tenantScoped: true

  # OPTIONAL: the data-plane verbs this provider serves on its own resources
  # (Pillar 2 class (a)). Declaring a verb grants NOTHING — the provider still
  # authorizes every call. What the declaration does is PUBLISH the coordinate:
  # every entry here (and every spec.actions[] entry) becomes a kcp custom
  # subresource "<resource>/<verb>" on the generated APIExport, so the verb is
  # an ordinary, kubectl-discoverable API path; consumers read it off
  # /api/providers instead of hardcoding it; and the hub scoped-identity
  # service will mint a capability only for a verb it can verify exists.
  # Requires apiExport: a provider declares verbs on its own kinds.
  dataPlane:
    verbs:
      - resource: greetings   # plural, in this provider's own API group
        verb: greet           # see the name rule below; not a standard kube verb
        description: "Return the greeting this Greeting describes."
        stream: false         # true when the verb upgrades or streams
        readOnly: true        # false when it mutates the resource or what it fronts
```

**The verb name is a kcp resource name.** `"<resource>/<verb>"` becomes
`spec.resources[].name` on the APIExport, which kcp holds to
`^[a-z][-a-z0-9]*[a-z0-9](/[a-z][-a-z0-9]*[a-z0-9])?$` — lower-case letters,
digits and hyphens, **no underscores** — and it refuses `status` and `scale`
outright, because those describe the object's own shape and are declared on
the APIResourceSchema. **One bad name makes the whole export unappliable**, not
just its entry, so `hack/verify-provider-contract.mjs` catches it at review
time (check `subresource-name`) rather than at a tenant's Enable.

```yaml
status:
  # Filled by catalog controller
  workspace: "root:railgrid:providers:cost-insights"
  apiExportRef:
    workspace: "root:railgrid:providers:cost-insights"
    name: "cost.railgrid.ai"
  endpoints:
    ui: "http://cost-insights-ui.cost-insights.svc.cluster.local"
    backend: "http://cost-insights.cost-insights.svc.cluster.local:8080"

  # Filled by heartbeat. provider.Ready = true iff heartbeat within TTL
  # AND (no backend declared OR backend healthz is 200).
  lastHeartbeat: "2026-05-22T10:15:00Z"
  reportedVersion: "1.2.0"
  ready: true

  conditions:
    - type: WorkspaceReady
    - type: APIExportReady
    - type: BackendHealthy   # only present if .spec.backend set
    - type: Ready
```

### Tenant Enable = a kcp `APIBinding` created by the hub (no second CRD)

We deliberately do NOT ship a `ProviderBinding` CRD. Enabling a provider
means a vanilla kcp `APIBinding` in the tenant's own workspace, pointing at
the provider's `APIExport`. This is the kcp-native pattern; adding a second
CRD would only re-wrap what `APIBinding` already does.

The **hub** creates it, not the portal. Clicking Enable `POST`s the hub's
Enable endpoint (`pkg/hub/restapi/providers_enable.go`), which:

1. checks the caller is a member of the target workspace;
2. checks every provider named in `spec.dependencies` is already enabled
   there (409 otherwise);
3. reconciles the accepted permission claims against the provider's
   declared set (verbs always come from the declaration, never the
   request); and
4. creates the `APIBinding` **as kcp-admin**.

The portal never calls kcp for Enable. It cannot: the hub's kcp user-proxy
pre-checks the cluster path against the user's default workspace and 403s
every other workspace before the request reaches kcp, so the membership
check the proxy would have done implicitly is done by this handler instead.

```yaml
# Created in the tenant's workspace (e.g. root:railgrid:tenants:alice)
# by the hub's Enable endpoint, as kcp-admin.
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: cost-insights
spec:
  reference:
    export:
      path: "root:railgrid:providers:cost-insights"
      name: "cost.railgrid.ai"
  permissionClaims:
    - resource: configmaps
      verbs: [get, list, watch]
      state: Accepted
```

**Why this works safely:**

- **The binding is created as kcp-admin, after a membership check.** The
  Enable handler is the authorization boundary: a caller who is not a member
  of the target workspace never reaches the create. The catalog controller
  still pre-grants tenants the `bind` verb on each provider's `APIExport`
  once its `CatalogEntry` reaches Ready (a `ClusterRole` aggregated to the
  tenant identity), so tenant-credentialed reads and deletes of their own
  binding keep working.
- **Permission claims are gated by kcp's `MaximalPermissionPolicy`** on
  each provider's `APIExport`. A tenant cannot accept a claim outside
  their workspace because the export's `MaximalPermissionPolicy` refuses.
  The provider chart declares the maximum claim set; users pick from it.
- **Audit and inventory** ("who enabled X?") = list `APIBindings` across
  tenant workspaces filtered by `reference.export.path`. Acceptable at
  current scale; revisit if it ever isn't.
- **Uninstall** (admin deletes `CatalogEntry`) leaves orphan
  `APIBindings` in tenant workspaces — kcp flips them NotReady (broken
  reference). The catalog controller's deletion hook walks tenant
  workspaces and removes them.
- **Disable** = tenant deletes their own `APIBinding`. No special API.

---

## Hub changes

### 1. Catalog controller (`pkg/hub/controllers/providercatalog/`)

Watches `CatalogEntry` in `root:railgrid:providers`. On each
reconcile:

1. **Sub-workspace**: ensure `root:railgrid:providers:{name}` exists. Use
   the existing kcp tenancy client. Created with type `universal`,
   `bootstrap.kcp.io/create-only: "true"`.
2. **Provider ServiceAccount**: ensure a `ServiceAccount` named
   `provider` exists in that workspace, bound to `cluster-admin` on the
   workspace (admin within its own sandbox, nothing outside).
3. **Kubeconfig Secret**: mint a token for the SA, build an exec-credential
   kubeconfig pointing at the hub URL with cluster
   `root:railgrid:providers:{name}`, write it as Secret
   `railgrid-provider-kubeconfig` in `spec.serviceAccountNamespace` of the
   *host* cluster. Idempotent. Rotate token every 24h (set
   `kubernetes.io/service-account-token` style annotation).
4. **Schema + APIExport apply**: parse `spec.apiExport.schemas[].body`,
   apply each as an `APIResourceSchema` in the workspace, then
   apply/update the `APIExport` referencing them.
5. **Registry upsert**: push (Name, UIURL, BackendURL, VWURL, Ready) into
   the in-process `Registry` (below).

The controller runs in the hub. It uses the hub's existing controller
manager and the kcp admin client.

### 2. In-memory routing registry (`pkg/hub/providers/`)

```go
type Registry struct {
    mu     sync.RWMutex
    byName map[string]*Provider
}

type Provider struct {
    Name       string
    UIURL      *url.URL  // may be nil
    BackendURL *url.URL  // may be nil
    Ready      bool
    Version    string
}

func (r *Registry) Get(name string) (*Provider, bool)
func (r *Registry) List() []*Provider
func (r *Registry) Upsert(p *Provider)
func (r *Registry) Delete(name string)
```

Pure in-memory; rebuilt on hub restart from the `CatalogEntry`
list. No external store.

### 3. Heartbeat endpoint

```
POST /api/providers/{name}/heartbeat
Authorization: Bearer <provider-SA-token>
Content-Type: application/json

{ "version": "1.2.0", "buildTime": "...", "status": "healthy" }
```

- **Platform providers only.** The endpoint addresses providers by bare name
  with no org context, so it resolves `{name}` to the *platform* workspace
  `root:railgrid:providers:{name}`. An org-owned (BYO) provider lives at
  `root:railgrid:tenants:{orgUUID}:providers:{name}` and cannot beat here; that
  is deliberate, since otherwise one org could keep a platform provider of the
  same name looking alive. Org-owned providers never set `HeartbeatRequired`
  and their readiness rests on endpoint validity alone
  (`Registry.Heartbeat`). In `enforce` they receive the same generic 401 as
  any unregistered name, so do not read that 401 as a credential problem.
- Authenticates the bearer token as the provider's own service account
  (`system:serviceaccount:default:provider`) by TokenReview in the provider's
  platform workspace `root:railgrid:providers:{name}` — the same SA and token the
  hub minted into the provider kubeconfig. Any other identity is rejected with
  403, a missing or unrecognised token with 401. The endpoint has no auth
  middleware in front of it; this check is the whole of its authentication.
  `--provider-heartbeat-auth=warn` (this release's default) logs failures
  and still records the beat so providers on older charts keep reporting
  alive while they are rolled forward; `enforce` (next release's default)
  rejects them, and such a provider goes stale after the TTL.
- A rejection body says only which class of failure it was (`heartbeat not
  authenticated` / `forbidden` / `heartbeat verification unavailable`); the
  reason — logical cluster, expected service account, TokenReview error —
  goes to the hub log only, because the endpoint answers anonymous callers.
  For the same reason `enforce` answers a beat for an unregistered provider
  with exactly that 401 rather than a 404, so the endpoint cannot be used to
  enumerate provider names. In `warn` mode, which accepts unauthenticated
  beats by design, an unknown name still gets 404.
- Provider side: every provider runs the one shared client,
  `provider-sdk/hubclient.RunHeartbeat` (configured by `ConfigFromEnv` from
  `RAILGRID_HUB_URL`, `RAILGRID_PROVIDER_NAME`, `RAILGRID_HUB_INSECURE`,
  `RAILGRID_PROVIDER_VERSION`). The bearer is `RAILGRID_HUB_TOKEN` if set,
  otherwise the token in `RAILGRID_PROVIDER_KUBECONFIG`
  (`hubclient.ResolveHubToken`). A 401/403 is logged with what to fix.
  Charts need no change.
- Updates `CatalogEntry.status.lastHeartbeat` and
  `reportedVersion`.
- TTL: 90 seconds. Catalog controller flips `Ready=false` if no heartbeat
  within TTL.
- Cheap: providers heartbeat every 30s; tiny payload.

### 4. Generic provider proxy

Two route prefixes registered in [pkg/hub/server.go](../pkg/hub/server.go):

```go
// New paths in pkg/api/url/paths.go
const (
    PathPrefixProvidersUI      = "/ui/providers"
    PathPrefixProvidersBackend = "/services/providers"
)

router.PathPrefix(apiurl.PathPrefixProvidersUI + "/").Handler(
    providers.NewUIProxy(registry, logger))
router.PathPrefix(apiurl.PathPrefixProvidersBackend + "/").Handler(
    providers.NewBackendProxy(registry, authMiddleware, logger))
```

Proxy behavior:

- Parse `{name}` from path: `/ui/providers/cost-insights/foo` → name=`cost-insights`, rest=`/foo`.
- Look up in registry; **404** if unknown, **503** if not Ready.
- Backend proxy: serves only the non-kube route classes (MCP, browser OAuth,
  signed webhooks, the agent tunnel, health, hub-only) — a verb is never a
  route here. Requires standard railgrid auth middleware; forwards the
  user's `Authorization` header and adds `X-Railgrid-User` plus the tenant's
  identity — the workspace's kcp logical-cluster ID — as both
  `X-Railgrid-Tenant` and `X-Railgrid-Cluster`. The workspace path
  (`root:railgrid:tenants:<org>:<ws>`) is hub-internal and is never forwarded;
  a provider that needs it (or the org/workspace UUIDs) reads the
  workspace's `LogicalCluster` `kcp.io/path` annotation as the caller
  (`provider-sdk/tenantaccess.ResolveWorkspace`). If the ID cannot be
  resolved, neither tenant header is sent.
- UI proxy: no auth requirement on static assets; injects
  `X-Railgrid-Base-Path: /ui/providers/{name}` so the provider can rewrite
  absolute links.
- Standard `httputil.ReverseProxy` with header sanitization.

Note: there is no `/services/providers/{name}/vw/*` sub-path, and no
`/dataplane/*` or `/actions/*` one either. `spec.virtualWorkspace.url` has
been **removed** from the `CatalogEntry` type. Custom verbs are kcp custom
subresources reached on `/clusters/{id}/apis/…` — see
[provider-connectivity-contract.md](./provider-connectivity-contract.md).

### 5. Catalog controller's RBAC + enable plumbing

When the catalog controller (`pkg/hub/controllers/providercatalog/`)
reconciles a `CatalogEntry`, it additionally:

1. **Grants tenants `bind` verb on the provider's `APIExport`.**
   The controller creates / updates a `ClusterRole` named
   `railgrid:providers:bind:{name}` in the provider's workspace with rules
   `[apiGroups: ["apis.kcp.io"], resources: ["apiexports"], verbs: ["bind"], resourceNames: ["{name}"]]`,
   and a `ClusterRoleBinding` aggregating that role to the tenant-identity
   group (`system:authenticated` is too broad — we use the same identity
   subject used by the existing tenant `APIBinding` to `core.railgrid.ai`).
2. **Sets `MaximalPermissionPolicy` on the provider's `APIExport`** to
   the union of claims declared in
   `CatalogEntry.spec.apiExport.permissionClaims` that are marked
   `tenantScoped`. This is the kcp-enforced safety wall: tenants cannot
   accept a claim that escapes their workspace.
3. **Cleanup on delete.** When the `CatalogEntry` is deleted, the
   controller walks tenant workspaces, lists `APIBindings` whose
   `reference.export.path` matches this provider's workspace, and deletes
   them. Best-effort; orphans flip NotReady on their own anyway.

There is no separate "binding reconciler" — the tenant's `APIBinding`
itself is the reconciled state, and kcp handles its lifecycle.

### 6. Bootstrap

The kcp bootstrap in [pkg/hub/bootstrap](../pkg/hub/bootstrap) already
creates `root:railgrid:providers`. We add:

- `APIResourceSchema` and `APIExport` for `CatalogEntry` in the
  `providers.railgrid.ai` group (admin-only — bound only in
  `root:railgrid:providers`, never in tenant workspaces, hence excluded from
  the merged `core.railgrid.ai` APIExport).
- New embed paths for these schemas in
  [config/kcp/embed.go](../config/kcp/embed.go).

No new workspaces in bootstrap; provider sub-workspaces are created
lazily on `CatalogEntry` admission.

---

## Portal changes

The portal is Vue 3 + Pinia + urql + Vite, with a single shared layout
([portal/src/components/AppLayout.vue](../portal/src/components/AppLayout.vue))
that every page wraps. Routes are static today
([portal/src/router/index.ts](../portal/src/router/index.ts)) and the side
nav reads a hardcoded `navItems` const at
[portal/src/components/AppLayout.vue:48-53](../portal/src/components/AppLayout.vue#L48-L53).
Both become provider-aware.

### Files to create

| Path | Purpose |
|---|---|
| `portal/src/stores/providers.ts` | Pinia store: catalog list, current user's bindings, derived nav items, route registration |
| `portal/src/router/providers.ts` | `registerProviderRoutes(bindings)` — idempotent `router.addRoute()` calls |
| `portal/src/pages/ProvidersPage.vue` | The `/providers` catalog view (grid of cards, Enable/Disable) |
| `portal/src/pages/ProviderFrame.vue` | Per-provider custom-element host; loads the SRI-pinned bundle, mounts `<railgrid-provider-{name}>`, pushes `railgridContext` (host fetch, tenant, theme, subPath), bubbles `railgrid-navigate` |
| `portal/src/components/ProviderEnableDialog.vue` | Modal listing `permissionClaims` (read from `CatalogEntry.spec.apiExport.permissionClaims` via `/api/providers`); on confirm, the portal POSTs the hub's Enable endpoint with the accepted claims, and the hub creates the `APIBinding` in the user's workspace as kcp-admin |
| `portal/sdk/index.ts` (new package `@railgrid/provider-sdk`) | `useRailgrid()` composable for providers' UIs: token, user, tenant, theme, `onNavigate` |
| `portal/sdk/package.json`, `tsconfig.json`, `README.md` | SDK packaging — publish to npm or include as workspace |

### Files to edit

| Path | Edit |
|---|---|
| [portal/src/App.vue](../portal/src/App.vue) | Mount scoped pages only after route-context resolution and URL commit. Register provider matchers in `main.ts` before the initial navigation; load the catalog for the verified context. |
| [portal/src/router/routes.ts](../portal/src/router/routes.ts) | Declare global, organization, and workspace routes using the shared UUID-constrained prefixes. `contextGuard.ts` resolves their authority; `providers.ts` registers the common provider matcher. |
| [portal/src/components/AppLayout.vue](../portal/src/components/AppLayout.vue) | Replace the static `navItems` array (lines 48-53) with a `computed` that merges static items with `providersStore.enabledNavItems`. Add a static "Providers" entry (catalog browser) before the dynamic block. Render dynamic items with `<img :src="iconURL">` instead of `<component :is="icon">` so providers can use their own icons. |
| [portal/vite.config.ts](../portal/vite.config.ts) | Add proxy entries so dev-mode shell on `:3000` forwards `/services` and `/ui/providers/*` to the hub at `:9443`. The `/ui/providers/*` rule must take precedence over Vite's own `/ui/` static serving (use `bypass: () => undefined` only for that prefix). |
| [pkg/hub/portal_security.go](../pkg/hub/portal_security.go) | Sets the portal `Content-Security-Policy`: `default-src 'self'; frame-src 'self' <configured platform frame sources>; img-src 'self' data: blob:; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; font-src 'self' data:`. `script-src 'self'` (no `'unsafe-inline'`) admits provider bundles because they are hub-proxied and therefore same-origin; the portal ships no inline script (the theme pre-paint bootstrap is `portal/public/theme-bootstrap.js`). `frame-src` is for the portal's own iframes (App Studio preview hosts), not for providers. |

### Reactive providers store

The current implementation lives at
[portal/src/stores/providers.ts](../portal/src/stores/providers.ts). It
holds a single `items: ProviderDTO[]` array loaded from the hub's
admin-mediated `/api/providers`. Today every authenticated user sees every
installed provider in the nav.

**Phase 3 change** (when direct-APIBinding Enable lands): split into two
sources:

- `catalog: ProviderDTO[]` — what's installed on the platform (hub
  `/api/providers`, unchanged).
- `enabled: APIBinding[]` — what the *current user* has bound, queried
  via kcp's APIBinding list in the user's workspace, filtered by
  `reference.export.path` starting with `root:railgrid:providers:`.

`enabledNavItems` becomes `enabled.filter(ready).map(...)`. The catalog
page shows union with status badges (Available / Enabled / Pending).

### Route registration

`portal/src/main.ts` registers the shared provider route shape before the initial
navigation. `portal/src/router/providers.ts` installs one matcher per router:
`WORKSPACE_ROUTE + '/providers/:name/:rest(.*)*'`, where `WORKSPACE_ROUTE` contains
the UUID-constrained organization and workspace segments. Resource suffixes
become `railgridContext.subPath`; the host preserves query strings and fragments.
Route registration does not wait for catalog discovery. The context guard and
ProviderFrame's existing catalog/binding checks gate mounting and access.

### `ProviderFrame.vue` (custom-element host)

The real implementation is
[portal/src/pages/ProviderFrame.vue](../portal/src/pages/ProviderFrame.vue);
[portal/src/components/DashboardTile.vue](../portal/src/components/DashboardTile.vue)
is the same lifecycle for the optional `<railgrid-dashboard-tile-{name}>`
element. There is no iframe and no postMessage handshake. The shape:

1. **Load the bundle, pinned.** `providerScriptLoader.ts` injects
   `/ui/providers/{name}/main.js?v={version}` as a classic `<script>` into the
   portal document. When the catalog entry carries `mainJSIntegrity` (the
   `sha384-…` the hub computed at registration, see §"Security
   considerations"), the loader sets `integrity` so the browser refuses a
   bundle whose bytes differ. It deliberately sets no `crossorigin`
   attribute: the bundle is a same-origin load (response type `basic`), for
   which the browser enforces SRI without one, and `crossorigin` would turn
   the load into a CORS-mode request that the hub's UI proxy does not
   negotiate (it sends no `Access-Control-Allow-Origin`), so the script would
   fail to load. Without a pin it loads anyway and logs a warning. The `?v=`
   cache-buster does not interact with SRI, which hashes the response body.

   An org-owned provider (`scope: "org"`) is loaded from the same path shape
   but with a grant: the portal first `POST`s `/api/providers/{name}/ui-grant`
   as the user (`providerBundle.ts`), and the hub answers with
   `/ui/providers/{name}/main.js?v=…&grant=…` plus the bundle's pin, hashed
   through the caller's delegated edge route. The UI proxy redeems the grant
   into a delegated token and serves the bundle over the org's edge tunnel;
   a grant-less URL stays platform-scoped. See
   [byo-providers.md](./byo-providers.md) §"Known gaps" and
   [pkg/hub/providers/ui_grant.go](../pkg/hub/providers/ui_grant.go).
2. **Mount the element.** After `customElements.whenDefined('railgrid-provider-{name}')`
   the host appends `<railgrid-provider-{name}>` into its own DOM. The provider
   shares the portal stylesheet (CSS variables cascade in), so there is no
   visible boundary.
3. **Push context.** The host sets `element.railgridContext` (a JS property, not
   an attribute) and re-pushes it on every theme / workspace / route change:

```ts
// What the host pushes (portal/src/providers/providerContext.ts).
interface ProviderContext {
  subPath?: string        // route after /providers/{name}/, host-router parsed
  user: unknown
  tenant: string | null   // kcp cluster name of the active workspace
  orgUUID: string | null
  workspaceUUID: string | null
  theme: 'light' | 'dark' // RESOLVED, never 'system'
  basePath: string        // /ui/providers/{name}; assets and serviceBase only
  navigationBasePath?: string // /ui/{orgID}/{workspaceID}/providers/{name}
  fetch: ProviderFetch    // host-owned transport, see below
  /** @deprecated one release; read `fetch` instead */ token: string | null
}
```

4. **Navigate.** The element dispatches a bubbling
   `railgrid-navigate` CustomEvent (`{ path, replace? }`); the host translates it
   into a navigation under `/ui/{orgID}/{workspaceID}/providers/{name}/`.
   Use `navigationBasePath` for native links within the provider, or the shared
   `portalkit/navigation` `portalHref(path, context)` helper for links to another
   provider. Keep `basePath` for assets and `serviceBase()`; it is not a page URL.

The host resolves the organization and workspace IDs in the URL through the
caller's authenticated APIs before mounting scoped content. Login preserves the
complete path, query, and fragment. Organization settings use
`/ui/{orgID}/settings/...`; global login, organization chooser, and admin pages
have no workspace prefix. Old unscoped page URLs have no aliases or redirects.
The `/ui/providers/{name}/...` asset proxy remains available for bundle assets.

The URL owns each tab's context. Local storage remembers only a landing
preference. Provider contexts expose the verified IDs and cluster target;
workspace switches invalidate old host fetch transports, including late
responses. Capture the context that starts an asynchronous operation and do not
reacquire a newer context to finish an older operation. Opening or sharing a
portal URL never grants resource access or enables a provider.

**The host fetch.** `railgridContext.fetch` is the only thing a bundle should use
to reach the hub. It resolves relative URLs against the portal origin, injects
`Authorization` and `X-Railgrid-Org` / `X-Railgrid-Workspace` from the host's own
state (so the bundle never holds the user's id token), and refuses anything
outside the provider's allow list
([portal/src/providers/providerFetch.ts](../portal/src/providers/providerFetch.ts)):

| Allowed (same-origin only) | Why |
|---|---|
| `/services/providers/{name}/` | the provider's own backend, via the hub proxy — `/oauth/*` and `/mcp` only; verbs are under `/clusters/` |
| `/ui/providers/{name}/` | its own static assets |
| `/clusters/` | `/clusters/{cluster}/apis/…` — kcp REST by cluster through the hub's kcp proxy (cluster-in-path model; the `portalkit` kube client) |
| `/api/orgs/{orgUUID}/` | org-scoped hub REST, as the user |
| `/api/providers` (GET/HEAD) | the catalog |

Everything else — another provider's backend, `/api/admin`, `/apis/*`, other
origins — throws `ProviderFetchDeniedError` before any request is made.
`portalkit/tenant.ts` exposes `providerFetch(ctx)`, which returns
`ctx.fetch` when the host provides it and falls back to the global `fetch`
plus `ctx.token` against an older host; every in-repo provider portal calls
it. `token` stays on the context for one release (reading it logs a one-time
deprecation warning per provider) and is then removed.

### Provider element contract (what a bundle implements)

```ts
// main.js — a classic script (not a module) registering the element.
class MyProvider extends HTMLElement {
  #ctx: ProviderContext | null = null
  set railgridContext(ctx: ProviderContext | null) { this.#ctx = ctx; this.render() }
  get railgridContext() { return this.#ctx }
  connectedCallback() { this.render() }
  async load() {
    const fetch = providerFetch(this.#ctx)        // portalkit/tenant.ts
    const cluster = this.#ctx!.tenant
    // Bound CRs are read and written with the kube client over /clusters/{id},
    // never through the provider's own backend.
    const kube = createKubeClient({ fetch, cluster })  // portalkit/kube.ts
    const things = await kube.list('example.railgrid.ai/v1alpha1', 'things')
    // A verb on one of those bound objects is a kcp custom subresource on the
    // same front door; kubeVerbPath spells it, serviceBase() is for /oauth and /mcp only.
    await fetch(kubeVerbPath(cluster, { group: 'example.railgrid.ai', version: 'v1alpha1', resource: 'things' },
                             things[0].metadata.name, 'refresh'),
                { method: 'POST' })
  }
  navigate(path: string) {
    this.dispatchEvent(new CustomEvent('railgrid-navigate', { bubbles: true, detail: { path } }))
  }
}
customElements.define('railgrid-provider-my-provider', MyProvider)
```

Optional — a bundle that ignores `railgridContext` still renders; it just has no
tenant scope, no theme, and no synced URL. Vendor `provider-sdk/portalkit/`
into the portal (`make sync-portalkit`) rather than re-implementing the tenant
header contract; see AGENTS.md §5.7.

**No iframe, no `postMessage` bridge to the host.** The element renders into
light DOM in the portal's own document; a provider that wraps itself in an
iframe and talks to the host over `postMessage` is reimplementing the
contract badly. Exactly **two** exceptions are sanctioned:

1. **An OAuth popup posting its result to `window.opener`.** The browser
   OAuth routes (Pillar 2 class (d)) open a popup for the identity provider;
   on callback the popup `postMessage`s its result to the opener and closes.
   The message crosses windows the provider owns at both ends — it is not a
   bridge between the element and the host. Reference: code
   `oauthgithub/oauth.go`.
2. **A sandboxed iframe whose contents are *content*, not UI.** A preview of
   a tenant's own generated application is untrusted content and belongs in a
   sandboxed iframe, listed in the hub's CSP `frame-src`. Reference:
   app-studio's preview surface. The iframe renders a document; it is not a
   transport for the provider's own UI, and the host API is still
   `railgridContext` + `providerFetch`.

Anything else that reaches for `postMessage` or an iframe is a deviation.

### Deep-link behavior

User pastes `https://railgrid.example.com/ui/#/providers/cost/forecasts`
into a fresh browser. Sequence:

1. Vue boots, `App.vue` `onMounted` calls `auth.detectAuthMode()`.
2. If not authenticated → `router.beforeEach` redirects to `/login` (no
   change from today).
3. If authenticated → `await providersStore.load()`. This populates the
   store AND calls `registerProviderRoutes(...)` *before* the first
   `<router-view />` render.
4. Vue Router resolves `/providers/cost/forecasts` → `ProviderFrame.vue`
   with `providerName=cost`, `subPath=forecasts`. The host loads
   `/ui/providers/cost/main.js`, mounts `<railgrid-provider-cost>`, and pushes
   `subPath: 'forecasts'` in `railgridContext`.

The key is awaiting the store load in `App.vue` before rendering. Without
that, the not-found route swallows the deep link.

### Dev-mode wiring

Vite dev server serves `/ui/*` as Vue assets and proxies `/apis`,
`/healthz` to the hub today. We add:

```ts
// vite.config.ts (excerpt)
server: {
  port: 3000,
  proxy: {
    '/apis':     { target: 'https://localhost:9443', changeOrigin: true, secure: false, ws: true },
    '/healthz':  { target: 'https://localhost:9443', changeOrigin: true, secure: false },
    // NEW:
    '/services': { target: 'https://localhost:9443', changeOrigin: true, secure: false, ws: true },
    // /ui/providers/{name}/* MUST go to hub, NOT vite's static dir.
    // Vite proxy matches first; rewrite-strip not needed because hub
    // expects the full path.
    '/ui/providers': { target: 'https://localhost:9443', changeOrigin: true, secure: false },
  },
},
```

In production the hub already proxies these routes directly — no Vite in
the picture.

### Providers catalog page

`/providers` (`ProvidersPage.vue`) — grid of cards from
`providersStore.catalog`. Each card shows:

- Icon (`<img>` from `entry.spec.iconURL` — proxied via hub).
- Display name, vendor, version, description.
- Status badge: Available / Enabled (= an `APIBinding` exists in your
  workspace) / Pending (provider not Ready).
- Primary button:
  - **Enable** when not bound, including before runtime readiness → opens
    `ProviderEnableDialog.vue` listing `permissionClaims`; on confirm, the
    portal calls the hub's workspace-scoped provider Enable endpoint to create
    the `APIBinding`. KCP publishes virtual-workspace endpoints only after a
    consumer binds, so readiness must not gate the first Enable. Permission
    consent and dependency checks still apply; **Open** requires readiness.
  - **Disable** when bound → confirm + delete the user's `APIBinding`.
  - **Re-accept** when the catalog's `permissionClaims` no longer match
    what the user's `APIBinding` has accepted → re-shows the dialog with
    the new claims highlighted; user confirm = patch the `APIBinding`.

`ProviderEnableDialog.vue` lists `permissionClaims` from the
`CatalogEntry`, distinguishes `tenantScoped` vs non
(non-tenant-scoped claims show a red warning explaining the admin
override needed). Confirm → calls the mutation, sets
`acceptedClaimsHash` to a SHA256 of the sorted claims list.

---

## Provider author experience

A provider ships as one Helm chart. **Chart only targets the host
cluster — never kcp directly**: everything inside the provider's kcp workspace
is applied by the provider's own `init`, which the chart runs as an init
container.

A provider author writes exactly **two declarative objects**, and `init` applies
both verbatim:

| Object | File | Written by | Holds |
| --- | --- | --- | --- |
| `CatalogEntry` | `manifest.yaml` | by hand | display metadata, URLs, **the data-plane verbs and actions**, self-hosting, **the APIExport name, its permission claims and what it composes** |
| `APIExport` | `config/kcp/apiexport-<exportName>.yaml` | `make codegen-<name>-provider` | `spec.resources` from kcp `apigen` **plus one `"<resource>/<verb>"` custom-subresource entry per declared verb and action**, `metadata.name` + `spec.permissionClaims` (hand-written **and** composition-derived) from the manifest |

`make codegen-<name>-provider` runs kcp's `apigen` and then
`provider-sdk/cmd/apiexportgen`, which reads `manifest.yaml`, renames the export
(apigen names it after the API *group*, which is not the export name), stamps
the claims — including one identity-agnostic claim per
`spec.dependencies[].composes[]` entry — appends the subresource entries, and
drops apigen's group-named file. Everything downstream is an **output** the
same target writes — `deploy/chart/files/apiexport.yaml` and
`deploy/chart/files/schemas/` — and
`hack/verify-provider-contract.mjs` fails the build when an output drifts from
its input (`claims-parity`, `export-copy`, `subresource-name`).

A subresource entry is `{name: "<resource>/<verb>", group, schema, storage}`.
Its `storage.virtual.reference` points at the provider's own
`DataPlaneEndpointSlice` (`dataplane.railgrid.ai/v1alpha1`, one per provider,
named after the APIExport), whose `status.endpoints[].url` is the provider's
base address from `spec.backend.url` — which is how kcp knows where to
reverse-proxy the verb. Its `schema` names the `APIResourceSchema` of the verb's own kind: every
provider declares one `<Verb>Request` type per verb in its API package
(`apis/.../subresources.go`, `+kubebuilder:resource:path=<verb>`), so
controller-gen and `apigen` mint its schema exactly like a stored kind's, the
name has the `.<verb>.<group>` form kcp's admission demands, and
`apiexportgen` ships it into `deploy/chart/files/schemas/` beside the stored
kinds' (`<verb>.<group>.yaml`) while dropping the kind itself from
`spec.resources` — nothing is stored under it. The shard never resolves that
schema (the request is routed from the storage reference), but a **claimer's**
APIExport virtual workspace does, to learn the kind before it builds the
subresource for another provider (kcp-dev/kcp#4388). A verb whose type is
missing fails codegen, naming the type to add; a verb that is the plural of a
stored kind of the same group (App Studio's `projects/sessions`) references that
kind's schema. An entry is
emitted only for a coordinate whose **parent** resource the same export
already exports — kcp refuses a subresource of nothing — which every provider
in this repository satisfies, infrastructure included: its `instances` and
`templates` schemas come from apigen like everyone else's, and only the
`templates` entry is re-pointed at CachedResource virtual storage by its own
init, once the identityHash is known.

There is no third copy: a permission claim is written in `manifest.yaml`, and
nowhere else. The chart's `catalogentry.yaml` still mirrors the manifest's whole
spec, because that rendering is what reaches production.

**Claims are per resource, not per name — so scope them.** A claim on
`secrets` with nothing else on it grants the provider's ServiceAccount
read-write access to *every* Secret in *every* workspace that enables the
provider: the tenant's cloud credentials and every other provider's backend
credential included. That is why a claim carries a label selector:

```yaml
permissionClaims:
  - resource: secrets
    verbs: [get, list, watch, create, update, delete]
    tenantScoped: true
    selector:
      matchLabels:
        railgrid.ai/owner: agents   # the provider's own name
```

Every Secret the provider owns carries `railgrid.ai/owner: <provider>`
(`provider-sdk/claimscope`); `apiexportgen` renders the selector as the kcp
claim's `defaultSelector` and the hub writes the same `matchLabels` onto the
accepted claim in each tenant's `APIBinding`. kcp then labels, filters and
admission-checks against it: an unlabelled Secret is a **404** through the
provider's virtual workspace, not a 403, and a write outside the selector is
refused. A core-group `secrets` claim with no selector fails
`hack/verify-provider-contract.mjs` (`claim-selector`) at review time and
`provider-sdk/install` at provider init. A Secret the *tenant* writes and the
provider must read is either read as the caller through a data-plane verb, or
labelled by the tenant — the label is the per-Secret consent. See
[provider-connectivity-contract.md §"Label-scoped claims"](./provider-connectivity-contract.md)
for what kcp enforces and for the upgrade caveat (an accepted claim's selector
is immutable, so an already-enabled workspace keeps its wider binding until the
provider is disabled and re-enabled there).

```
provider-cost-insights/
├── Chart.yaml
├── values.yaml
├── files/
│   ├── apiexport.yaml           # generated APIExport (output of codegen)
│   └── schemas/                 # APIResourceSchemas (output of codegen)
└── templates/
    ├── namespace.yaml
    ├── serviceaccount.yaml
    ├── deployment.yaml          # init container (`<provider> init`) + provider pod
    ├── service.yaml             # ClusterIP services for UI and backend
    └── catalogentry.yaml        # CatalogEntry, mirroring manifest.yaml
```

The image bakes `files/` at `/etc/railgrid/kcp`; the init container points
`RAILGRID_KCP_DIR` there. `init` applies the schemas; then, if the export
declares any custom subresource, applies the `DataPlaneEndpointSlice` CRD,
waits for it to be **Established** and writes the slice carrying the
provider's base URL; then applies the APIExport; then creates the
`APIExportEndpointSlice` and the bind grant. The order is load-bearing: kcp
gives an APIExport that references an object a `ClusterCachedResource` for the
referenced kind, and a reference to a kind that is not established yet is
never replicated, so the subresource would be routed nowhere with no error
anywhere. The slice's URL is `spec.backend.url` of the CatalogEntry `init`
self-registers; when something else registers the CatalogEntry — the
Makefile's `install-provider-<name>` applies `manifest.yaml` through the admin
path, and the provider e2e registers it itself — `init` has no file to read it
from, and `RAILGRID_DATAPLANE_URL` names the address instead (the
`init-provider-<name>` targets set it to the manifest's loopback port).

`init` adds nothing to the claims. A claim on another provider's first-party
group is **identity-agnostic**: kcp resolves it per consumer workspace against
whatever export that workspace bound — which is what lets an organization
self-host a dependency. There is no way to pin an `identityHash`; the
machinery that did (`RAILGRID_IDENTITY_HASHES`, the admin identities view,
`identityFor` self-hosting values, stale-claim detection) is gone.

`helm install cost-insights ./chart` →

1. Provider Deployment starts. Reads
   `/var/run/secrets/railgrid/railgrid-provider-kubeconfig` (mounted from the
   Secret the hub will write).
2. `CatalogEntry` is applied to the host cluster API.
3. Hub catalog controller picks it up:
   a. Creates `root:railgrid:providers:cost-insights` workspace.
   b. Creates `provider` SA in that workspace.
   c. Mints token, writes `railgrid-provider-kubeconfig` Secret to
      `cost-insights` namespace.
   d. The provider's own `init` container then applies the
      `APIResourceSchema`s, the `DataPlaneEndpointSlice` carrying its base
      URL, and the generated `APIExport` from `RAILGRID_KCP_DIR`, and creates
      the `APIExportEndpointSlice` and bind grant. The hub does not apply
      them.
4. Provider pod's controller-runtime manager sees the kubeconfig file
   appear (or retries until it does), starts reconciling its own CRs.
5. Provider starts heartbeating; `status.ready=true`.
6. Users see it in `/providers`, click Enable, get an `APIBinding`.

### Alternative: self-bootstrap via an init container

The flow above is **hub-provisioned** — the hub catalog controller owns
all kcp interactions and mints `railgrid-provider-kubeconfig`. A provider
may instead **self-bootstrap** with an init container that holds a kcp
workspace-admin kubeconfig, which lets it be installed into any cluster
with no hub provisioning step. The infrastructure provider supports this
via `bootstrap.enabled=true` (see
[providers/infrastructure](../providers/infrastructure/README.md#b-self-bootstrap-with-an-init-container-bootstrapenabledtrue)).

The key simplification: **one kubeconfig, shared by init and serve.** Two
sources, set by `bootstrap.kubeconfigSource`:

**`hubMinted` (default)** — clean division of responsibility:

```
Platform admin                         Provider owner
─────────────                          ─────────────
applies CatalogEntry                   helm install … --set bootstrap.enabled=true
   │                                       │
   ▼  hub catalog controller               ▼  pod scheduled, waits for the Secret
creates root:railgrid:providers:<name>    init container (`<provider> init`)
mints kubeconfig (cluster-admin          uses railgrid-provider-kubeconfig to install
  in the workspace)                       CRDs / CachedResource / APIExport
HostSecretWriter writes it as            │
  railgrid-provider-kubeconfig              ▼  serve container, SAME Secret, runs
```

The minted `provider` SA is **cluster-admin within the provider
workspace** (`EnsureProviderSA`), so it's powerful enough to do init's
installs *and* run serve. The init/serve volume is **not** `optional` —
the pod blocks until the hub delivers the Secret, giving natural ordering.
Requires the hub to run with `--kubeconfig` so its `HostSecretWriter`
([pkg/hub/providers/secretwriter.go](../pkg/hub/providers/secretwriter.go))
can write into the provider's cluster.

**`supplied`** — fully standalone, no hub: you provide a
workspace-admin kubeconfig (`bootstrap.kcpKubeconfig` /
`kcpKubeconfigSecretRef`) and own the prerequisites (workspace exists,
kubeconfig targets it).

Trade-offs vs. hub-provisioned (model A):

- **hubMinted needs no separate credential** — the platform already minted
  one; the provider owner never handles a kcp admin kubeconfig.
- **Simpler than the old mint-to-Secret approach**: no second token, no
  mid-pod Secret write, no extra RBAC.
- **Privilege**: serve runs with cluster-admin-in-workspace rather than a
  narrow scoped SA. For strict least-privilege, prefer model A with a
  manual init.

All models converge on the same runtime contract: the serve container
mounts a kubeconfig at `/var/run/secrets/railgrid/railgrid-provider-kubeconfig`
and talks to kcp with it. Only *which identity* and *who supplies the
Secret* differ.

### Minimal provider backend contract

A provider's backend (if it declares one) MUST:

- **Be built by `provider-sdk/serve`.** `serve.New(Options{Name, Readiness,
  Portal, MCP, DataPlane, Actions, Subresources, HubOnly, OAuth})` returns the
  whole `http.Handler` with the fixed layout — `/healthz`, `/readyz`, `/mcp` +
  `/mcp/sse`, `/clusters/` (the shard-forwarded subresource path — the only
  way a verb is reached), `/workload-identities/*` (hub-only), `/oauth/`, and
  the portal file server with SPA fallback — and **refuses to register anything
  outside it**. There is no `/api/*`: a provider cannot add one by accident,
  and `hack/verify-provider-contract.mjs` fails the build on an `"/api/` route
  literal (check `adhoc-rest`).
- **Serve every tenant verb through `provider-sdk/dataplane`**, and register
  each one exactly once. A verb is reachable one way:
  - the **kube path**
    `/apis/{group}/{version}/{resource}/{name}/{verb}` — what a caller and
    `kubectl` see. kcp authorizes the `{resource}/{verb}` noun with ordinary
    RBAC and the shard reverse-proxies it to the provider at
    `<endpoint>/clusters/{id}/apis/…`, stripping inbound identity headers and
    stamping the caller into `X-Remote-User` / `X-Remote-Group` /
    `X-Remote-Extra-*` plus a hop counter. There is no bearer here:
    `dataplane.Gate` runs gate 1 as a `SubjectAccessReview` for `get` on the
    parent **on the caller's behalf** and then reads the object as the
    provider — both through the provider's APIExport virtual workspace, found
    from its `APIExportEndpointSlice` (`dataplane.WithProviderConfig(cfg,
    exportName)`); gate 2 is not repeated, because kcp already authorized the
    noun. After the gate the handler acts as the provider; any further
    question about the caller is `dataplane.Authorize`, a SubjectAccessReview
    on the caller's behalf. The hub's backend proxy strips those `X-Remote-*`
    headers from everything it forwards, so nothing routed through the hub
    can reach this route with a forged identity. That SubjectAccessReview is
    a builtin kcp
    serves through the virtual workspace **only for an export that claims
    it**, so every provider with a custom subresource declares
    `authorization.k8s.io/subjectaccessreviews` (`create`, tenantScoped) in
    its manifest — the one hand-written claim a provider that claims no data
    still carries (`subresource-access-claim` in
    `hack/verify-provider-contract.mjs`).

    **Cross-provider calls (provider A calling provider B's verb)** go the
    same way, as A: a `spec.dependencies[].composes[]` entry naming
    `"{resource}/{verb}"` with `verbs: ["*"]` claims the subresource, and A
    addresses it through its own export virtual workspace with its own
    credential (`Callers.ExportVerbURL` + `ProviderHTTPClient`). End-user
    identity is not carried across. Measured against kcp-dev/kcp#4388 at `pr-4388-4bce57376`
    (`TestF4CrossClaimThroughClaimerVW`): a claimer export that claims
    B's `<resource>` *and* `<resource>/<verb>` gets the subresource
    advertised and forwarded through its own APIExport virtual workspace, the
    shard routes it to B's endpoint, and B receives **the claimer's identity**
    (its ServiceAccount, `system:serviceaccount:default:provider` — same
    spelling as every other provider's) with kcp's warrant among the
    forwarded extras. Two things were needed, and both are in: (1) kcp builds
    the claimed subresource only when the entry's `schema` names an existing
    APIResourceSchema, so every verb has a `<Verb>Request` kind in the
    provider's API package whose apigen-minted schema the chart ships beside
    the stored kinds' (`apis/.../subresources.go`); (2) B's gate treats a
    ServiceAccount from another logical cluster, forwarded by a shard, as
    authorized by the claim kcp already enforced (`ProxiedIdentity.IsForeignProvider`)
    — a SubjectAccessReview would refuse it, since it holds no RBAC in the
    tenant workspace and kcp does not honour the forwarded warrant inside a
    SAR. kuery reaches every edge's `kubernetesclusters/k8s` this way
    (`TestF4CrossClaimThroughClaimerVW` proves the hop with a 200).

  There is no hub-proxied grammar: `serve.New` mounts no `/dataplane/` or
  `/actions/` prefix, and a verb never carries a bearer. `dataplane.Serve`
  applies the declared limits and the `actionwire` envelope. Verify with the
  `dataplane/conformance` suite against the real `serve.New` server.
- **Set `serve.Options.Subresources`.** It is the `"<resource>/<verb>"` →
  route table that says which coordinates exist and whether each is a
  data-plane verb or an action (and at which
  version). Build it from the manifest with
  `serve.SubresourcesFromCatalogEntryFile(path)` rather than by hand, so the
  table cannot disagree with what `apiexportgen` published; a coordinate
  absent from it is a 404 there even if a handler would have answered.
- **Publish the shard-facing address.** The URL in the provider's
  `DataPlaneEndpointSlice` comes from `spec.backend.url` and must be the
  address only the shard can reach: on that path the caller's identity is
  headers on a trusted connection, so anyone who can reach the endpoint
  directly can claim any identity.
- **Declare every verb in the manifest** — `spec.dataPlane.verbs` for
  unversioned streaming/proxy verbs, `spec.actions` for versioned, schema'd
  request/response ones. Declaring grants nothing; it is what publishes the
  coordinate as a custom subresource, lets consumers read it off
  `/api/providers`, and lets the hub's scoped-identity service mint a
  capability for a coordinate it can verify exists. **Every declared name must
  pass kcp's rule** for `spec.resources[].name`:
  `^[a-z][-a-z0-9]*[a-z0-9](/[a-z][-a-z0-9]*[a-z0-9])?$`, no underscores, and
  never `status` or `scale` (infrastructure's status verb is
  `instances/runtime-status`; the code provider's is
  `repositories/mint-clone-token`). One bad name makes the whole export
  unappliable.
- `GET /readyz` → 200 when the provider's tenant watches are actually healthy
  (`provider-sdk/vwhealth`). This — not `/healthz` — is what
  `spec.backend.healthPath` should point at: `/healthz` only says the process
  is up, which stays true while nothing reconciles.
- Heartbeat, **platform providers only**: `POST /api/providers/{name}/heartbeat`
  to the hub every 30s via the shared `provider-sdk/hubclient.RunHeartbeat`
  (not a local copy), authenticated as the provider's own service account
  (token from `hubclient.ResolveHubToken`: `RAILGRID_HUB_TOKEN`, else the
  provider kubeconfig's bearer). `HeartbeatConfig.CanSend` is **required**:
  it is consulted before every beat, so a provider whose watches are dead
  stops reporting alive. Wire it to the provider's readiness
  (`vwhealth.Readiness.Check`, the same gate as `/readyz`); `RunHeartbeat`
  refuses to start without it. An org-owned (BYO) provider MUST NOT beat:
  the endpoint is keyed by bare name and resolves only platform providers, so
  the beat is rejected and would in any case never mark it Ready. Its readiness
  comes from endpoint validity instead — see the heartbeat endpoint section.
- `GET /healthz` → 200 when the process is serving (liveness).

A provider's controller (the kcp-talking part) MUST:

- Wait for `railgrid-provider-kubeconfig` Secret to appear before starting.
- Use the kubeconfig's `provider` SA identity. The SA only has rights in
  the provider's own workspace; cross-workspace access is via the
  `APIExport`'s VirtualWorkspace endpoint (kcp serves this natively
  using the APIExport's identity).
- Be a set of **watch-driven reconcilers on multicluster-runtime**, not a
  polling loop. Tenant kinds are watched through the virtual workspace
  (`provider-sdk/apiexportprovider` → `mcmanager` → `mcbuilder`), one
  workqueue per tenant workspace; provider-private kinds go on the manager's
  local cluster. Every piece of durable state is a `status` field or a
  private kind in the provider's workspace, so a reconcile can be rebuilt
  from the objects after a restart. `RequeueAfter` is used only to pace an
  external system that cannot be watched (a git host, a ticket source, a
  runner's HTTP API) or to back off a failure — never to notice that another
  kcp object changed. Write loops run under `provider-sdk/leaderelection`;
  the HTTP surface (actions, MCP, portal) serves on every replica. The
  canonical wiring is `providers/code/controller_manager.go`; the rationale
  and a full before/after is in
  `railgrid/providers/docs/reconciler-architecture-review.md`. See
  [AGENTS.md §5.8](../AGENTS.md#58-reconcilers-not-loops).

A provider's UI MUST:

- Serve static assets such that internal links are relative or rooted at
  `/ui/providers/{name}/`. Use `X-Railgrid-Base-Path` from the proxy if a
  build-time base is needed.

---

## Credentials and rotation

Every provider gets one credential from the hub: a kubeconfig for the `provider`
ServiceAccount in its own workspace (`RAILGRID_PROVIDER_KUBECONFIG`). Its bearer is
a legacy `kubernetes.io/service-account-token` Secret, so it does not expire —
it is valid until the Secret or the ServiceAccount is deleted.

### What the credential is allowed to do

The ServiceAccount is bound to a role **inside its own provider workspace and
nowhere else**; cross-workspace reach only ever comes from the APIExport's
permission claims, which each consuming workspace accepts at Enable time.

Which role is a hub flag:

| `--provider-workspace-cluster-admin` | Bound to | |
|---|---|---|
| `true` (default this release) | `cluster-admin` | The historical behaviour. |
| `false` (default next release) | generated `railgrid:provider` | Explicit allow-list. |

`railgrid:provider` is generated from what providers actually do with this
kubeconfig (`providerClusterRoleRules` in `pkg/hub/providers/provision.go`):
`apis.kcp.io` APIResourceSchemas / APIExports / APIExportEndpointSlices /
APIBindings, `apiexports/content` with all verbs (the APIExport
virtual-workspace authorizer SARs the in-flight request's own verb, discovery
included), `bind` on `apiexports` so `init` can write the tenant bind grant,
`cache.kcp.io` CachedResources and their endpoint slices, `core.kcp.io`
LogicalClusters (read), `providers.railgrid.ai` CatalogEntries and their status,
ClusterRoles and ClusterRoleBindings, CustomResourceDefinitions, core
ServiceAccounts / Secrets / ConfigMaps / Namespaces / Events,
`coordination.k8s.io` Leases for leader election, `create` on TokenReviews and
SubjectAccessReviews for providers that authenticate their own callers, and the
discovery non-resource URLs.

It deliberately withholds `escalate` on ClusterRoles (so RBAC's
escalation-prevention holds the provider to rights it already has, and it cannot
promote itself back), `impersonate`, and any write to `tenancy.kcp.io`.

Stage the change rather than flipping it blind: a provider that defines its own
CRDs in this workspace *and* writes objects of them — the `infrastructure`
provider seeding Templates — needs more than the allow-list grants, and must
stay on `true` until it declares what it needs. Flipping either way replaces the
existing binding (RoleRef is immutable, so the hub deletes and recreates it).

### Rotating

```
POST /api/admin/providers/{name}/credentials/rotate          platform admin
POST /api/orgs/{org}/providers/{name}/credentials/rotate     org admin
```

Both return a fresh `kubeconfig` plus `rotatedAt` and `previousValidUntil`. The
hub issues a **second** token Secret for the same ServiceAccount, points itself
at it (`providers.railgrid.ai/active-token-secret` on the ServiceAccount), and
stamps the previous Secret with `providers.railgrid.ai/delete-after` — 24 hours by
default. The catalog controller deletes retired Secrets once that lapses. The
platform endpoint additionally rewrites the kubeconfig Secret in
`root:railgrid:system:providers`, so in-cluster readers move with it.

During the grace period **both credentials work**: they are tokens for the same
ServiceAccount, so kcp authenticates either as
`system:serviceaccount:default:provider`, and every hub-side check — the
heartbeat's TokenReview included — keys on that identity rather than on which
Secret the token came from. Reinstall the chart with the new kubeconfig before
`previousValidUntil`.

`status.credentialsRotatedAt` on the `CatalogEntry` records the last rotation;
the credential itself is shown once and stored nowhere the hub reads back. There
is no `railgrid` CLI subcommand — use curl:

```bash
curl -sS -X POST -H "Authorization: Bearer $RAILGRID_TOKEN" \
  "$RAILGRID_HUB_URL/api/admin/providers/$NAME/credentials/rotate" \
  | jq -r .kubeconfig > provider-kubeconfig.yaml
```

Rotation is not revocation: it schedules the old credential's death, it does not
hasten it. For a leaked credential, delete the retired Secret in the provider
workspace by hand.

### Re-accepting permission claims

```
POST /api/admin/providers/{name}/claims/reaccept             platform admin
```

Ships the migration AGENTS.md §5.1 demands whenever a provider **adds** a
permission claim. Editing `manifest.yaml`, re-running
`make codegen-<name>-provider` and mirroring the chart's `catalogentry.yaml`
updates the provider-side `APIExport` and nothing else: what the provider is actually allowed to touch in
a tenant's workspace is the claim set on that tenant's own `APIBinding`, which
the Enable flow wrote once and nobody revisits. Deploy code that *requires* the
new claim without this step and every already-enabled tenant 403s, with the
claim visible in the binding's `status.exportPermissionClaims` but absent from
its `spec`.

The endpoint walks every `APIBinding` in the tenant fleet whose
`spec.reference.export` is this provider's export — across all Orgs and all
their workspaces, including bindings that are not `Bound`, since a binding held
out of Bound by a missing claim is exactly the one to fix — and rewrites
`spec.permissionClaims` to the `tenantScoped` claims the provider's
`CatalogEntry` declares today, each `Accepted`.

Two things it will not do:

- **It does not overturn a rejection.** A claim the tenant explicitly set to
  `Rejected` stays `Rejected`. The migration propagates what the provider
  declares; it does not manufacture consent.
- **It does not re-scope a claim.** A claim already on the binding keeps its
  selector; only genuinely new claims take their scope from the export, because
  kcp makes an accepted claim's selector immutable.

Matching is on the export, not the provider name, so an Org self-hosting a
provider of the same name is untouched — migrate that copy by re-Enabling it,
or run the endpoint against the platform copy only. Claims the provider no
longer declares are dropped: the target is the current `CatalogEntry`, not the
union with history.

```bash
curl -sS -X POST -H "Authorization: Bearer $RAILGRID_TOKEN" \
  "$RAILGRID_HUB_URL/api/admin/providers/$NAME/claims/reaccept"
# {"provider":"agents","claims":[...],"updated":12,"unchanged":3,"failed":[]}
```

`updated` counts bindings rewritten, `unchanged` those already correct, and
`failed` names each `{org, workspace, binding, error}` that could not be
migrated. A failing workspace does not abort the run — re-run the endpoint as
the retry; a second pass over a migrated fleet reports everything `unchanged`.
It refuses outright (400) when the provider declares no tenant-scoped claims,
rather than stripping every tenant's grants.

## Security considerations

- **Auth token forwarding** (backend proxy): the user's bearer token is
  forwarded to the provider backend. Operators MUST trust the providers
  they install. Same trust model as installing any cluster operator.
- **Provider→kcp isolation**: provider SAs are scoped to their own
  workspace. Cross-tenant access only via the APIExport mechanism, which
  kcp gates by `permissionClaims`.
- **Provider→provider isolation**: a provider never holds a credential into
  another provider's backend (runtime cluster, DB, internal Service). All
  cross-provider access is through the other provider's published `APIExport`
  resources and its **declared data-plane verbs**, as the tenant user, with the
  owning provider running both gates — see §"Provider isolation". This contains
  blast radius (one owner per backend) and is what makes BYO compute work.
- **A provider does not mint identities.** Where background work needs a
  workspace-local credential, it asks the hub's scoped-identity service
  (`pkg/hub/identity`, client `provider-sdk/identityclient`): TokenRequest-minted,
  TTL'd, `resourceNames`-scoped, garbage-collected with its owning object, and
  refused unless the rule fits one of four clauses — the requester's own group;
  `get` on *named* resources of another provider's group that the tenant has
  bound; `create` on a `{resource}/{verb}` that provider **declares**; or a
  closed platform allowlist. Nothing on the core group is mintable, and no
  foreign write is. Providers therefore hold no `serviceaccounts`,
  `clusterroles` or `clusterrolebindings` permission claims; a claim on those
  types is a contract violation. (kuery and app-studio still carry theirs —
  outstanding debt tracked in
  [roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md).)
- How published apps get their URL and access control (the template-embedded
  access gate + kcp RBAC grants) is documented in
  [Published apps: template-native access](./app-studio-publishing.md).
- **Permission claim gate**: the binding controller refuses any claim not
  marked `tenantScoped`. An override exists
  (`railgrid.ai/accept-untrusted-claims=true`) but is admin-only
  (host-cluster RBAC on the `CatalogEntry` resource).
- **Provider bundles are fully trusted code.** A provider UI is a classic
  script the portal loads into its own document (no iframe, no sandbox). Once
  it runs it can read anything the portal can, including the DOM of other
  mounted providers. Installing a provider is therefore the same trust
  decision as installing a cluster operator. Two controls bound the default
  exposure, neither is a sandbox:
  - **SRI pin.** At registration (and again whenever `spec.version` or the
    heartbeat's `status.reportedVersion` changes, plus a 10-minute resync) the
    catalog reconciler fetches `<spec.ui.url>/main.js` — or reads it from the
    embedded assets of a first-party provider — and records
    `sha384-…` in `CatalogEntry.status.ui.mainJSIntegrity` and on the registry
    record (`pkg/hub/providers/ui_integrity.go`). `/api/providers` exposes it
    as `mainJSIntegrity`; the portal loads the script with `integrity` (and
    no `crossorigin` attribute — the load is same-origin, so SRI applies
    without one and CORS mode would be refused by the UI proxy), so a bundle
    swapped behind the URL after registration is refused by the browser
    until the hub re-admits it.
    Org-owned providers are served over the edge tunnel and never dialled by
    the reconciler; their pin is computed at grant time instead
    (`POST /api/providers/{name}/ui-grant` hashes the bundle through the
    caller's delegated route, `pkg/hub/providers/ui_grant.go`) and returned
    with the bundle URL, so they load pinned too.
  - **Host fetch, no raw token.** The host hands the bundle
    `railgridContext.fetch` (Authorization + tenant headers injected by the host,
    same-origin allow list) instead of the user's id token. `token` is still
    exposed, deprecated, for one release.
- **CSP**: `script-src 'self'` with no `'unsafe-inline'` — the portal ships no
  inline script and an injected one is refused. `frame-src 'self'` plus
  explicitly configured platform-owned frame sources (App Studio preview
  hosts) covers the portal's own iframes; providers do not use frames.
- **Follow-up (L)**: a sandboxed iframe with a postMessage bridge and a
  hub-minted per-provider token, if third-party providers written by parties
  the operator does not trust become a product goal.
- **Internal-only services**: providers should be `ClusterIP`. Hub is the
  only public ingress. Network policies recommended.
- **Heartbeat token**: the heartbeat is authenticated as the provider's own
  service account, verified by TokenReview in the provider's workspace. The
  token is the provider kubeconfig's long-lived legacy SA token (a
  `kubernetes.io/service-account-token` Secret); it stays valid until the
  Secret or SA is deleted, and the hub caches a successful verification for
  less than the heartbeat TTL so revocation takes effect within one liveness
  window.

---

## Phased delivery

| Phase | Scope | Verifiable outcome |
|---|---|---|
| 1 | `CatalogEntry` CRD + catalog controller (workspace + SA + Secret + schema apply) + registry + heartbeat endpoint + backend proxy | An example provider's chart installs, hub provisions everything, provider pod heartbeats, `/services/providers/example/*` reaches the backend |
| 2 | UI proxy + `ProviderFrame.vue` + dynamic routes + providers store + AppLayout nav integration + CSP + dev proxy | A static "hello" provider UI loads inside the portal at `/providers/hello`, side nav shows it, theme + tenant context arrive on `railgridContext` |
| 3 | Catalog controller adds RBAC grant (`ClusterRole` + binding for tenant identity) + `MaximalPermissionPolicy` apply on the provider's APIExport. Portal: EnableDialog + the hub's Enable endpoint creating the `APIBinding` + nav filter to user's APIBindings + validation that bound CRs are reachable through the kcp proxy. | Users can enable/disable from the portal; an `APIBinding` lands in their workspace; provider CRs visible AND readable via `/clusters/{cluster}` kube REST. |
| 4 | Provider SDK + example chart in `providers/quickstart/` | Third party can copy the example and ship a working provider end-to-end |
| 5 | Hardening: RBAC fuzz, cache-bust verification, e2e tests, claim re-acceptance flow on chart upgrade | Ready to declare stable |

## Deferred items

1. **Bound CRs reachable through the kcp proxy** — REQUIRED by end of phase
   3, not optional. Once a tenant workspace has an `APIBinding` to a
   provider's `APIExport`, the bound CRs MUST be reachable via
   `/clusters/{cluster}/apis/{group}/{version}/{resource}` kube REST through
   the hub's kcp proxy ([pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)),
   which forwards to kcp as the caller. This is kcp's own binding semantics,
   so it is expected to work transparently — validate in phase 3 with the
   example provider's `Greeting` CR listing through the proxy
   (`kubectl --server=$HUB/clusters/$CLUSTER get greetings`). If it does
   not, file as a follow-up task, do NOT block phase 1 or 2.
2. **Cross-provider dependencies** — out of scope for v1.
3. **Heartbeat over kcp leases** — possible v2 simplification.
4. **Per-permission-claim UI toggles** — v2.

---

## Phase 1 implementation plan

Phase 1 = the full backend skeleton, no portal changes yet. Verifiable by
installing a stub provider chart and curling
`/services/providers/example/healthz` through the hub.

### What landed (current tree)

Use these as the authoritative source — the Phase 1A skeleton is in
place. The list below is descriptive, not prescriptive.

| Path | Purpose |
|---|---|
| [apis/providers/v1alpha1/types_catalogentry.go](../apis/providers/v1alpha1/types_catalogentry.go) | `CatalogEntry` Go type (admin-only group `providers.railgrid.ai`) |
| [apis/providers/v1alpha1/groupversion_info.go](../apis/providers/v1alpha1/groupversion_info.go) | Scheme registration for the new group |
| [config/crds/providers.railgrid.ai_catalogentries.yaml](../config/crds/providers.railgrid.ai_catalogentries.yaml) | Host-cluster CRD (codegen) |
| [config/kcp/apiresourceschema-catalogentries.providers.railgrid.ai.yaml](../config/kcp/apiresourceschema-catalogentries.providers.railgrid.ai.yaml) | kcp APIResourceSchema (codegen) |
| [config/kcp/apiexport-providers.railgrid.ai.yaml](../config/kcp/apiexport-providers.railgrid.ai.yaml) | Admin-only APIExport (excluded from `core.railgrid.ai` merge) |
| [hack/gen-core-apiexport/main.go](../hack/gen-core-apiexport/main.go) | Excludes `apiexport-providers.railgrid.ai.yaml` from the merged tenant-facing core export |
| [pkg/hub/providers/registry.go](../pkg/hub/providers/registry.go) | In-memory routing table |
| [pkg/hub/providers/proxy.go](../pkg/hub/providers/proxy.go) | `NewUIProxy`, `NewBackendProxy` reverse proxies |
| [pkg/hub/providers/controller.go](../pkg/hub/providers/controller.go) | Catalog reconciler (Phase 1A: URL parse → registry upsert + Ready condition). Phase 1B will add workspace/SA/Secret/schema apply; Phase 3 will add the RBAC `bind`-verb grant + `MaximalPermissionPolicy` apply. |
| [pkg/hub/providers/api.go](../pkg/hub/providers/api.go) | `GET /api/providers` admin-mediated list endpoint backing the portal |
| [pkg/hub/portal_security.go](../pkg/hub/portal_security.go) | `WithPortalSecurityHeaders` middleware (CSP) — applied to both embedded SPA and `--portal-dev-url` proxy |
| [pkg/apiurl/urls.go](../pkg/apiurl/urls.go) | `PathPrefixProvidersUI`, `PathPrefixProvidersProxy` constants |
| [pkg/hub/server.go](../pkg/hub/server.go) | Route registration; second multicluster manager bound to `providers.railgrid.ai` for the catalog controller |
| [pkg/hub/scheme.go](../pkg/hub/scheme.go) | Registers the new providers group |
| [pkg/hub/kcp/bootstrap.go](../pkg/hub/kcp/bootstrap.go) | `ensureProvidersSelfBinding` — APIBinding in `root:railgrid:providers` so catalog entries can live there |
| [providers/quickstart/](../providers/quickstart/) | Reference provider — Go binary, Dockerfile, `manifest.yaml`, README |
| [portal/src/stores/providers.ts](../portal/src/stores/providers.ts) | Pinia store fetching `/api/providers` |
| [portal/src/router/providers.ts](../portal/src/router/providers.ts) | Dynamic `/providers/:name/:rest(.*)*` route registration |
| [portal/src/pages/ProvidersPage.vue](../portal/src/pages/ProvidersPage.vue) | Catalog grid |
| [portal/src/pages/ProviderFrame.vue](../portal/src/pages/ProviderFrame.vue) | Custom-element host: SRI-pinned bundle load, `railgridContext` push (host fetch), `railgrid-navigate` |
| [portal/src/components/AppLayout.vue](../portal/src/components/AppLayout.vue) | `navItems` computed, merges static + provider entries; renders icon URLs |
| [portal/vite.config.ts](../portal/vite.config.ts) | Dev proxy entries for `/api/providers`, `/services/providers`, `/ui/providers` |

### Key code anchors (from current tree)

- Route registration block: [pkg/hub/server.go:307-359](../pkg/hub/server.go#L307-L359)
- Exec-credential kubeconfig pattern (model for provider kubeconfig
  minting): [pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)
- Existing APIExport YAML (template for the new one's permissionClaims):
  [config/kcp/apiexport-railgrid.ai.yaml](../config/kcp/apiexport-railgrid.ai.yaml)
- Bootstrap entry point: `pkg/hub/bootstrap` + invocation around
  [pkg/hub/server.go:280-301](../pkg/hub/server.go#L280-L301)
- kcp embedded FS: [config/kcp/embed.go](../config/kcp/embed.go)
- Static path constants live in `pkg/api/url/` (referenced as `apiurl` in
  `pkg/hub/server.go`)
- Workspace YAML pattern:
  [config/kcp/workspace-providers.yaml](../config/kcp/workspace-providers.yaml)

### Phase 1 verification recipe

1. `make codegen && make build` — clean build.
2. Start the hub against an embedded kcp:
   `./bin/railgrid-hub --embedded-kcp --static-auth-tokens=test:user-default`.
3. Apply a stub `CatalogEntry`:
   ```yaml
   apiVersion: railgrid.ai/v1alpha1
   kind: CatalogEntry
   metadata: { name: hello }
   spec:
     displayName: Hello
     vendor: railgrid
     version: 0.0.1
     serviceAccountNamespace: default
     backend:
       url: http://localhost:8081  # any local HTTP responder
       healthPath: /healthz
     apiExport:
       name: hello.example.com
       schemas:
         - groupResource: greetings.hello.example.com
           body: |
             apiVersion: apis.kcp.io/v1alpha1
             kind: APIResourceSchema
             metadata: { name: v260522-stub.greetings.hello.example.com }
             spec: { ... minimal valid schema ... }
   ```
4. Observe in hub logs:
   - workspace `root:railgrid:providers:hello` created
   - SA `provider` created
   - Secret `railgrid-provider-kubeconfig` written to `default` namespace
   - APIResourceSchema + APIExport applied
   - registry shows `hello` once stub backend returns 200 on `/healthz`
5. `curl -H "Authorization: Bearer test" \
   http://localhost:9443/services/providers/hello/healthz` → reaches the
   stub backend (matches the body it serves).
6. POST a heartbeat with the SA token from the Secret →
   `status.lastHeartbeat` updates.
7. Delete the `CatalogEntry` → registry entry removed, Secret
   cleaned up. (Workspace deletion is a v2 concern — leave it for now,
   note in code as TODO.)

### What phase 1 deliberately does NOT do

- No portal changes.
- No tenant Enable/Disable flow yet — every authenticated user sees every
  installed provider. Phase 3 adds the per-tenant `APIBinding` create from
  the portal and filters the nav.
- No validation of bound CRs through the kcp proxy.
- No Helm example chart yet (phase 4).

---

## Phase 2 implementation plan (portal)

Phase 2 = the full portal wiring. Verifiable by serving a static "hello"
provider UI and seeing its custom element mount inside the portal.

See §"Portal changes" above for the file create/edit lists. Order of
operations:

1. **CSP first** ([pkg/hub/portal_security.go](../pkg/hub/portal_security.go))
   — `script-src 'self'` admits the hub-proxied bundle; there is no
   `'unsafe-inline'`, so the portal itself must ship no inline script. A
   small middleware sets the header on portal responses only.
2. **UI proxy** — `pkg/hub/providers/proxy.go` (already created in phase
   1) gets the `NewUIProxy` handler wired into the router. Existing
   backend proxy stays.
3. **Catalog/bindings REST calls** + **Pinia store** + **route registration helper** —
   landed together; nothing depends on order between them.
4. **App.vue** — await `providersStore.load()` before mounting
   `<router-view />`. Critical for deep-link bootstrapping.
5. **AppLayout.vue** — replace static `navItems` with computed.
6. **ProvidersPage.vue + ProviderFrame.vue** — render the catalog and
   frame.
7. **EnableDialog.vue** — wired only enough to display the claims; the
   actual mutation lands in phase 3 (binding controller).
8. **Provider SDK** — published as workspace package; consumed by the
   example provider in phase 4.

### Phase 2 verification recipe

1. With phase 1 deployed, install a stub `CatalogEntry` with a
   simple HTTP server behind `spec.ui.url` that serves a `main.js`
   registering `<railgrid-provider-quickstart>` (see §"Provider element contract")
   and rendering `<h1>hello provider</h1>` plus whatever `railgridContext`
   it received.
2. Open the portal in a browser. Side nav and `/providers` show the new
   provider immediately — Phase 1A/2 do not gate visibility per tenant.
   Phase 3 adds the Enable/Disable flow and the nav filter.
3. `kubectl get catalogentry hello -o jsonpath='{.status.ui.mainJSIntegrity}'`
   prints a `sha384-…` value and `GET /api/providers` carries it as
   `mainJSIntegrity`.
4. Click it. URL becomes `/providers/hello`. The element mounts; the
   injected `<script id="railgrid-provider-script-hello">` carries
   `integrity` and no `crossorigin` attribute.
5. Open browser devtools → confirm:
   - A request the stub makes through `railgridContext.fetch` to
     `/services/providers/hello/…` arrives at the backend with
     `Authorization` and `X-Railgrid-Tenant` (visible in the backend log);
     one to `/services/providers/other/…` rejects client-side with
     `ProviderFetchDeniedError`.
   - No CSP violations.
   - No CORS errors.
6. Toggle theme in the shell — the host re-pushes `railgridContext` and the
   stub's background flips. (Optional check.)
7. Replace the stub's `main.js` without changing its version → the browser
   refuses the bundle with an integrity error until the hub re-hashes
   (a version change or the 10-minute resync).
8. Reload the deep link `https://railgrid.example.com/ui/#/providers/hello`
   in a fresh tab → still works (proves the store loads before route
   resolution).

### What phase 2 deliberately does NOT do

- Catalog Enable/Disable buttons (UI present, mutation is phase 3).
- Per-claim consent toggles (phase 3 ships only the all-or-nothing
  dialog).
- WebSocket support in the backend proxy (add in phase 5 if needed).
- Example provider chart (phase 4).

---

## Example: a minimal provider

The reference provider is [`providers/quickstart/`](../providers/quickstart/).
There is no `examples/provider-hello/` — it was never written. Structure: one
Go binary serving `/healthz` and `/readyz` plus one data-plane verb
(`POST /clusters/{id}/apis/quickstart.railgrid.ai/v1alpha1/greetings/{name}/greet`,
the custom subresource kcp forwards, gated through
`provider-sdk/dataplane`); one multicluster reconciler using
`railgrid-provider-kubeconfig` to stamp status on the `Greeting` CR it exports,
under `provider-sdk/leaderelection` with readiness from `provider-sdk/vwhealth`;
the Helm chart from §"Provider author experience"; and a portal bundle
registering `<railgrid-provider-quickstart>` plus
`<railgrid-dashboard-tile-quickstart>`, which read and write Greetings with the
portalkit kube client and call the verb through `providerFetch`.

It is small enough to read end to end, and every piece of the contract appears
exactly once. Start from [its README](../providers/quickstart/README.md).
