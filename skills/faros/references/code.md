# Code provider reference (git repositories)

## 1. What it is

The code provider is a declarative control plane over **GitHub**. It hosts
no git server: there is no faros clone URL, no `git-receive-pack`, and no
git smart-HTTP. You declare `Connection`, `Repository`, `DeployKey`, and
`Collaborator` CRs in your workspace; controllers reconcile them into real
GitHub state with the GitHub API. `status.cloneURL` and `status.sshURL` are
GitHub's own URLs. Clone and push with GitHub credentials.

HTTP surface of the provider itself: `/healthz`, `/readyz` (liveness of the
HTTP process only: both stayed 200, and the catalog stayed `ready: true`,
while the controllers were dead for an hour — judge reconciliation by the
freshness of `Repository`/`Package` status instead), `/mcp`, `/mcp/sse`,
`/oauth/github/{config,start,callback}`, and the embedded portal. CRUD is kubectl or kube REST through `/clusters/<cluster>`, or the MCP
tools.

## 2. CRDs (`code.faros.sh/v1alpha1`, all cluster-scoped)

| Kind | shortName | Purpose |
|---|---|---|
| `Connection` | `gconn` | A GitHub account or org binding: `provider: github`, `type: pat` (only implemented type; `oauth` connections are also written as PAT-shaped secrets by the portal), `owner`, `secretRef {name, namespace default "default", key default "token"}`, `baseURL` for GHES. Status: `login`, `scopes[]`, condition `Validated`. |
| `Repository` | `grepo` | `connectionRef` (required), `name` (repo name on GitHub), `owner` override, `visibility private\|public\|internal`, `description`, `defaultBranch`, `autoInit`. Status: `repoID`, `htmlURL`, `cloneURL`, `sshURL`, condition `Ready`. Adopts an existing GitHub repo of that name; creates it on 404. **Deleting the CR deletes the GitHub repo** (needs `delete_repo` scope). |
| `RepositoryCommit` | `gcommit` | Durable record of a commit made through faros: `repositoryRef`, `branch`, `message` (≤ 512 chars), `source.bundleRef {name, digest}`. Status: `phase Pending\|Running\|Succeeded\|Failed`, `startedAt`, `commitSHA`, `commitURL`, `files[]` (≤ 500). File contents never live in the CR. Rate-limited commits stay `Running` with Ready reason `RateLimited` (see below). |
| `RepositoryCheckout` | `gcheckout` | Transient: `repositoryRef`, `ref`; status `commitSHA`, `bundleRef`, `skipped[]`. Created and deleted by the `checkout_repository` tool. Annotation `code.faros.sh/binary-encoding: base64` opts into binaries (any other value fails the checkout). |
| `RepositoryBuildStatus` | `gbuildstatus` | Transient: `repositoryRef`, `workflowFileName`, `ref`, `action status\|rerun`, `maxLogLines`. Status: `run {found, runID, htmlURL, headSHA, status, conclusion, jobs[{name, status, conclusion, failureLog}]}`, `dispatched`. |
| `DeployKey` | `gkey` | `repositoryRef`, `title`, `publicKey` (empty → ed25519 generated), `readOnly`. Status `keyID`, `secretRef` (Secret key `ssh-privatekey`). |
| `Collaborator` | `gcollab` | `repositoryRef`, `username`, `permission pull\|push\|admin`. Status `invitationID`, condition `InvitationPending`. |
| `Package` | `gpkg` | Read-only, crawled from ghcr every 2 minutes (every 30 s for 10 min after a commit succeeds): `packageName`, `type`, `imageRepository`, `versions[{digest, tags[], createdAt}]` (≤ 100). Label `code.faros.sh/repository=<repo>`. This is how App Studio finds `sha-<commit>` images. |

Minimal setup:

```yaml
apiVersion: v1
kind: Secret
metadata: { name: gh-token, namespace: default }
stringData: { token: ghp_xxx }
---
apiVersion: code.faros.sh/v1alpha1
kind: Connection
metadata: { name: github-default }
spec:
  provider: github
  type: pat
  owner: my-org-or-user
  secretRef: { name: gh-token }
---
apiVersion: code.faros.sh/v1alpha1
kind: Repository
metadata: { name: my-service }
spec:
  connectionRef: github-default
  name: my-service
  visibility: private
  autoInit: true
```

PAT scopes: `repo`, `workflow`, `delete_repo`, `read:org`, `admin:public_key`,
`read:packages`. Without `workflow`, commits touching `.github/workflows/`
fail.

