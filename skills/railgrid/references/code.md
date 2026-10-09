# Code provider reference (git repositories)

## 1. What it is

The code provider is a declarative control plane over **GitHub**. It hosts
no git server: there is no railgrid clone URL, no `git-receive-pack`, and no
git smart-HTTP. You declare `Connection`, `Repository`, `DeployKey`,
`Collaborator` and `PullRequest` CRs in your workspace; controllers reconcile
them into real GitHub state with the GitHub API. `status.cloneURL` and
`status.sshURL` are GitHub's own URLs. Clone and push with GitHub credentials.

HTTP surface of the provider itself: `/healthz`, `/readyz` (readiness is the
APIExport virtual workspace being reachable **and** watched; the hub surfaces
a failing one on `GET /api/providers` as `ready: false` with
`readinessReason: BackendUnhealthy`), `/mcp`, `/mcp/sse`,
`/oauth/github/{config,start,callback}`, the embedded portal, and the verb
path kcp forwards custom subresources to:
`/clusters/{clusterID}/apis/code.railgrid.ai/v1alpha1/{resource}/{name}/{verb}`
(section 5). There is no `/api/*`. CRUD is kubectl or kube REST through the
hub's `/clusters/<cluster>`, or the MCP tools.

## 2. CRDs (`code.railgrid.ai/v1alpha1`, all cluster-scoped)

