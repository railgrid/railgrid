# Code provider reference (git repositories)

Read from `providers/code/` on 2026-09-09.

## 1. What it is

The code provider is a declarative control plane over **GitHub**. It hosts
no git server: there is no faros clone URL, no `git-receive-pack`, and no
git smart-HTTP. You declare `Connection`, `Repository`, `DeployKey`, and
`Collaborator` CRs in your workspace; controllers reconcile them into real
GitHub state with the GitHub API. `status.cloneURL` and `status.sshURL` are
GitHub's own URLs. Clone and push with GitHub credentials.

HTTP surface of the provider itself: `/healthz`, `/readyz`, `/mcp`,
`/mcp/sse`, `/oauth/github/{config,start,callback}`, and the embedded
portal. CRUD is kubectl or GraphQL against the workspace, or the MCP tools.

## 2. CRDs (`code.faros.sh/v1alpha1`, all cluster-scoped)

| Kind | shortName | Purpose |
|---|---|---|
| `Connection` | `gconn` | A GitHub account or org binding: `provider: github`, `type: pat` (only implemented type; `oauth` connections are also written as PAT-shaped secrets by the portal), `owner`, `secretRef {name, namespace default "default", key default "token"}`, `baseURL` for GHES. Status: `login`, `scopes[]`, condition `Validated`. |
| `Repository` | `grepo` | `connectionRef` (required), `name` (repo name on GitHub), `owner` override, `visibility private\|public\|internal`, `description`, `defaultBranch`, `autoInit`. Status: `repoID`, `htmlURL`, `cloneURL`, `sshURL`, condition `Ready`. Adopts an existing GitHub repo of that name; creates it on 404. **Deleting the CR deletes the GitHub repo** (needs `delete_repo` scope). |
| `RepositoryCommit` | `gcommit` | Durable record of a commit made through faros: `repositoryRef`, `branch`, `message`, `source.bundleRef {name, digest}`. Status: `phase Pending\|Running\|Succeeded\|Failed`, `commitSHA`, `commitURL`, `files[]` (≤ 500). File contents never live in the CR. |
| `RepositoryCheckout` | `gcheckout` | Transient: `repositoryRef`, `ref`; status `commitSHA`, `bundleRef`, `skipped[]`. Created and deleted by the `checkout_repository` tool. |
| `RepositoryBuildStatus` | `gbuildstatus` | Transient: `repositoryRef`, `workflowFileName`, `ref`, `action status\|rerun`, `maxLogLines`. Status: `run {found, runID, htmlURL, headSHA, status, conclusion, jobs[{name, status, conclusion, failureLog}]}`, `dispatched`. |
| `DeployKey` | `gkey` | `repositoryRef`, `title`, `publicKey` (empty → ed25519 generated), `readOnly`. Status `keyID`, `secretRef` (Secret key `ssh-privatekey`). |
| `Collaborator` | `gcollab` | `repositoryRef`, `username`, `permission pull\|push\|admin`. Status `invitationID`, condition `InvitationPending`. |
| `Package` | `gpkg` | Read-only, crawled from ghcr every 2 minutes: `packageName`, `type`, `imageRepository`, `versions[{digest, tags[], createdAt}]` (≤ 100). Label `code.faros.sh/repository=<repo>`. This is how App Studio finds `sha-<commit>` images. |

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

**Rate limiting reports itself as a scope problem.** Every GitHub-backed
operation surfaces a 403 as
`github: forbidden — token lacks scope or rate-limited (403)`, so the message
is useless for telling the two apart. Read the detail after the colon: a
rate limit says `API rate limit exceeded for user ID …` and carries a
`rate reset in …` hint. Do not start editing PAT scopes on the strength of
the headline.

The limit is **per token**. `gh api rate_limit` reporting 5000/5000 says
nothing about the PAT in the `Connection`, because the `gh` CLI holds a
different token for the same user. Wait for the window and retry. Each failed
attempt still creates a `RepositoryCommit` in phase `Failed`, so a repository
carrying a long run of failed commits with identical messages is the
signature of a retry loop that ran through a rate limit.

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
change must be promotable through App Studio.

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
| `commit_files` | `repositoryRef`, `message?`, `branch?`, `files[{path,content}]`, `deletePaths[]` | Stores a bundle (≤ 500 files, 2 MiB per file, 16 MiB total, UTF-8 text), creates a `RepositoryCommit`, waits ≤ 75 s, returns `{name,phase,commitSHA,commitURL,branch,files,deletedPaths}`. Uses the GitHub Git Data API; no clone. |
| `checkout_repository` | `repositoryRef`, `ref?` | Returns `{phase,ref,commitSHA,files[{path,content}],skipped[]}` inline. Caps: 500 files, 256 KiB per file, 16 MiB total; binaries skipped. |
| `build_status` | `repositoryRef`, `workflowFileName`, `ref?`, `maxLogLines?` (200) | Latest run for that workflow plus per-job conclusions and failure log tails |
| `rebuild` | `repositoryRef`, `workflowFileName`, `ref?` | `workflow_dispatch`; returns `{dispatched}` |
| `add_deploy_key` | `name`, `repositoryRef`, `title?`, `publicKey?`, `readOnly?` | Omit `publicKey` to generate |
| `add_collaborator` | `name`, `repositoryRef`, `username`, `permission?` | |
| `remove_collaborator` | `name` | |

Provider-direct endpoint `https://<hub>/services/providers/code/mcp` exists
but the MCP SDK's host guard can 403 it behind the hub proxy; use the
aggregate.

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
  connect to a Git account before you can continue"), picks a free name, and
  the Project reconciler creates a `Repository` with `visibility: private`,
  `autoInit: true`, and label `app-studio.ai.faros.sh/project=<project>`.
- Repositories are never deleted with the project; the claim is released.
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
`create/repository`. Everything goes through the hub GraphQL gateway as
`code_faros_sh { v1alpha1 { … } }` plus `applyYaml`.