**Rate limits vs scopes.** Rate limits are
classified first — primary (`X-RateLimit` reset) and secondary/abuse
(`Retry-After`, default 1 min) — and always read
`github: rate limited, resets in <dur> (at <RFC3339>): <detail>` (the detail
is GitHub's text, e.g. `API rate limit exceeded for user ID …`, or
`HTTP 403`/`HTTP 429` from the shared transport). A remaining 403 reads
`github: forbidden — token lacks the required scope (403): …` and really is a
scope problem; 401 is `github: credential rejected (401): …`.

A `RepositoryCommit` that hits a rate limit stays `Running`, Ready
`False`/`RateLimited` with `GitHub rate limit; retrying in <N>s`, keeps its
bundle, and is retried at the reset (the idempotency trailer prevents a
double commit). It is marked `Failed` only when the reset falls more than
15 minutes after `status.startedAt`.

The limit is **per token**. `gh api rate_limit` reporting 5000/5000 says
nothing about the PAT in the `Connection`, because the `gh` CLI holds a
different token for the same user. Each failed attempt leaves a
`RepositoryCommit` in phase `Failed`, so a long run of failed commits with
identical messages is the signature of a retry loop. The Connection token is shared by every project's
commits, checkouts, build-status checks and the package crawl.

## 3. Cloning and pushing from a laptop

```bash
kubectl get repository my-service -o jsonpath='{.status.cloneURL}'   # https://github.com/org/my-service.git
git clone https://<user>:<gh-token>@github.com/org/my-service.git      # GitHub credentials
# or a faros-generated deploy key:
kubectl get deploykey ci-key -o jsonpath='{.status.secretRef.name}'
kubectl get secret <that> -n default -o jsonpath='{.data.ssh-privatekey}' | base64 -d > id_ed25519 && chmod 600 id_ed25519
GIT_SSH_COMMAND="ssh -i $PWD/id_ed25519" git clone git@github.com:org/my-service.git
```

`faros get-token` is unrelated to git; it is the kubectl OIDC exec plugin.

**Promotability caveat.** App Studio resolves builds from `RepositoryCommit`
CRs. A commit created by `git push` has no `RepositoryCommit`, so App Studio
cannot select it for promotion. Commit through `commit_files` when the
change must be promotable through App Studio — from a clone,
`faros commit <repositoryRef>` does that for your local commits
([cli.md](cli.md)).

**Package pickup.** A `RepositoryCommit` moving to `Succeeded`
enqueues its repository at once; for the 10 minutes after a commit's
`completedAt` the repository is crawled every 30 s and its GHCR
`container` listings and versions bypass the shared listing cache. Other
ecosystems stay cached; outside the window the crawl is every 2 min.

## 4. MCP tools (`code__*` on the aggregate)

Server name `faros-code`. Every action runs as the caller. Tenant identity
comes from the bearer; never ask the user for a tenant path.

