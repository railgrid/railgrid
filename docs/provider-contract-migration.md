# Provider contract remediation — upgrade notes

**Audience:** operators upgrading an existing hub, and tenants who consume
providers through their own RBAC, agents or scripts. Written for the branch
that lands [roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
(20 September 2026). Everything here follows from that plan; the plan explains
*why*, this page only says *what to do*.

The contract itself is in [providers.md](./providers.md) and
[provider-connectivity-contract.md](./provider-connectivity-contract.md).

> **Superseded in part (2026-09-25).** The upgrade steps below still describe
> the 20 September branch, where a verb was reachable both on the hub-proxied
> grammar `/services/providers/{name}/{dataplane,actions}/clusters/{id}/…` and
> as a kcp custom subresource. The hub-proxied grammar has since been removed
> outright: a tenant's call to a provider verb is **only**
> `/clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}` (an action's
> `/v<n>` is not in the path; a component is `?component=`), authorized by kcp
> RBAC on the `{resource}/{verb}` subresource — spelled `verbs: ["*"]` in a
> hand-written ClusterRole, because kcp maps the HTTP method onto the RBAC
> verb — and the provider's own review of `get` on the parent. Scripts, agents
> and CI that post to `/services/providers/{name}/dataplane/…` or `…/actions/…`
> get a 404 from the hub. Browser WebSockets carry the bearer as the
> `base64url.bearer.authorization.k8s.io.<token>` subprotocol; the edges
> `ticket` verb is gone. An external kcp needs `--request-timeout` raised
> (the embedded shard uses one hour) for streaming verbs. See
> [provider-connectivity-contract.md](./provider-connectivity-contract.md)
> §"Known divergences" item 2.
>
> **Also superseded (2026-09-25): the CatalogEntry's own shape.** `CatalogEntrySpec`
> is now four sections — `export` (the APIExport's `name` plus `resources[]`,
> each `{name, apiVersion, kind}` carrying the `verbs[]` and `actions[]` served
> **on it**), `requires` (ONE list keyed by API group, replacing both
> `spec.apiExport.permissionClaims` and `spec.dependencies[].composes[]`),
> `serving` (`ui`/`backend`/`selfHosting`) and `hub`
> (`access`/`assistantSkills`). The field paths named below are the old ones and
> are kept as written; the full mapping is in
> [roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
> §"Status update 2026-09-25 — the CatalogEntry contract is four sections".
>
> **One extra operator step.** The change is in place on `v1alpha1` with no
> conversion and no dual-read, so an old-shape CatalogEntry does not validate
> and its provider drops out of the hub registry. **Every provider must
> re-apply its CatalogEntry** — `helm upgrade` for a chart-installed provider,
> a re-apply of `manifest.yaml` for the `install-provider-<name>` path. Nothing
> rewrites stored objects and `schemaDigest` values are unchanged, so this is a
> redeploy rather than a data migration. Tenant `APIBinding`s are unaffected:
> `spec.requires` generates the same claims the old two lists did, so the
> re-accept step in §4 below is still needed only where a provider actually
> widened or narrowed what it requires.

---

## Before you upgrade

Nothing on this page has to happen before the new hub starts. Every step below
is safe to run after the rollout, and the hub reports the state that needs it.

## Operator: one-time actions after the rollout

### 1. Narrowed `secrets` claims need a Disable/Enable per workspace

Every provider that still claims core `secrets` now claims them **label-scoped**
to `railgrid.ai/owner: <provider>` (`defaultSelector` on the APIExport, enforced
by kcp's claim labeler and virtual-workspace admission). A tenant APIBinding
accepted *before* the upgrade keeps the old `matchAll` selector, because kcp
makes an accepted claim's selector immutable. Such a binding keeps working with
its original, wider scope and shows `PermissionClaimsValid=False` as a warning
until it is recreated.

The only way to narrow it is for the workspace to **disable and re-enable** the
provider (`code`, `agents` or `app-studio`), which recreates the binding with
the current selector. Nothing forces this: the wider scope is what the
workspace already consented to, and the hub does not overturn consent.

The admin endpoint

```
POST /api/admin/providers/{name}/claims/reaccept
```

exists for the other half of a claim rollout: it propagates a provider's
current claim **set** (new claims added, dropped claims removed, explicit
rejections preserved) to every tenant binding. It deliberately keeps each
existing claim's selector and identity hash, so it does not narrow an old
`secrets` claim. Its response counts `updated` / `unchanged` / `failed`
bindings and names each failed `(org, workspace, binding)`; a failure never
aborts the run, and re-running is the retry.

### 2. Delete the orphaned edges-proxy grant

Enable used to create a `railgrid:provider:edges:edges-proxy` ClusterRole and
ClusterRoleBinding in every workspace that had edges enabled. That grant was
retired with the edges data plane's per-verb gates; nothing reads it and no
code path removes it. Delete both objects in each affected workspace:

```
kubectl --context <workspace> delete clusterrolebinding railgrid:provider:edges:edges-proxy
kubectl --context <workspace> delete clusterrole        railgrid:provider:edges:edges-proxy
```

Leaving them is harmless but grants a subject nothing uses.

### 3. Edge agents must be upgraded

The agent-ingress route changed shape to the shared data-plane grammar,
`/agent/clusters/{clusterID}/{resource}/{name}/proxy`, with **no compatibility
window**. An agent built before this release gets HTTP 400 from the hub and
never connects. Upgrade the `railgrid` binary on every edge (or roll the
in-cluster agent Deployment); the existing join token or saved credential is
reused, no re-enrolment is needed.

An agent that was pointed at a rebuilt hub, or whose edge was deleted and
recreated under the same name, now discards a saved credential that does not
match its target and re-enrols with the join token it is started with. See
[edges-agent-credentials.md](./edges-agent-credentials.md).

### 4. Composition is a permission claim, and needs a re-Enable per workspace

Providers that manage another provider's kinds (App Studio composing
infrastructure `instances` and code `repositories`/`repositorycommits`, kuery
watching edges `kubernetesclusters`) declare it once, as
`CatalogEntry.spec.dependencies[].composes`. From that one declaration the
build now generates an **identity-agnostic permission claim** on the composing
provider's own APIExport — no `identityHash`, so kcp resolves it per consumer
workspace against whatever export that workspace bound — and that claim is how
the provider reads and writes the composed kind.

A tenant `APIBinding` created before this release **does not acquire the new
claim on its own**: the hub only ever creates a binding, and never rewrites an
existing one. Until the claim is on the binding and `Accepted`, the composing
provider sees none of those objects and the symptom is "enabled, but it never
builds anything". Two ways to fix it, per workspace:

```
POST /api/admin/providers/{name}/claims/reaccept
```

propagates the provider's current claim **set** to every tenant binding — new
claims added, dropped claims removed, explicit rejections preserved — which is
what a composition-derived claim needs. It preserves each existing claim's
selector and identity hash, so it does not also narrow an old `secrets` claim
(step 1). Alternatively a workspace admin disables and re-enables the
provider, which recreates the binding with the current claim set.

The `Grant` side is unchanged and still separate: a workspace or org admin
accepts the composition in the Enable dialog, the hub records it as a
`compose:<group>/<resource>` capability, and identity policy clause E checks
it. What clause E mints is now only what a claim cannot carry — a call to
another provider's data-plane verb or action, `use` on an MCP server, App
Studio's per-workspace dependency watch. Workspaces that enabled kuery or App
Studio before compositions existed have no recorded acceptance, and those
calls are refused with `composition_not_granted` until an admin re-enables and
accepts. The portal shows the pending consent on the provider's card; the hub
API reports it under `compositions.pending` on `GET .../providers/enabled`.

### 5. The platform's `PermissionClaimPolicy` is applied at bootstrap

kcp admits a permission claim with no `identityHash` only when a cluster-scoped
`PermissionClaimPolicy` (group `admin.kcp.io`) pairs the **claimer** — the API
group the claiming APIExport itself exports, not the provider's name — with the
claimed group. The platform ships one such object,
`config/kcp/permissionclaimpolicy.yaml`, generated from the same `composes[]`
entries by `hack/generate-permission-claim-policy.mjs` and verified by
`make verify-provider-contract`.

Operator-facing consequences:

- **The hub applies it through the admin virtual workspace**, at
  `<kcp>/services/admin/clusters/root`, using its kcp admin credentials, and
  only when discovery reports that kcp serves `admin.kcp.io/v1alpha1`. A kcp
  without that API is skipped, not failed — but then no unpinned claim is
  admitted either, so cross-provider composition does not work on it.
- **Naming a pairing reserves both groups.** Once a group appears in the
  policy, as claimer or as claimed, only the subjects listed in
  `spec.providers` may export it — *including against cluster admins*. Adding
  an out-of-tree provider whose exported group already appears there means
  its export must be applied by one of those subjects.
- **The subject is the provider ServiceAccount**
  `system:serviceaccount:default:provider` — the identity the hub mints in
  each `root:railgrid:providers:<name>` workspace, and the one every
  provider's `init` applies its APIExport with. **Known caveat:** a bare
  ServiceAccount username is *logical-cluster-scoped* in kcp, so this subject
  also matches a `default/provider` ServiceAccount a tenant creates in their
  own workspace. kcp's disambiguated form
  `system:kcp:serviceaccount:{cluster}:{ns}:{name}` cannot be written
  statically, because `{cluster}` is a runtime-assigned logical cluster id.
  The TODO is recorded in the generated file's header; narrowing it is a kcp-
  side question.
- **An out-of-tree provider's `composes[]` edges are not in the committed
  file** unless the generator was run with its tree:
  `make permission-claim-policy EXTERNAL_PROVIDERS_DIR=../providers`. A claim
  with no matching policy rule is refused by kcp admission at `init`.

### 6. Data-plane verb names changed — this is a wire break

Every declared verb and action is now published as a kcp **custom
subresource** named `{resource}/{verb}` on the provider's APIExport, and kcp
holds `spec.resources[].name` to
`^[a-z][-a-z0-9]*[a-z0-9](/[a-z][-a-z0-9]*[a-z0-9])?$`, refusing `status` and
`scale` outright. Verbs that did not fit were renamed, with **no compatibility
window**:

- **underscores are gone** — `mint_clone_token` is now `mint-clone-token`, and
  so on for every underscored verb and action id;
- **`instances/status` is now `instances/runtime-status`**, because `status`
  belongs to the object's own shape.

A caller on an old name gets a 404 from the provider and, on the kube path, a
resource kcp does not serve. This affects:

- **scripts, agents and CI** that post to
  `/services/providers/{name}/{dataplane|actions}/clusters/{id}/…/{old-verb}`;
- **ClusterRoles a tenant wrote by hand** that grant `create` on
  `{resource}/{old-verb}` — hub-managed roles are regenerated, hand-written
  ones are not;
- **action ids** pinned in a workload's configuration.

The verbs a provider serves are listed on its CatalogEntry
(`spec.dataPlane.verbs`, `spec.actions`) and readable from `GET /api/providers`;
on the kube path they are ordinary subresources, so `kubectl get --raw` and
discovery show them.

## Tenant-visible changes

| Area | Before | After | What a tenant does |
|---|---|---|---|
| Provider actions RBAC (code, planner, App Studio commits) | `invoke` on `{resource}/{action}` | `create` on `{resource}/{action}` | Update ClusterRoles that grant `invoke`; the hub-managed roles are regenerated |
| kuery REST | `POST /api/query`, `GET /api/edges`, `/api/status` (tenant from a header) | `POST /dataplane/clusters/{clusterID}/savedviews/{name}/run` as the caller; edge set comes from the workspace's Engagements | Create a `SavedView` and run it; the body may carry `{"input":{"query":…}}` to override the saved query |
| agents REST | ~40 `/api/*` CRUD routes | `Run` and the other kinds over the APIBinding; data-plane verbs on the grammar | Use the kube API for CRUD; scripts that posted to `/api/*` get 404 |
| App Studio REST | ~95 `/api/projects/*` routes | Project/Session/Thread kinds over the APIBinding; verbs on the grammar | Same as agents |
| planner, databricks, factory | provider-specific `/api/*` routes | verbs on the grammar behind the two gates | Same |
| Any provider | hub `/services/providers/{name}/api/*` | refused (404) by `provider-sdk/serve` | Move to the data-plane or action grammar |
| Any provider | verb names with `_`, and `instances/status` | hyphens only; `instances/runtime-status` | Update every hardcoded verb, action id and hand-written ClusterRole — **no compatibility window** (see step 6) |

The general rule: a tenant's REST call to a provider is
`/{root}/clusters/{clusterID}/{resource}/{name}/{verb}` (data plane) or
`/actions/{resource}/{name}/{action}/{version}`, authorized as the caller by a
real GET of the object plus a SelfSubjectAccessReview for `create` on
`{resource}/{verb}`. The same verb is now **also** an ordinary API path,
`/apis/{group}/{version}/{resource}/{name}/{verb}`, which kcp authorizes as
RBAC on the `{resource}/{verb}` noun and the serving shard reverse-proxies to
the provider; `kubectl` discovers it. Both reach the same handler. The verbs a
provider declares are listed on its CatalogEntry under `spec.dataPlane.verbs`
and `spec.actions`.

## BYO (organization-owned) providers

Nothing changes for how a BYO provider is registered. Two things get easier:

- **No `identityHash` in claims.** A claim on another provider's first-party
  group is left identity-agnostic, and kcp resolves it per consumer workspace
  against whatever export that workspace bound — so an org running its own
  copy of edges or infrastructure no longer breaks App Studio or kuery, which
  used to pin the platform export's identity hash. That pin, and everything
  that served it (`RAILGRID_IDENTITY_HASHES`, the admin identities view,
  `identityFor` self-hosting values, stale-claim detection), is gone.
  What an org-owned provider *does* need is a policy rule: run
  `make permission-claim-policy EXTERNAL_PROVIDERS_DIR=<its tree>` so its
  `composes[]` edges reach `config/kcp/permissionclaimpolicy.yaml`.
- **Two shipped objects, no Go claim lists.** A provider ships a hand-written
  CatalogEntry (`manifest.yaml`) and an APIExport generated by
  `provider-sdk/cmd/apiexportgen` from its apigen output plus the manifest's
  claims, its composed kinds and its declared verbs; `init` applies both from
  `RAILGRID_KCP_DIR`, plus the `DataPlaneEndpointSlice` the subresource
  entries route through. `make verify-provider-contract` refuses a drift
  between the two, and refuses a verb name kcp would reject
  (`subresource-name`).

## Verifying an upgraded install

- No tenant binding to `code`, `agents` or `app-studio` still reports
  `PermissionClaimsValid=False`; those that do have not been re-enabled yet.
- No workspace still has the `railgrid:provider:edges:edges-proxy` ClusterRole.
- `kubectl get permissionclaimpolicy railgrid -o yaml` (as kcp admin, against
  `<kcp>/services/admin/clusters/root`) shows the pairings the committed
  `config/kcp/permissionclaimpolicy.yaml` carries; if the resource does not
  exist, this kcp does not serve `admin.kcp.io` and unpinned claims will be
  refused.
- Every tenant binding to a composing provider (`app-studio`, `kuery`) carries
  the composition-derived claims as `Accepted`; one that does not has not been
  re-accepted or re-enabled yet.
- No caller is still using an underscored verb name or `instances/status`.
- Every edge shows `status.connected: true` after its agent restarted on the
  new binary.
- `GET .../providers/enabled` shows an empty `compositions.pending` in
  workspaces that use kuery or App Studio.
