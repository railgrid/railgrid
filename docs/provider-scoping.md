# Provider scoping — Global, Org, Personal

**Status:** Design draft
**Owner:** TBD
**Last updated:** 2026-05-30
**Reads as a delta on:** [providers.md](./providers.md), [organizations.md](./organizations.md)

---

## Why this doc exists

Three questions this doc answers:

1. **How does a provider become available to the whole platform?**
2. **How does an Org publish a provider only its own Workspaces can use?**
3. **How does a single user register a provider just for themselves?**

What it does **not** redo:

- The Enable mechanic (`APIBinding` in the consuming workspace,
  permission-claim auto-accept rules, bind-verb grants) is pinned in
  [providers.md](./providers.md) decisions #9 and #10. Don't relitigate.
- The Org / Workspace / Membership tree is in
  [organizations.md](./organizations.md). This doc assumes it exists.

Today every `CatalogEntry` is platform-global and visible to every
authenticated caller — see
[NewListHandler in api.go](../pkg/hub/providers/api.go#L108). The
filtering and per-workspace gating below is the work.

---

## Decisions pinned

Don't re-litigate; the doc body assumes these.

| # | Decision | Rationale |
|---|---|---|
| P-1 | **CatalogEntry identity = UUID.** `metadata.name` is a server-assigned UUID; `spec.displayName` and `spec.slug` (URL-safe display string) are separate. Two Orgs can each register a "vault" provider with no collision. | Mirrors O-1. Removes the "name collision in URL path" problem entirely; the proxy's `splitProviderPath` resolves by `{entry-uuid}` not slug. |
| P-2 | **Enforcement of "no provider APIBindings in Org workspaces" = api-proxy mediation.** Tenants never receive a kubeconfig that reaches an Org workspace; all Org-workspace operations (CatalogEntry CRUD, Membership CRUD, child Workspace create) go through hub REST endpoints. The kedge kcp proxy ([pkg/server/proxy/proxy.go](../pkg/server/proxy/proxy.go)) refuses to issue exec-credentials for paths under `root:kedge:orgs:{uuid}` (without a child `:{ws-uuid}` segment). Tenants *physically cannot* `kubectl apply` an APIBinding there. | Strongest possible enforcement (network-level, not RBAC) and zero kcp changes. See [organizations.md](./organizations.md) §"Org workspaces are hub-mediated only." |
| P-3 | **`bind` verb scope = per-Org `ClusterRole`, controller-maintained.** A controller watches CatalogEntries + Memberships and keeps `kedge:org:{uuid}:bind` up-to-date with `resourceNames` = all that Org's APIExports + the Global ones, subjects = every Org Membership. | Standing privilege but matches the Org-membership model; auditable via `kubectl get clusterrole kedge:org:*:bind`. |
| P-4 | **Builtin providers require explicit Enable too** (not auto-bound in new Workspaces). Membership APIs are the only thing the `workspace` WorkspaceType auto-binds. | Consistent rule: every provider, builtin or third-party, Enable is a deliberate Workspace action. Avoids the "this just showed up, what is it?" surprise on new Workspaces. |
| P-5 | **Permission claim acceptance = per-Workspace Enable** for all scopes (Global, Org, Personal). Same flow as today (`providers.md` #9 auto-accepts `tenantScoped` claims at Enable time). Org admin's CatalogEntry create does *not* pre-accept on behalf of members. | One consistent flow; the trust decision stays with the workspace that gains the binding. |
| P-6 | **No `requireWorkspaceContext` migration flag.** Workspace headers (`X-Kedge-Org`, `X-Kedge-Workspace`) are **required** from day one. Follows from clean-slate migration (O-2). | No legacy users to keep working; the fallback flag was solving a problem we don't have. |
| P-7 | **Disable = kcp handles the cascade; hub gates on confirm.** `DELETE .../providers/{uuid}/enable` returns 409 + a preview body (counts of CRs that will be affected, per kind) unless `?confirm=true` is passed. Hub doesn't try to delete CRs itself — kcp's APIBinding deletion semantics own that. | Fat-finger protection without re-implementing what kcp already does. |
| P-8 | **Breaking CatalogEntry fields are immutable via CEL.** `spec.apiExport.schemas`, `spec.apiExport.permissionClaims`, `spec.apiExport.path`, `spec.apiExport.name`, and `spec.backend.url` carry a `+kubebuilder:validation:XValidation` rule of `self == oldSelf`. Display fields (`displayName`, `iconURL`, `category`, `version`) stay mutable. Changing a locked field requires deleting the CatalogEntry and creating a new one. | Kcp/CRD-layer enforcement; hub doesn't need to inspect updates. Producers expressing "this is the same provider" by reusing the slug get to control breaking-change UX explicitly. |
| P-9 | **Org soft-delete grace behavior (during the 30-day O-13 window):** the deleting Org's CatalogEntries are hidden from `/api/providers` everywhere; the per-Org `bind` ClusterRole (P-3) is removed so no *new* `APIBinding` can be created; existing `APIBindings` keep working until cascade-day. Undelete restores listings + RBAC. | Honors deletion intent without breaking running workloads mid-flight. |
| P-10 | **ServiceAccounts can Enable iff `role=admin`** (mirroring human Memberships). Member-role SAs cannot. Permission claims are auto-accepted on the SA's behalf since SAs can't see a dialog — Org admin pre-authorizes the trust by giving the SA admin role. | Lets CI pipelines bootstrap a Workspace end-to-end; preserves the human-reviewed default for casual automation. |
| P-11 | **Slug uniqueness is bi-directional.** Global adds are rejected if the slug is already in use by any Org-Private entry (response lists the conflicting Org UUIDs). Org-Private adds are rejected if the slug is in use Globally (today's rule). | Symmetric, no silent shadowing. The platform admin sees a clear list at add time. |
| P-12 | **Hub probes CatalogEntry backend URLs at register time.** A controller GETs `spec.backend.url` (and `spec.ui.url`) once at register, then again per heartbeat. Result lands on `status.conditions: BackendReachable`. Probe failure does **not** block registration — the CR is accepted with the warning condition so dev workflows ("register first, deploy backend later") still work; the portal surfaces the condition prominently. | Catches the localhost/private-URL footgun without locking out legitimate "stage the entry first" use cases. |

---

## Three scopes

| Scope | CatalogEntry lives in | Visible to (which workspaces) | Who can register |
|---|---|---|---|
| **Global** | `root:kedge:providers` | every Workspace on the platform | platform admin (Helm install) |
| **Org** | `root:kedge:orgs:{org-uuid}` | every Workspace under that Org | Org admin |
| **Personal** | `root:kedge:orgs:{personal-org-uuid}` (the Org marked `spec.personal: true` on the User) | every Workspace under the personal Org | the user (sole admin of their personal Org) |

Personal collapses to "Org-scoped in the personal Org" — same code
path, same admission rules. It's only a distinct *user-facing concept*
because the personal Org is implicit; mechanically there is no third
scope to maintain.

---

## Storage = WHERE the CatalogEntry sits

The `CatalogEntry` CRD is identical at all three scopes. The
workspace it lives in determines:

- **Visibility** (which Workspaces' users see it in `/api/providers`).
- **Authority to register** (kcp RBAC in that workspace gates writes,
  honoring `Organization.spec.catalogEntryCreation` per O-7).
- **Lifecycle** (deleting the parent Org / Workspace cascades).

Per P-1, `metadata.name` is a server-assigned UUID. The CRD gains
two display fields and keeps scope as a derived hint. Per P-8, the
breaking-change fields are immutable via CEL.

```go
type CatalogEntrySpec struct {
    // DisplayName is the human label rendered in the catalog card.
    // Mutable.
    DisplayName string `json:"displayName"`

    // Slug is a URL-safe short identifier the proxy uses in
    //   /ui/providers/{slug}/...   and
    //   /services/providers/{slug}/...
    // Required, must match ^[a-z0-9][a-z0-9-]{0,62}$. Uniqueness is
    // enforced bi-directionally between Global and Org-Private scopes
    // (P-11) at write time.
    // Immutable (P-8) — rename = delete + recreate.
    //
    // +kubebuilder:validation:Pattern="^[a-z0-9][a-z0-9-]{0,62}$"
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="slug is immutable"
    Slug string `json:"slug"`

    // Scope is informational. The catalog controller derives it from
    // the workspace this CatalogEntry lives in (root:kedge:providers
    // → Global, root:kedge:orgs:{uuid} → Org or Personal depending on
    // Organization.spec.personal). Overwritten on reconcile.
    //
    // +kubebuilder:validation:Enum=Global;Org;Personal
    // +optional
    Scope string `json:"scope,omitempty"`

    // Backend, UI, APIExport: existing structures from providers.md.
    // Per P-8, the following sub-fields are immutable:
    //   - spec.backend.url
    //   - spec.apiExport.path
    //   - spec.apiExport.name
    //   - spec.apiExport.schemas (the list itself; per-element edits
    //     blocked by CEL on each element's identifying tuple)
    //   - spec.apiExport.permissionClaims
    // Display sub-fields (spec.ui.iconURL, spec.version) stay mutable.
    Backend   *Backend          `json:"backend,omitempty"`
    UI        *UI               `json:"ui,omitempty"`
    APIExport *APIExportConfig  `json:"apiExport,omitempty"`
}

type CatalogEntryStatus struct {
    // Conditions[BackendReachable] is set by the URL probe controller
    // (P-12): True if the most recent hub-side GET to spec.backend.url
    // succeeded, False (with reason) if not. Drives the "unreachable"
    // warning in the portal.
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

No separate `ownerOrg` field — the workspace path encodes it.

---

## Enable is per-Workspace, not per-Org

Per [organizations.md](./organizations.md): all tenant work lives in
**child Workspaces**, not in the Org workspace. That carries straight
through here:

- A `CatalogEntry` in `root:kedge:orgs:{org-uuid}` says "this provider
  is *available* to Workspaces under that Org."
- The `APIBinding` that actually Enables the provider lives in a
  specific Workspace — `root:kedge:orgs:{org-uuid}:{ws-uuid}`.
- Sibling Workspaces in the same Org are *eligible* to Enable the
  same provider but are not bound until each one explicitly creates
  its own `APIBinding`.

Two Workspaces under the same Org therefore have **independent enable
state**. The catalog list is per-workspace, not per-org.

This also means: a provider's UI/backend proxy gate checks the
*workspace*, not the *org*. The same user, switching tabs from one
workspace to another, can see one with the provider Enabled and one
without.

---

## "How do I become a provider for…?"

| You want… | You do… | Who has to approve |
|---|---|---|
| The whole platform | Submit a Helm chart that installs a `CatalogEntry` into `root:kedge:providers` on the host cluster | Platform admin |
| Your Org only | As Org admin, `POST /api/orgs/{org}/catalog` (creates a `CatalogEntry` in `root:kedge:orgs:{org}`) pointing at your backend / chart you've installed | You (Org admin) |
| Just yourself | Same as above, in your **personal Org** (the auto-bootstrapped Org with `spec.personal: true`) | You |
| Promote your Org provider to platform-wide | (v2) `POST /api/orgs/{org}/catalog/{name}/submit` opens a request to the platform admin to clone the entry into `root:kedge:providers` | Platform admin reviews + accepts |

Org-scoped and Personal-scoped providers don't get the same hub-side
guarantees as Global ones — no host-cluster Helm chart, no managed
ServiceAccount, no kcp provider workspace bootstrap. They are
**bring-your-own-URL**: useful for in-house tools, dev environments,
gated betas, or a teammate sharing a localhost tunnel before the
provider goes Public.

---

## Catalog listing API

Today: `GET /api/providers` returns the whole registry. After:

```
GET /api/providers
  X-Kedge-Org:        7f3a91d2-...     (required, Org UUID)
  X-Kedge-Workspace:  9c4b8e1f-...     (required, Workspace UUID)
```

Resolution, in order:

1. Tenant middleware verifies the caller's Membership in
   `{org-uuid}/{ws-uuid}` (per [organizations.md](./organizations.md)
   §Tenant middleware). Else 403.
2. Catalog list fetches CatalogEntries from:
   - `root:kedge:providers` (Global)
   - `root:kedge:orgs:{org-uuid}` (Org)
3. Per entry, the list computes `enabled` by checking for an
   `APIBinding` in `root:kedge:orgs:{org-uuid}:{ws-uuid}` whose
   `reference.export.path` matches the provider's APIExport. One List
   call total, not one per provider.
4. Response shape gains:

```go
type providerDTO struct {
    // ... existing fields ...

    // Scope is "Global" | "Org" | "Personal" — for the portal badge.
    Scope string `json:"scope"`

    // OwnerOrg is the owning Org UUID for Scope=Org/Personal entries,
    // empty for Global. The portal renders "by {displayName}" by
    // resolving the UUID via the caller's UserMembershipIndex.
    OwnerOrg            string `json:"ownerOrg,omitempty"`
    OwnerOrgDisplayName string `json:"ownerOrgDisplayName,omitempty"`

    // Enabled is true when the caller's *workspace* has an APIBinding
    // to this provider's APIExport. Drives Enable-button vs. side-nav
    // rendering. Builtin providers are always enabled.
    Enabled bool `json:"enabled"`
}
```

Per P-4, builtin providers (`Builtin: true`,
[builtin.go](../pkg/hub/providers/builtin.go)) go through the same
visibility + enable computation as everything else. They are Global by
construction, but each Workspace must explicitly Enable them like any
third-party provider. The portal hint that a CatalogEntry is a builtin
(`builtin: true` on the DTO) drives a different "Enable" affordance —
no permission-claim dialog, one-click — but never an auto-bind.

---

## Proxy gating

`/services/providers/{slug}` and `/ui/providers/{slug}` need the active
workspace from the tenant middleware. The slug (P-1) is resolved to a
CatalogEntry UUID by looking it up in:

1. The Global catalog (`root:kedge:providers`), then
2. The active Org's catalog (`root:kedge:orgs:{org-uuid}`).

First match wins. This means a Global slug shadows a same-named Org
slug — document this; portal validation rejects Org slug creates that
collide with a Global one.

- **Asset paths** (anything under `/ui/providers/{slug}` ending in a
  file extension — see `isAssetPath` in
  [proxy.go](../pkg/hub/providers/proxy.go#L263)) **stay open.** The
  portal needs to load the catalog card icon before the user has
  picked any org/workspace.
- **Non-asset UI paths and all backend paths** check:
  1. Resolve slug → CatalogEntry (UUID). 404 if it doesn't exist in
     either visible scope.
  2. Does the workspace have an `APIBinding` for the provider? Else
     403 with a body the portal recognizes (`{"reason":"not-enabled",
     "enableUrl":"..."}`) and renders an Enable prompt. Per P-4 this
     applies to builtins too — there is no bypass.

Per P-11, slug uniqueness is enforced bi-directionally at write time:
both Global and Org-Private CatalogEntry creates are rejected if the
slug is already in use in the *other* scope. The error body lists the
conflicting entries (UUIDs + Org UUIDs) so the registering party knows
who to talk to.

---

## Enable / Disable a provider (Workspace-level)

Enable: `POST /api/orgs/{org-uuid}/workspaces/{ws-uuid}/providers/{entry-uuid}/enable`

1. Tenant middleware verifies caller has `role: admin` Membership in
   the Workspace (or admin in the parent Org per O-15). For
   ServiceAccounts: only `role: admin` SAs may call this (P-10).
2. Hub creates the `APIBinding` in the Workspace (or returns 200 if
   one already exists with matching identity).
3. Permission claims are auto-accepted per P-5 for humans; for SAs
   (P-10) the acceptance is implicit — the Org admin pre-authorized
   the trust by issuing the SA an admin role.

Disable: `DELETE /api/orgs/{org-uuid}/workspaces/{ws-uuid}/providers/{entry-uuid}/enable`

Per P-7, the hub gates fat-finger Disables but **does not** cascade
CRs itself:

1. Without `?confirm=true`: 409 with a preview body listing the
   provider's CRD kinds and the count of objects of each kind that
   exist in this Workspace. Example body:
   ```json
   {
     "reason": "confirm-required",
     "affected": [
       {"kind": "Secret", "group": "vault.example.com", "count": 12},
       {"kind": "AuthBackend", "group": "vault.example.com", "count": 1}
     ]
   }
   ```
2. With `?confirm=true`: the hub deletes the `APIBinding`. From here,
   kcp handles the cascade per the APIBinding deletion semantics — the
   CRs become inaccessible / NotReady / get garbage-collected per kcp's
   own rules. The hub does not try to delete them itself.
3. If the SA / user calling Disable lacks admin role: 403.

---

## Endpoints

```
POST   /api/orgs/{org-uuid}/catalog                                    create Org/Personal CatalogEntry (UUID assigned)
GET    /api/orgs/{org-uuid}/catalog                                    list this Org's own CatalogEntries (Org-admin UI)
PUT    /api/orgs/{org-uuid}/catalog/{entry-uuid}                       update an Org CatalogEntry (displayName, URLs, claims)
DELETE /api/orgs/{org-uuid}/catalog/{entry-uuid}                       delete it

POST   /api/orgs/{org-uuid}/workspaces/{ws-uuid}/providers/{entry-uuid}/enable   create APIBinding in the workspace
DELETE /api/orgs/{org-uuid}/workspaces/{ws-uuid}/providers/{entry-uuid}/enable   delete the APIBinding

GET    /api/providers                                                  (existing) — now requires X-Kedge-Org + X-Kedge-Workspace
```

All identifiers in paths are UUIDs; display names are returned in
response bodies, never accepted in request paths.

The Enable endpoints are convenience wrappers — per
[providers.md](./providers.md) #10 the portal may keep calling kcp
directly to create the `APIBinding`. The wrappers exist for CLI use
and to centralize permission-claim acceptance.

`POST /api/orgs/{org}/catalog` validates:

- Caller has a Membership in the Org and meets the role threshold
  declared by `Organization.spec.catalogEntryCreation` (O-7): default
  `members` means any Org member can publish; `admin` restricts to
  Org admins.
- `spec.apiExport` (if set) points at an `APIExport` the Org is
  allowed to reference — for v1, only APIExports inside the same Org
  workspace. (Cross-org APIExport references see §Explicitly deferred
  to v2.)

---

## Migration story

Per O-2 (clean slate), there is no production data to migrate. Existing
dev/test CatalogEntries in `root:kedge:providers` are already Global
and stay where they are. There are no production Users whose old
single-workspace UX needs preserving, so per P-6 the workspace context
headers are **required** on `/api/providers` and the proxies from day
one — no fallback flag.

Concretely, on first deploy after this lands:

1. The bootstrap controller creates a personal Org + default Workspace
   for every existing User CR.
2. The portal pins the User's personal Org UUID as the default
   `X-Kedge-Org` (from `User.status.personalOrg`, per
   [organizations.md](./organizations.md)) and the default Workspace
   UUID as `X-Kedge-Workspace`.
3. The previous `root:kedge:users:{userId}` workspaces are deleted by
   a one-shot cleanup job (they were dev data per O-2).

---

## Implementation order

Depends on all [organizations.md](./organizations.md) PRs having
landed (without them, workspace context, tenant middleware, and the
kcp-proxy Org gate from O-10 don't exist). Then in this doc's order:

1. **CatalogEntry CRD bump.** `metadata.name` becomes UUID,
   `spec.slug` + `spec.scope` (derived) land, plus the CEL
   immutability rules from P-8. Catalog controller reconciles `scope`
   from the workspace path. Bi-directional slug-uniqueness admission
   from P-11 lands here too.
2. **Multi-source registry.** Catalog controller watches both sources
   (`root:kedge:providers`, `root:kedge:orgs:*`) and feeds the
   in-memory `Registry`. Keys become `(scope, ownerOrg, uuid)`; slug
   resolution lookups happen at request time, not at registry-write
   time. The registry filters out CatalogEntries whose owning Org has
   `status.deletionRequestedAt` set (P-9).
3. **Per-Org `bind` ClusterRole controller (P-3).** Watches
   CatalogEntries + Memberships, maintains `kedge:org:{uuid}:bind`.
   Also reconciles P-9: when an Org enters soft-delete, the controller
   removes its bind ClusterRole; undelete restores it.
4. **Backend URL probe controller (P-12).** Periodically GETs
   `spec.backend.url` / `spec.ui.url` for every CatalogEntry, writes
   `status.conditions[BackendReachable]`.
5. **Visibility filter + per-workspace `enabled`** in `NewListHandler`,
   reading workspace context from the middleware (mandatory per P-6).
6. **Proxy gating** with the slug→UUID resolution above. Builtins
   gated like everything else (P-4).
7. **`POST/DELETE /api/orgs/{org-uuid}/catalog`** for Org-admin
   CatalogEntry management. Honors `Organization.spec.catalogEntryCreation`
   (O-7).
8. **`POST/DELETE …/enable`** convenience wrappers. P-7's confirm
   gate lives here. Triggers the per-Workspace claim-acceptance dialog
   flow per P-5; auto-accepts for SAs per P-10.

Steps 1-2 are read-side and can land together. Steps 3-4 add
controller writes but no user-facing change. Steps 5-8 turn on the
user-facing surface.

Note: the kcp-proxy Org-workspace gate that P-2 depends on is owned by
organizations.md PR #5 (per O-10), not by this doc. This doc consumes
that gate; it does not ship it.

---

## Open questions

Open after this round of decisions:

- **Deletion of an Org CatalogEntry with live Workspace APIBindings.**
  P-7 covers the *Workspace-side* Disable flow but doesn't address what
  happens when an Org admin deletes the source CatalogEntry while child
  Workspaces still have APIBindings to it. Per
  [providers.md](./providers.md) line 298 the bindings go NotReady.
  Decide: warn + proceed (default), or refuse the delete until bindings
  are removed. Before shipping the Org CatalogEntry delete endpoint.
- **Catalog controller startup cost.** Listing CatalogEntries across
  every Org workspace is O(orgs). Fine for hundreds, awkward at tens
  of thousands. Pre-aggregated index workspace
  (`root:kedge:catalog-index` mirroring all entries) is the obvious
  scale fix; defer until we measure pain.
- **Empty-state UX details.** Per Q11 the portal renders a "Suggested
  for you" rail (mcp / edges / server-edges) for fresh Workspaces with
  no APIBindings. Open: which exact providers go in the rail, what
  order, and whether the list is hardcoded in the portal or driven by
  a `tags: [suggested]` annotation on the CatalogEntry. Portal team
  detail.

### Explicitly deferred to v2

- **Cross-org provider sharing.** Org A publishes a Private CatalogEntry
  that Org B wants to consume. Requires cross-org `bind` grants on the
  APIExport. Add `spec.sharedWith []OrgUUID` to CatalogEntry when this
  lands.
- **Org → Global promotion workflow.** A `CatalogPromotion` CR + admin
  review.
- **Provider deprecation flow.** `spec.deprecated` + `deprecationMessage`
  to hide from new Enables while keeping existing bindings working.
  v1 ships hard-delete only; operators coordinate out-of-band.
- **Managed backend tunneling for Personal CatalogEntries.** P-12's
  unreachable-URL warning is the v1 answer; v2 may ship a managed
  reverse-tunnel so a user can register `http://localhost:8080` and
  have it work from the hub.
- **Org-Private builtins.** Builtins ship in the hub binary, so an Org
  can't add one. Closest equivalent stays "Org-Private CatalogEntry
  with stable backend."

### Verification tasks (not decisions, but blocking)

- Confirm slug→UUID resolution can be cached safely against the
  catalog controller's watch — the registry already caches the full
  entry; the slug index is just a derived map keyed by
  `(active-org-uuid, slug)`.
- Confirm the CRD CEL rules in P-8 actually reject the targeted edits
  on the kcp-side admission path (kcp's CRD validation should respect
  standard XValidation rules; verify with a quick spike).