| Kind | shortName | Purpose |
|---|---|---|
| `Connection` | `gconn` | A GitHub account or org binding: `provider: github`, `type`, `owner`, `secretRef {name, namespace default "default", key default "token"}`, `baseURL` for GHES. Types: `pat` (the Secret holds the token); `oauth` (what the portal's "Connect with GitHub" writes: Secret keys `token`, `refreshToken`, `expiry`; an expiring 8 h token is refreshed on every credential read); `github-app` (Secret keys `appID`, `installationID`, `privateKey` — one PEM RSA key ≥ 2048 bits; the provider signs an App JWT and mints installation tokens; kubectl-authored, no portal flow). Status: `login`, `scopes[]`, condition `Validated`. Deleting the Connection garbage-collects the Secret the portal wrote. |
| `Repository` | `grepo` | `connectionRef` (required), `name` (repo name on GitHub), `owner` override, `visibility private\|public\|internal`, `description`, `defaultBranch`, `autoInit`. Status: `repoID`, `htmlURL`, `cloneURL`, `sshURL`, condition `Ready`. Adopts an existing GitHub repo of that name; creates it on 404. Annotation `code.railgrid.ai/existing-only: "true"` registers a repo whose lifecycle is outside railgrid: it is never created, and deleting the CR never deletes it. Annotation `code.railgrid.ai/create-only: "true"` (App Studio sets it) pins `status.repoID`: a missing or replaced upstream repo fails closed instead of being adopted by name or recreated. Otherwise **deleting the CR deletes the GitHub repo** (needs `delete_repo` scope). |
| `RepositoryCommit` | `gcommit` | Durable record of a commit made through railgrid: `repositoryRef`, `branch`, `message` (≤ 512 chars), `source.bundleRef {name, digest}`. Status: `phase Pending\|Running\|Succeeded\|Failed`, `startedAt`, `completedAt`, `branch`, `commitSHA`, `commitURL`, `source {digest, size, fileCount}`, `files[{path, size, digest, delete}]` (≤ 500). File contents never live in the CR. Label `code.railgrid.ai/repository=<repo>`. Rate-limited commits stay `Running` with Ready reason `RateLimited` (see below). |
| `RepositoryCheckout` | `gcheckout` | Transient: `repositoryRef`, `ref`; status `phase`, `ref`, `commitSHA`, `bundleRef`, `skipped[]` (≤ 100). Created and deleted by the `checkout_repository` tool. Annotation `code.railgrid.ai/binary-encoding: base64` opts into binaries. |
| `RepositoryBuildStatus` | `gbuildstatus` | Transient: `repositoryRef`, `workflowFileName` (required), `ref`, `action status\|rerun`, `maxLogLines` (1–1000). Status: `phase`, `run {found, runID, htmlURL, headSHA, status, conclusion, jobs[{name, status, conclusion, failureLog}]}`, `dispatched`. Created and deleted by `build_status` / `rebuild`. |
| `PullRequest` | `gpr` | One PR as a resource, for a coordinator that stages snapshots with `stage-snapshot`: `repositoryRef`, `branch`, `base`, `title` (≤ 256), `body` (≤ 16 KiB), `desiredHead {commit, baseCommit, tree, bundleRef, message}`, `observeInterval` (default 5m, floor 30s). Status: `phase Pending\|Open\|Merged\|Closed\|Failed`, `number`, `url`, `head`, `state`, `merged`, `mergeCommit`, `mergedAt`, `merger`, `reviews[]` (newest 64), `comments[]` (newest 128, `commentsTruncated`), `lastObserved`, conditions `Ready` and `HeadApplied`. Code never merges a PR. |
| `DeployKey` | `gkey` | `repositoryRef`, `title`, `publicKey` (empty → ed25519 generated), `readOnly`. Status `keyID`, `secretRef` (Secret key `ssh-privatekey`, owned by the CR). |
| `Collaborator` | `gcollab` | `repositoryRef`, `username`, `permission pull\|push\|admin` (default `pull`). Status `invitationID`, condition `InvitationPending`. |
| `Package` | `gpkg` | Read-only, crawled from the host every 2 minutes by default (`CODE_PACKAGE_CRAWL_INTERVAL`; every 30 s for 10 min after a commit succeeds): `packageName`, `type` (`container\|docker\|npm\|maven\|rubygems\|nuget`), `visibility`, `htmlURL`, `versionCount`, `imageRepository`, `versions[{digest, tags[], createdAt}]` (≤ 100, newest first), `lastSyncTime`. Label `code.railgrid.ai/repository=<repo>`; owned by its Repository. This is how App Studio finds `sha-<commit>` images. |

Minimal setup:

```yaml
apiVersion: v1
kind: Secret
metadata: { name: gh-token, namespace: default }
stringData: { token: ghp_xxx }
---
apiVersion: code.railgrid.ai/v1alpha1
kind: Connection
metadata: { name: github-default }
spec:
  provider: github
  type: pat
  owner: my-org-or-user
  secretRef: { name: gh-token }
---
apiVersion: code.railgrid.ai/v1alpha1
kind: Repository
metadata: { name: my-service }
spec:
  connectionRef: github-default
  name: my-service
  visibility: private
  autoInit: true
```

PAT scopes (classic): `repo`, `workflow`, `delete_repo`, `read:org`,
`admin:public_key`, `read:packages` — the same list the OAuth flow requests
by default. `delete_repo` is only needed to delete provider-created repos,
`admin:public_key` for deploy keys, `read:org` for org repos, `read:packages`
for the Packages panel and crawl. Without `workflow`, GitHub refuses commits
touching `.github/workflows/`.

**Rate limits vs scopes.** Rate limits are
classified first — primary (`X-RateLimit` reset + 1 s) and secondary/abuse
(`Retry-After`, default 1 min) — and always read
`github: rate limited, resets in <dur> (at <RFC3339>): <detail>` (the detail
is GitHub's text, e.g. `API rate limit exceeded for user ID …`). A remaining
403 reads `github: forbidden — token lacks the required scope (403): …` and
really is a scope problem; 401 is `github: credential rejected (401): …`.

A `RepositoryCommit` that hits a rate limit stays `Running`, Ready
`False`/`RateLimited` with `GitHub rate limit; retrying in <N>s`, keeps its
bundle, and is retried at the reset (the `Railgrid-RepositoryCommit:` trailer
in the commit message lets the backend find a commit that already landed, so
a retry never double-commits). It is marked `Failed` only when the retry
would fall more than 15 minutes after `status.startedAt`. Repository,
DeployKey, Collaborator and Connection reconciles wait the same way with
reason `RateLimited`.

The limit is **per token**. `gh api rate_limit` reporting 5000/5000 says
nothing about the PAT in the `Connection`, because the `gh` CLI holds a
different token for the same user. Each failed attempt leaves a
`RepositoryCommit` in phase `Failed`, so a long run of failed commits with
identical messages is the signature of a retry loop. The Connection token is
shared by every project's commits, checkouts, build-status checks and the
package crawl.

## 3. Cloning and pushing from a laptop

```bash
kubectl get repository my-service -o jsonpath='{.status.cloneURL}'   # https://github.com/org/my-service.git
git clone https://<user>:<gh-token>@github.com/org/my-service.git      # GitHub credentials
# or a railgrid-generated deploy key:
kubectl get deploykey ci-key -o jsonpath='{.status.secretRef.name}'
kubectl get secret <that> -n default -o jsonpath='{.data.ssh-privatekey}' | base64 -d > id_ed25519 && chmod 600 id_ed25519
GIT_SSH_COMMAND="ssh -i $PWD/id_ed25519" git clone git@github.com:org/my-service.git
```

`railgrid get-token` is unrelated to git; it is the kubectl OIDC exec plugin.

**Promotability caveat.** App Studio resolves builds from `RepositoryCommit`
CRs. A commit created by `git push` has no `RepositoryCommit`, so App Studio
cannot select it for promotion. Commit through `commit_files` when the
change must be promotable through App Studio — from a clone,
`railgrid commit <repositoryRef>` does that for your local commits
([cli.md](cli.md)):

- Refuses a dirty tree (`uncommitted changes; run 'git add -A && git commit' first`)
  and a HEAD that is not ahead of `<remote>/<branch>` (`HEAD does not contain
  origin/main (it moved upstream); run 'git rebase origin/main' first`).
- Sends every file that differs between `<remote>/<branch>` and HEAD
  (deletions as `deletePaths`) through `code__commit_files`; the message is
  the local commit subjects, first as title, rest as bullets, truncated to
  512 characters. Symlinks and submodules are refused; a newly executable
  file only warns (the mode may not be preserved, which fails the final tree
  check).
- Non-UTF-8 files go base64 (25 MiB each, 48 MiB per commit) only if the
  tool's `inputSchema` declares `files.items.properties.encoding`; otherwise
  `<paths>: binary file(s) not supported: the hub's code provider doesn't
  support binary files yet …`.
- On `Succeeded` it fetches, checks `<remote>/<branch>` has exactly HEAD's
  tree, and `git reset --hard`s the local branch onto it; the SHA is printed
  on stdout. Any other phase: `commit not confirmed (phase=…)` with the
  `kubectl get repositorycommits.code.railgrid.ai -l code.railgrid.ai/repository=<ref>`
  hint.

**Package pickup.** A `RepositoryCommit` moving to `Succeeded` shortens the
crawl: for the 10 minutes after its `completedAt` the repository is crawled
every 30 s and its GHCR `container` listings and versions bypass the shared
listing cache. Other ecosystems stay cached; outside the window the crawl is
every 2 min.

## 4. MCP tools (`code__*` on the aggregate)

Server name `railgrid-code`. Every tool runs as the caller (its bearer plus
the workspace's kcp cluster ID, forwarded by the hub aggregate). Tenant
identity comes from the bearer; never ask the user for a tenant path. A
provider that is down is not federated, so the aggregate answers
`unknown tool "code__…"`.

| Tool | Input | Output / notes |
|---|---|---|
| `list_connections` | none | `{connections:[{name,provider,owner,login,validated}]}` (read-only) |
| `list_repositories` | none | `{repositories:[{name,connection,repo,visibility,htmlURL,ready}]}` (read-only) |
| `create_connection` | `name`, `owner`, `secretName`, `provider?`, `secretNamespace?`, `secretKey?`, `baseURL?` | Binds an existing Secret as a `type: pat` Connection; the token never transits MCP. `{name,kind,created}`; `Connection "<n>" already exists` on reuse |
| `create_repository` | `name`, `connectionRef`, `repo?` (default `name`), `owner?`, `visibility?`, `description?`, `defaultBranch?`, `autoInit?` (default true) | `{name,kind,created}`; `Repository "<n>" already exists` on reuse |
| `delete_repository` | `name` | **Destructive: deletes the GitHub repo** (unless `existing-only`). Idempotent, `{deleted:true}` even if already gone |
| `commit_files` | `repositoryRef`, `message?` (≤ 512 chars incl. body; default `Update generated application files`), `branch?` (default the Repository's `defaultBranch`, then `main`), `files[{path,content,encoding?}]`, `deletePaths[]` | Stores a bundle, creates a `RepositoryCommit`, waits ≤ 75 s, returns `{repositoryRef,name,phase,bundleRef,bundleDigest,commitSHA,commitURL,branch,files,deletedPaths}`. Uses the GitHub Git Data API; no clone. Annotated destructive. Limits and errors below |
| `checkout_repository` | `repositoryRef`, `ref?`, `binaryEncoding?` (`base64`) | Returns one JSON **text** block `{repositoryRef,name,phase,ref,commitSHA,files[{path,content,encoding?}],skipped[]}`; there is no `outputSchema`/`structuredContent` (parse `content[0].text`). Waits ≤ 75 s (`RepositoryCheckout "<n>" did not complete in time`). Caps below |
| `build_status` | `repositoryRef`, `workflowFileName?` (default `build.yaml`; a `.github/workflows/<file>` path is reduced to its basename), `ref?` (commit SHA), `maxLogLines?` (200) | Latest run for that workflow plus per-job conclusions and failure log tails. `build.yaml` is what the shipped App Studio scaffolds use (the template's `development.build.workflowPath`). Output is flat: `{repositoryRef, found, runID, htmlURL, headSHA, status, conclusion, jobs[{name,status,conclusion,failureLog}]}` (not the CRD's `status.run{…}`). Waits ≤ 75 s |
| `rebuild` | `repositoryRef`, `workflowFileName?` (default `build.yaml`), `ref?` (branch) | `workflow_dispatch`; returns `{repositoryRef, dispatched}`. Not idempotent |
| `add_deploy_key` | `name`, `repositoryRef`, `title?`, `publicKey?`, `readOnly?` | Omit `publicKey` to generate |
| `add_collaborator` | `name`, `repositoryRef`, `username`, `permission?` | |
| `remove_collaborator` | `name` | Destructive; revokes the grant, cancels a pending invitation. Idempotent |

Provider-direct endpoint `https://<hub>/services/providers/code/mcp` exists
but the MCP SDK's host guard can 403 it behind the hub proxy; use the
aggregate.

### `commit_files` details

- `files[].encoding`: `utf-8` (default, or omitted) for text; `base64`
  (RFC 4648 standard, padded, no line breaks, strictly decoded) for binary.
  Anything else: `file "<p>": unsupported encoding "<e>": use "utf-8" or "base64"`;
  bad base64: `file "<p>" has invalid base64 content: …`.
  Clients send base64 only when the tool's `inputSchema` declares
  `files.items.properties.encoding`.
- Limits, on decoded bytes: 2 MiB per text file, 25 MiB per binary file,
  48 MiB and 500 files per commit.
  `file "<p>" is too large: N > M bytes`, `too many files: N > 500`,
  `bundle is too large: N > M bytes`.
- Message checked before the bundle is written:
  `commit message is N characters; the limit is 512 — shorten the body`.
- Binary files are uploaded as git blobs with a 3-minute per-request timeout
  (default GitHub timeout 30 s); checkout downloads of blobs > 1 MiB use the
  same long timeout.
- Result phase: `Succeeded` → ok; `Failed` →
  `RepositoryCommit "<n>" failed: <condition message>`. Still running after
  75 s is a **tool error**:
  - rate limited: `RepositoryCommit "<n>" is queued behind a GitHub rate limit (<detail>); the provider retries it until <RFC3339>, then marks it Failed. The files are not committed yet: watch RepositoryCommit "<n>" for phase Succeeded before relying on them`
  - otherwise: `RepositoryCommit "<n>" did not finish within the 1m15s wait (phase <p>); the files may not be committed yet: watch RepositoryCommit "<n>" for phase Succeeded or Failed`
- Bundles are deleted once consumed (or when the commit fails); a sweeper
  removes bundles untouched for 24 h (hourly, and at startup).

### `checkout_repository` caps

| Mode | Per file | Total | Files |
|---|---|---|---|
| default (text only; binaries listed in `skipped`) | 256 KiB | 16 MiB | 500 |
| `binaryEncoding: "base64"` | 256 KiB text, 25 MiB binary | 48 MiB | 500 |

Skipped entries carry a reason suffix: ` (binary)`, ` (file too large)`,
` (total-size cap)`, ` (file-count cap)`. A caller that did not opt in never
receives an encoded file (it is listed as `<path> (binary)`). Bad value:
`unsupported binaryEncoding "<v>": use "base64" or omit it`. A failed
checkout is `RepositoryCheckout "<n>" failed: <condition message>`.

## 5. Verbs and actions (kcp custom subresources)

Everything a non-MCP caller invokes is a custom subresource on the
`code.providers.railgrid.ai` APIExport, POSTed on whichever kcp front door the
caller holds a credential for — for a user, the hub's `/clusters/<cluster>`:

```
POST $HUB/clusters/<cluster>/apis/code.railgrid.ai/v1alpha1/repositories/<name>/<action>
POST $HUB/clusters/<cluster>/apis/code.railgrid.ai/v1alpha1/connections/<name>/mint-registry-token
Authorization: Bearer <token>
{"input": {...}}
```

kcp authorizes the `repositories/<action>` (or `connections/<action>`)
subresource with ordinary RBAC, forwards with the caller stamped, and the
provider runs a SubjectAccessReview for `get` on the parent before acting as
itself. There is no bearer on the provider side and no `X-Railgrid-*` header.
The action's `/v1` is its catalog id, **not** a path segment. The response is
the shared envelope: `requestID`, provider, action/version, `resourceRef`, and
exactly one of `result` or `error {code, message, retryable}`.

Every repository action input except `commit` carries `repository`
(canonical `owner/name`), `repositoryUID` and `connectionUID`; the provider
pins them against what it reads, so a Repository or Connection deleted and
recreated under the same name fails closed.

| Coordinate | Input (beyond the three pins) | Result / notes |
|---|---|---|
| `repositories/branches` v1 | `page?` (1–10000) | `{branches[] (≤ 50), nextPage}` |
| `repositories/branch-head` v1 | `branch` | `{head}` |
| `repositories/find-pull-request` v1 | `head`, `base`, `commit` (40-hex) | the PR object, or `null` |
| `repositories/pull-request` v1 | `number` | the PR object |
| `repositories/create-pull-request` v1 | `head`, `base`, `commit`, `title` (≤ 256), `body?` (≤ 32 KiB) | idempotency `none`: a failed response may have created the PR; inspect before retrying |
| `repositories/update-pull-request` v1 | `number`, `head`, `base`, `commit`, `title`, `body?` | |
| `repositories/feedback` v1 | `number`, `commit` | reviews, inline comments, check annotations (≤ 2000 items) |
| `repositories/comments` v1 | `number`, `page` (1–20) | |
| `repositories/add-comment` v1 | `number`, `body` | idempotency `none` |
| `repositories/reply-to-review` v1 | `number`, `parentCommentID`, `body` | idempotency `none` |
| `repositories/stage-snapshot` (verb) | a git bundle: `baseCommit`, `commit`, `tree`, base64 `bundle` (25 MiB decoded, 36 MiB on the wire) | `bundleRef` (64-hex), scoped to cluster + Repository UID + Connection UID + the caller; expires after an hour; ≤ 16 handles / 256 MiB per tenant |
| `repositories/prepare-snapshot` v1 | `bundleRef` | `{commit, tree}` after verifying the single-parent snapshot |
| `repositories/publish-snapshot` v1 | `bundleRef`, `branch`, `expectedHead` (40-hex or empty = branch must not exist) | atomic expected-head lease; read the branch after a lost response |
| `repositories/stage-commit-bundle` (verb) | a file list (48 MiB decoded, 68 MiB on the wire, 500 files) | `{bundleRef, bundleDigest}` for `commit` |
| `repositories/commit` v1 | `repositoryUID`, `message?` (≤ 512), `branch?`, and either `files[{path, content, encoding? utf-8\|base64, delete?}]` (≤ 1 MiB input) or `bundleRef` + `bundleDigest` | `{commit: {name, uid}}` — **async**: watch that `RepositoryCommit` for the SHA. Same executor as `commit_files` |
| `repositories/mint-clone-token` v1 | — | `{remoteURL, username, token, expiresAt?, scoped}`: read-only clone credential for this one repo. `scoped: true` only for a `github-app` Connection (`contents:read`, ~1 h); a PAT/OAuth token is returned as is with `scoped: false`; no token → `token: ""` (public repos clone anonymously) |
| `connections/mint-registry-token` v1 | `connectionUID` | `{registry, username, token, expiresAt?, scoped}`: image-pull credential; `scoped: true` only for `github-app` (`packages:read`, ~1 h) |

`stage-snapshot` and `stage-commit-bundle` are plain verbs rather than
catalogued actions (the catalog caps `maxInputBytes` at 1 MiB), so App Studio
cannot grant them through a project binding; a caller needs `create` on that
coordinate in workspace RBAC. The MCP tools and the portal never use this
path; they drive the CRs directly.

## 6. GitHub OAuth (portal only)

`GET /services/providers/code/oauth/github/config` → `{enabled, startURL?, scopes?}`
(`scopes` is one comma-joined string). The portal opens `startURL` in a popup;
the callback page posts `{token, refreshToken?, expiry?, login, scopes}` to the
portal, which writes the Secret and a `type: oauth` `Connection`. The token
never transits kcp or the hub. Configured by the operator with
`GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`,
`GITHUB_OAUTH_REDIRECT_URL` (must end in `/callback`),
`GITHUB_OAUTH_PORTAL_ORIGIN`, `GITHUB_OAUTH_SCOPES` (default
`repo,workflow,delete_repo,read:org,admin:public_key,read:packages`).
`enabled: false` → the "Connect with GitHub" button is hidden; paste a PAT.

## 7. How App Studio uses it

- Project creation resolves a validated `Connection` (else the portal's
  "You need to connect to a Git account before you can continue"), picks a
  name, and the Project reconciler creates a `Repository` with
  `visibility: private`, `autoInit: true`, label and annotation
  `app-studio.ai.railgrid.ai/project=<project>`, annotation
  `app-studio.ai.railgrid.ai/project-uid`, and `code.railgrid.ai/create-only: "true"`.
  An explicit project `name` is the repository name, with a 409
  (`a code Repository named "<n>" already exists (possibly left by a deleted
  project); adopt it with existingRepositoryRef or choose another name`);
  only derived names get a suffix.
- Repositories survive project deletion by default; the claim is released.
  The one-deletion opt-in is the Project annotation
  `ai.railgrid.ai/delete-repository: "true"` (the portal's `deleteRepository`
  option sets it): the `Repository` App Studio created is deleted (and so the
  GitHub repo); an adopted repository
  (`app-studio.ai.railgrid.ai/adopted: "true"`) is never deleted.
- `existingRepositoryRef` adopts an existing `Repository` CR (one project
  per repository) and hydrates from its default branch. To attach an
  arbitrary GitHub repo, first create a `Repository` CR naming it; the
  controller adopts it instead of creating.
- Runtime traffic: workspace → git is the `repositories/commit` action (plus
  `stage-commit-bundle` over 1 MiB), invoked **as App Studio** through its own
  export virtual workspace under its `spec.requires` claim — by the Project
  reconciler when idle and by the assistant's `commit_project_files` tool;
  it never calls `code__commit_files`. git → workspace is the
  `code__checkout_repository` MCP tool (hydrate); the build doctor is
  `code__build_status` and `code__rebuild` against the template's workflow
  file. Promotion selects a commit by matching its SHA to a `RepositoryCommit`.
- Project history lists `RepositoryCommit` CRs with label
  `code.railgrid.ai/repository=<ref>`, capped at 100.

## 8. Portal

Routes: `connections`, `connections/<name>`, `repositories`,
`repositories/<name>` (deploy keys, collaborators, packages panels),
`packages`, `create/connection/token`, `create/connection/github`,
`create/repository`. Everything goes through the hub's kcp proxy as plain kube
REST on `code.railgrid.ai/v1alpha1` (`/clusters/<cluster>/apis/code.railgrid.ai/v1alpha1/…`)
via the shared `portalkit` kube client; create-or-update is server-side apply,
and the credential Secret is written at `/api/v1/namespaces/default/secrets`.
