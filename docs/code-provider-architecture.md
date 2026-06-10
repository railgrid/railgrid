# Code provider: git repository management

Status: **Design proposal, scaffold in progress.**
Author: 2026-06-09
Related: `providers/infrastructure/` (the standalone-provider pattern this is modeled on), `pkg/hub/providers/` (CatalogEntry provisioning), `docs/providers.md`, `docs/infrastructure-architecture.md`.

## Summary

The **`code`** provider manages source-code repositories the same way the `infrastructure`
provider manages application templates. The motivation: when we provision infrastructure we
also need somewhere for the code to live — so a dedicated provider should, on demand, "give me
a repo, give me a deploy key", driven primarily by **MCP** and a **Vue portal UI**.

It is multi-backend ("sub-providers"): **GitHub first**, GitLab/others later, behind one
pluggable backend interface. v1 scope: **repo create / list / delete** and **access management
(deploy keys + collaborators)**.

The provider is a **standalone deployed service** discovered purely via a `CatalogEntry` —
identical packaging to `infrastructure`, but simpler: its CRDs are **tenant-authored** (not
platform-owned Templates), so there is no CachedResource, no virtual storage, and no kro.

## 1. Credential model (the central decision)

The open question was *which account creates the repositories*. Decision: **PAT-first, behind
a pluggable `Connection` abstraction**, so per-user/per-org **BYO-GitHub** can arrive later
without changing the consumer-facing API.

- `Connection.spec.type` is an enum: `pat | github-app | oauth`. v1 implements `pat`.
- A user onboards in the portal by pasting a Personal Access Token (stored as a `Secret`) and
  creating a `Connection`. Repos are created under the org/account that token controls
  (`Connection.spec.owner`).
- **Future:** `github-app` (the per-user/per-org install flow — short-lived, fine-grained,
  revocable installation tokens) and `oauth` slot in behind the same `Connection.spec.type` +
  a new backend implementation. Consumers (Repository/DeployKey/Collaborator, MCP, portal) are
  unaffected.

## 2. API surface

