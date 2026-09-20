# Provider contract remediation — upgrade notes

**Audience:** operators upgrading an existing hub, and tenants who consume
providers through their own RBAC, agents or scripts. Written for the branch
that lands [roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
(20 September 2026). Everything here follows from that plan; the plan explains
*why*, this page only says *what to do*.

The contract itself is in [providers.md](./providers.md) and
[provider-connectivity-contract.md](./provider-connectivity-contract.md).

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

### 4. Composition consent is recorded at Enable

Providers that manage another provider's kinds (App Studio composing
infrastructure `instances` and code `repositories`, kuery reading edges
`kubernetesclusters`) no longer
carry first-party permission claims for it. They declare it as
`CatalogEntry.spec.dependencies[].composes`, and a workspace or org admin
accepts it in the Enable dialog. The hub records the acceptance as a
`compose:<group>/<resource>` capability in the workspace's Grant and mints the
provider's scoped identity against it (identity policy clause E).

Workspaces that enabled kuery or App Studio before this release have **no
recorded acceptance**. Their reconcilers are refused with
`composition_not_granted` until an admin re-enables the provider in that
workspace and accepts the composition. The portal shows the pending consent on
the provider's card; the hub API reports it under `compositions.pending` on
`GET .../providers/enabled`.

## Tenant-visible changes

| Area | Before | After | What a tenant does |
|---|---|---|---|
| Provider actions RBAC (code, planner, App Studio commits) | `invoke` on `{resource}/{action}` | `create` on `{resource}/{action}` | Update ClusterRoles that grant `invoke`; the hub-managed roles are regenerated |
| kuery REST | `POST /api/query`, `GET /api/edges`, `/api/status` (tenant from a header) | `POST /dataplane/clusters/{clusterID}/savedviews/{name}/run` as the caller; edge set comes from the workspace's Engagements | Create a `SavedView` and run it; the body may carry `{"input":{"query":…}}` to override the saved query |
| agents REST | ~40 `/api/*` CRUD routes | `Run` and the other kinds over the APIBinding; data-plane verbs on the grammar | Use the kube API for CRUD; scripts that posted to `/api/*` get 404 |
| App Studio REST | ~95 `/api/projects/*` routes | Project/Session/Thread kinds over the APIBinding; verbs on the grammar | Same as agents |
| planner, databricks, factory | provider-specific `/api/*` routes | verbs on the grammar behind the two gates | Same |
| Any provider | hub `/services/providers/{name}/api/*` | refused (404) by `provider-sdk/serve` | Move to the data-plane or action grammar |

The general rule: a tenant's REST call to a provider is always
`/{root}/clusters/{clusterID}/{resource}/{name}/{verb}` (data plane) or
`/actions/{resource}/{name}/{action}/{version}`, authorized as the caller by a
real GET of the object plus a SelfSubjectAccessReview for `create` on
`{resource}/{verb}`. The verbs a provider declares are listed on its
CatalogEntry under `spec.dataPlane.verbs`.

## BYO (organization-owned) providers

Nothing changes for how a BYO provider is registered. Two things get easier:

- **No `identityHash` in claims.** Cross-provider access goes through hub-minted
  scoped identities, so an org running its own copy of edges or infrastructure
  no longer breaks App Studio or kuery, which used to pin the platform export's
  identity hash.
- **Two shipped objects, no Go claim lists.** A provider ships a hand-written
  CatalogEntry (`manifest.yaml`) and an APIExport generated by
  `provider-sdk/cmd/apiexportgen` from its apigen output plus the manifest's
  claims; `init` applies both from `RAILGRID_KCP_DIR`. `make
  verify-provider-contract` refuses a drift between the two.

## Verifying an upgraded install

- No tenant binding to `code`, `agents` or `app-studio` still reports
  `PermissionClaimsValid=False`; those that do have not been re-enabled yet.
- No workspace still has the `railgrid:provider:edges:edges-proxy` ClusterRole.
- Every edge shows `status.connected: true` after its agent restarted on the
  new binary.
- `GET .../providers/enabled` shows an empty `compositions.pending` in
  workspaces that use kuery or App Studio.