| Tool | Input | Output / notes |
|---|---|---|
| `list_connections` | none | `{connections:[{name,provider,owner,login,validated}]}` |
| `list_repositories` | none | `{repositories:[{name,connection,repo,visibility,htmlURL,ready}]}` |
| `create_connection` | `name`, `owner`, `secretName`, `provider?`, `secretNamespace?`, `secretKey?`, `baseURL?` | Binds an existing Secret; the token never transits MCP |
| `create_repository` | `name`, `connectionRef`, `repo?`, `owner?`, `visibility?`, `description?`, `defaultBranch?`, `autoInit?` (default true) | |
| `delete_repository` | `name` | **Deletes the GitHub repo.** Idempotent. |
| `commit_files` | `repositoryRef`, `message?` (≤ 512 chars incl. body), `branch?`, `files[{path,content,encoding?}]`, `deletePaths[]` | Stores a bundle, creates a `RepositoryCommit`, waits ≤ 75 s, returns `{name,phase,commitSHA,commitURL,branch,files,deletedPaths}`. Uses the GitHub Git Data API; no clone. Limits and errors below. |
| `checkout_repository` | `repositoryRef`, `ref?`, `binaryEncoding?` (`base64`) | Returns one JSON **text** block `{repositoryRef,name,phase,ref,commitSHA,files[{path,content,encoding?}],skipped[]}`; there is no `outputSchema`/`structuredContent` (parse `content[0].text`). Caps below. |
| `build_status` | `repositoryRef`, `workflowFileName`, `ref?`, `maxLogLines?` (200) | Latest run for that workflow plus per-job conclusions and failure log tails. `workflowFileName` is the basename under `.github/workflows/` — `build.yaml` for the shipped scaffolds (the template's `development.build.workflowPath`), not the tool description's `faros-app-studio-build.yml` |
| `rebuild` | `repositoryRef`, `workflowFileName`, `ref?` | `workflow_dispatch`; returns `{dispatched}` |
| `add_deploy_key` | `name`, `repositoryRef`, `title?`, `publicKey?`, `readOnly?` | Omit `publicKey` to generate |
| `add_collaborator` | `name`, `repositoryRef`, `username`, `permission?` | |
| `remove_collaborator` | `name` | |

Provider-direct endpoint `https://<hub>/services/providers/code/mcp` exists
but the MCP SDK's host guard can 403 it behind the hub proxy; use the
aggregate.

### `commit_files` details

- `files[].encoding`: `utf-8` (default, or omitted) for text; `base64`
  (RFC 4648 standard, padded, no line breaks, strictly decoded) for binary.
  Anything else: `file "<p>": unsupported encoding "<e>": use "utf-8" or "base64"`.
  Clients send base64 only when the tool's `inputSchema` declares
  `files.items.properties.encoding`.
- Limits, on decoded bytes: 2 MiB per text file, 25 MiB per binary file,
  48 MiB and 500 files per commit.
  `file "<p>" is too large: N > M bytes`.
- Message checked before the bundle is written:
  `commit message is N characters; the limit is 512 — shorten the body`.
- Binary files are uploaded as git blobs (`CreateBlob`, identical payloads
  once) with a 3-minute per-request timeout (default GitHub timeout 30 s);
  checkout downloads of blobs > 1 MiB use the same long timeout.
- Result phase: `Succeeded` → ok; `Failed` →
  `RepositoryCommit "<n>" failed: <condition message>`. Still running after
  75 s is a **tool error**:
  - rate limited: `RepositoryCommit "<n>" is queued behind a GitHub rate limit (<detail>); the provider retries it until <RFC3339>, then marks it Failed. The files are not committed yet: watch RepositoryCommit "<n>" for phase Succeeded before relying on them`
  - otherwise: `RepositoryCommit "<n>" did not finish within the 1m15s wait (phase <p>); the files may not be committed yet: watch RepositoryCommit "<n>" for phase Succeeded or Failed`
- Bundles are deleted once consumed; a sweeper removes bundles and temp files
  untouched for 24 h (hourly, and at startup).

### `checkout_repository` caps

| Mode | Per file | Total | Files |
|---|---|---|---|
| default (text only; binaries listed in `skipped`) | 256 KiB | 16 MiB | 500 |
| `binaryEncoding: "base64"` | 256 KiB text, 25 MiB binary | 48 MiB | 500 |

Skipped entries carry a reason suffix: ` (binary)`, ` (file too large)`,
` (total-size cap)`, ` (file-count cap)`. A caller that did not opt in never receives an encoded file (it is
listed as `<path> (binary)`). Bad value:
`unsupported binaryEncoding "<v>": use "base64" or omit it`.

## 5. GitHub OAuth (portal only)

`GET /services/providers/code/oauth/github/config` → `{enabled, startURL, scopes}`.
The portal opens `startURL` in a popup; the callback page posts the token to
the portal, which writes the Secret and a `Connection`. The token never
transits kcp or the hub. Configured by the operator with
`GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`,
`GITHUB_OAUTH_REDIRECT_URL`, `GITHUB_OAUTH_PORTAL_ORIGIN`, `GITHUB_OAUTH_SCOPES`.
GitHub App installations are declared in the enum but not implemented.

## 6. How App Studio uses it

- Project creation resolves a validated `Connection` (else "You need to
  connect to a Git account before you can continue"), picks a name, and
  the Project reconciler creates a `Repository` with `visibility: private`,
  `autoInit: true`, and label `app-studio.ai.faros.sh/project=<project>`.
  An explicit project `name` is the repository name, with a 409 if a
  `Repository` of that name exists; only derived names get a suffix.
- Repositories survive project deletion by default; the claim is released.
  `DELETE …?uid=&deleteRepository=true` deletes the
  `Repository` App Studio created (and so the GitHub repo); adopted or
  foreign repositories are a 409.
- `existingRepositoryRef` adopts an existing `Repository` CR (one project
  per repository) and hydrates from its default branch. To attach an
  arbitrary GitHub repo, first create a `Repository` CR naming it; the
  controller adopts it instead of creating.
- Runtime traffic: `commit_files` (workspace → git, by the reconciler when
  idle and by the `commit_project_files` tool), `checkout_repository`
  (git → workspace), `build_status` and `rebuild` (build doctor) against
  the template's workflow file.
- Project history lists `RepositoryCommit` CRs with label
  `code.faros.sh/repository=<ref>`, capped at 100.

## 7. Portal

Routes: `connections`, `connections/<name>`, `repositories`,
`repositories/<name>` (deploy keys, collaborators, packages panels),
`packages`, `create/connection/token`, `create/connection/github`,
`create/repository`. Everything goes through the hub's kcp proxy as plain kube
REST on `code.faros.sh/v1alpha1` (`/clusters/<cluster>/apis/code.faros.sh/v1alpha1/…`)
via the shared `portalkit` kube client; create-or-update is server-side apply.