Group **`code.kedge.faros.sh`**. All CRDs are **cluster-scoped**, **tenant-authored** (created
in the tenant's own workspace via the APIBinding), with a `status` subresource and standard
conditions/finalizers.

| Kind | Spec (key fields) | Status |
|---|---|---|
| **Connection** | `provider` (github), `type` (pat\|github-app\|oauth), `owner`, `secretRef`, `baseURL` | `Validated` condition, `login`, `scopes` |
| **Repository** | `connectionRef`, `name`, `owner?`, `visibility`, `description`, `defaultBranch`, `autoInit` | `htmlURL`, `cloneURL`, `sshURL`, `repoID`, conditions |
| **DeployKey** | `repositoryRef`, `publicKey?` (BYO) or generate, `readOnly` | `keyID`, `secretRef` (generated private key) |
| **Collaborator** | `repositoryRef`, `username`, `permission` (pull\|push\|admin) | conditions (e.g. `InvitationPending`) |

DeployKey and Collaborator are **separate CRDs** (not arrays on Repository): one
controller-per-kind with finalizers and per-item status, avoiding racy read-modify-write of a
parent object.

## 3. Pluggable backend (sub-providers)

A `GitBackend` interface + a `Registry` copied from
[`providers/infrastructure/backend/interface.go`](https://github.com/faroshq/faros-kedge/blob/main/providers/infrastructure/backend/interface.go):

```
GitBackend {
  Name() string
  ValidateConnection(ctx, *Connection, creds) (login string, scopes []string, err error)
  EnsureRepository / DeleteRepository
  EnsureDeployKey  / DeleteDeployKey
  EnsureCollaborator / RemoveCollaborator
}
```

All ensure/delete methods are **idempotent**. Unlike infra's `Backend`, there is **no
`Run(ctx, vwConfig)`** — for `code`, the controllers own the watch loop and the backend is a
pure remote-API dispatcher. v1 implementation: `backend/github` using `google/go-github` +
`oauth2.StaticTokenSource`.

## 4. Controllers — multicluster

`code`'s CRs live across **every tenant workspace**, so the controller manager uses the
**multicluster** shape (the hub's wiring in `pkg/hub/server.go`), *not* infra's single-cluster
manager:

- `apiexport.New(cfg, "code.providers.kedge.faros.sh", …)` → `mcmanager.New(...)`.
- Each reconciler: `mcbuilder.ControllerManagedBy(mgr).For(&Repository{})`; inside
  `Reconcile(ctx, mcreconcile.Request)` it calls
  `mgr.GetCluster(ctx, req.ClusterName).GetClient()` to act in the tenant workspace.
- Four reconcilers: connection, repository, deploykey, collaborator.

**Credential resolution:** controllers read the PAT `Secret` via their own VW-scoped per-cluster
client (authorized by the `secrets` permission claim), **not** via a caller bearer token. The
caller-token factory is used only by MCP/portal paths. DeployKey writes the generated private
key as a `Secret` in the tenant workspace with an `ownerReference` to the DeployKey CR — the GC
seam the `infrastructure` provider mounts to clone/push.

## 5. MCP + portal

- **MCP tools are CRD-native** (copy the infra pattern): they create/list/delete CRs in the
  tenant workspace *as the caller*; the controller does the real work. Tools:
  `list/get/create/delete_repository`, `add/list_deploy_key`, `add/remove_collaborator`,
  `list/validate_connection`, `create_connection` (references an existing Secret by name).
  **Pasting a PAT is a portal action, never an MCP tool** — the secret is never transported
  through MCP.
- **Portal:** `<kedge-provider-code>` custom element with nav children **Connections** and
  **Repositories**. Views: Connections (paste PAT → Secret + Connection, show
  `Validated`/login/scopes), Repositories (list/create/delete), RepoDetail (deploy keys +
  collaborators).

## 6. Hub integration & manifest

**No hub code change.** A standalone provider is discovered purely by applying its
`manifest.yaml` `CatalogEntry`; the hub's `CatalogReconciler` + `provisioner` create the
sub-workspace, apply the schemas, mint the SA + kubeconfig.

Manifest specifics (the corrections vs infra):

- `apiExport.permissionClaims`: `secrets` with verbs `[get, list, watch, create, update,
  patch, delete]`, `tenantScoped: true` (write verbs are needed for the DeployKey private-key
  Secret; infra only needed read).
- `apiExport.schemas`: **NON-empty** — 4 inline `APIResourceSchema` bodies, applied by the hub
  with `storage: {crd: {}}`. Each body's `metadata.name` MUST follow the immutable
  content-versioned format `vYYMMDD-hash.<resource>.code.kedge.faros.sh` (required by the
  provisioner's `splitSchemaName`).

## 7. Staged delivery

- **PR A — scaffold:** new Go module `providers/code/` (added to `go.work`); API types + CRDs;
  `GitBackend` interface + stub backend; multicluster controller-manager wiring with no-op
  reconciler skeletons; manifest (4 inline schemas, widened secrets claim); Helm chart; portal
  shell. Builds and registers against the hub; the stub flips a Connection to `Validated=true`.
- **PR B — GitHub backend:** `backend/github` (validate, ensure/delete repository) + Connection
  and Repository controllers + PAT resolution. Resolve the APIExportEndpointSlice open item.
- **PR C — access + MCP + portal:** deploy keys + collaborators (controllers + backend
  methods), real MCP tools, portal views.
- **Later:** GitLab backend; `github-app`/`oauth` credential types (per-user UI onboarding).

## 8. Open item

Confirm whether the hub provisioner auto-creates an `APIExportEndpointSlice` for provider
APIExports, or whether `code`'s `init` must create one for its multicluster manager to discover
tenant clusters (infra created one explicitly). If auto-created, `code` may not need an `init`
subcommand at all.
