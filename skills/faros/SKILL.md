---
name: faros
description: Use when driving a faros hub as a user from a laptop, CI, or an AI coding agent (Claude Code, Codex, Cursor) rather than developing faros itself. Covers logging in with the faros CLI, picking an org and workspace, wiring the workspace MCP endpoint, building and shipping an App Studio project end to end (template, GitHub repo, dev sandbox, CI build, promote to production, publish and share), adding binary assets, deploying containers and databases with the infrastructure provider, creating and invoking hosted agents, and reaching edge clusters and servers with kubectl, ssh, or MCP. Read before answering any question about faros CLI flags, hub URLs, org or workspace IDs, provider REST paths, kubectl resource kinds, or MCP tool names.
---

# faros for coding agents

faros is a multi-tenant control plane. One hub fronts your kcp workspace (a
Kubernetes-style API you reach with kubectl), the providers enabled in it (App
Studio, code, infrastructure, agents, kuery, …), and the edges you connect
(clusters and Linux servers behind NAT). Every call on every surface runs as
**you**, with your workspace RBAC.

This file is the map and the playbooks. The references hold the exhaustive
material; read the one for an area before non-trivial work in it:

| Area | Reference |
|---|---|
| The `faros` CLI: `env`, `app`, `commit`, `sandbox`, `mcp`, edges | [references/cli.md](references/cli.md) |
| Login, tokens, org/workspace IDs, hub REST, URL grammar, kube REST by cluster | [references/access.md](references/access.md) |
| App Studio: projects, assistant, files, attachments, sandbox, promote, publish | [references/app-studio.md](references/app-studio.md) |
| code provider: connections, repositories, commits, checkout, CI, packages | [references/code.md](references/code.md) |
| infrastructure: templates, instances, data plane, access gate, app tokens | [references/infrastructure.md](references/infrastructure.md) |
| agents: agents, runs, channels, schedules | [references/agents.md](references/agents.md) |
| MCP aggregate, per-edge MCP, edges, kuery | [references/mcp-and-edges.md](references/mcp-and-edges.md) |
| Error strings and what they mean, measured timings | [references/troubleshooting.md](references/troubleshooting.md) |

**When a live hub disagrees with this file, the hub wins.**

## 0. Fast path

```bash
faros login --hub-url https://<hub>          # no default hub exists; ask the user
eval "$(faros env)"                          # HUB CLUSTER ORG WS TOKEN AS MCP_URL MCP_TOKEN
faros app create shop --template application --wait
faros app status shop
# edit a clone locally, then:
git add -A && git commit -m "Add cart"       # local only, never push
faros commit "$(faros app status shop -o json | jq -r .project.repository.ref)"
faros sandbox exec shop-dev api -- node -e 'fetch("http://127.0.0.1:8080/api/health").then(r=>r.text()).then(console.log)'
faros app promote shop --hostname-prefix shop   # once `faros app status` says promotable
faros app publish shop --mode public
faros app status shop                           # the Production: line is the real URL
```

A static site or SPA uses `--template simple-webapp`, whose sandbox component
is `app`, not `api` (table in 4.2).

The rest of this file calls hub and provider REST through `fc`, and provider
MCP tools through `fmcp` (one tool call through the local MCP server,
section 2). In an MCP client that already lists the faros tools, call those
tools directly instead of `fmcp`.

```bash
fc() { curl -s -H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $ORG" -H "X-Faros-Workspace: $WS" "$@"; }
# AS is exported by `faros env`: $HUB/services/providers/app-studio
fmcp() {  # fmcp <provider__tool> ['<json args>'] → the tool's result; a tool error exits non-zero with its text
  jq -nc --arg n "$1" --argjson a "${2:-null}" '{jsonrpc:"2.0",id:1,method:"tools/call",params:{name:$n,arguments:($a // {})}}' |
    faros mcp proxy 2>/dev/null |
    jq -r 'if .error then error(.error.message) elif (.result.isError // false) then error(.result.content[0].text)
           else (.result.structuredContent // (.result.content[0].text | (fromjson? // .))) end'
}
```

OIDC tokens are short-lived: re-run `eval "$(faros env)"` in each new shell
call. `fmcp` needs no token: the proxy reads and refreshes your login itself.

## 1. Rules that override everything else

1. **There is no default hub.** If you don't know the hub URL, ask.
2. **Address workspaces by cluster ID** (`/clusters/<clusterName>`), never by
   `root:…` path (403). Tenant headers carry **UUIDs**, never display names;
   never send `X-Faros-Tenant`/`X-Faros-Cluster` (the hub owns those).
3. **No TTY means explicit flags**: `faros use --org --workspace`,
   `faros login --token`.
4. **Prefer kubectl over `faros apply`/`faros get`** for workspace resources.
5. **Enumerate, don't trust lists.** Providers, their `scope`, and your MCP
   tools differ per hub and per org: `GET /api/providers` and MCP `tools/list`
   are the only authority. Tool names are `<provider>__<tool>`; an MCP client
   adds its own prefix (e.g. `mcp__faros__code__list_repositories`).
6. **Check readiness before promising outcomes.** `exposure: internal` never
   gets a URL; a build is promotable only when every component has an image
   for the exact faros-recorded commit; a private or restricted URL answers
   anonymous curl with 302 (test it with an app token, 4.6), and **jobs and
   hosted agents cannot pass that gate at all**. Choose the access mode with
   your machine callers in mind (section 5, "Machine callers").
7. **Secrets stay out of prompts, logs and commits.** Where an API takes a
   Secret name (the `connections` slots, pull secrets, agent connections),
   reference it by name. Template `env` maps are world-readable, and **no
   workload template takes a reference to a Secret of your own**; patterns for
   app secrets are in [references/infrastructure.md](references/infrastructure.md)
   section 4, "Your own secrets".
8. **Destructive calls need the user's explicit ask**: deleting a project (and
   `?deleteRepository=true`, which deletes the GitHub repo), deleting a code
   `Repository` (deletes the GitHub repo), `delete_instance`, `delete_agent`,
   `edge delete`, `pods_delete`, service calls that move physical things.
9. **An App Studio project is created by App Studio, in one call.** Never
   hand-assemble one from a `Repository` + `Instance`; adopt existing repos
   with `existingRepositoryRef`.
10. **Only faros-recorded commits are promotable.** Use `faros commit`,
    `code__commit_files`, the assistant, or the file routes — never `git push`
    to a project repo.
11. **Tell "not yet" from "wrong" before acting** (section 8). Waiting and
    rebuilding are opposite responses.
12. **Tests and experiments must never target the user's real hub by
    accident.** A test that falls back to `~/.kube/config` creates real
    projects and GitHub repos.

## 2. Get connected

**Check for the CLI first.** Everything below assumes `faros` is on `PATH`.
`command not found: faros` (or `command -v faros` printing nothing) means it
is not installed, not that the hub is down. Install it with the curl
installer, which needs only `curl`, `tar` and `uname` and no sudo:

```bash
command -v faros >/dev/null || curl -fsSL https://downloads.faros.sh/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"       # the installer's default INSTALL_DIR; add to the shell profile too
faros version
```

`FAROS_VERSION=vX.Y.Z` pins a release; `INSTALL_DIR=/usr/local/bin sudo -E sh`
installs system-wide. krew, `go install` and the release archives (Windows
included; they unpack `kubectl-faros`, rename it to `faros`):
[references/access.md](references/access.md) section 1. Once the CLI is
present, `faros skills install` fetches the current version of this skill
from GitHub into `~/.claude/skills` and `~/.agents/skills`; run it when this
copy looks stale.

**No way to install anything.** The hub is plain HTTPS, so every CLI
command has a curl equivalent once you hold a bearer; the CLI is only the
convenient way to get one. On a static-token hub, `POST $HUB/auth/token-login`
with `Authorization: Bearer <token>` and an empty body provisions your
user and returns a kubeconfig (`.kubeconfig`, base64 in the JSON), and the same token is your
`TOKEN` for every `fc` call. On an OIDC hub there is no browser-free login:
ask someone with the CLI for a workspace service-account token
(`POST …/serviceaccounts/{uuid}/tokens`, [references/access.md](references/access.md)
section 6), which works on hub REST, kube REST and MCP, or for the
workspace MCP connect token, which works on the MCP endpoint only and sees
no org-owned provider tools. Org, workspace and cluster IDs then come from
`GET $HUB/api/orgs` and `GET $HUB/api/orgs/$ORG/workspaces` instead of
`faros env`.

`faros login` writes a kubeconfig context `faros` pointing at
`<hub>/clusters/<clusterName>`; OIDC logins cache tokens in
`~/.config/faros/tokens/`. `faros use --org <o> --workspace <w>` switches
workspace. Details, raw-curl equivalents and the hub REST map:
[references/access.md](references/access.md).

**Providers.** App Studio needs `code` and `infrastructure` enabled; `edges`
is an ordinary provider too. Catalog `scope` is `global` (platform) or `org`
(self-hosted; carries `ownerOrg`, and `shadowsPlatform: true` when it
replaces a platform provider of the same name).

```bash
fc "$HUB/api/providers" | jq -c '.items[] | {name, scope, ready, shadowsPlatform}'
fc "$HUB/api/orgs/$ORG/workspaces/$WS/providers/enabled"
fc -X POST "$HUB/api/orgs/$ORG/workspaces/$WS/providers/<p>/enable" -H 'Content-Type: application/json' \
  -d '{"acceptedClaims":[{"group":"","resource":"secrets"}]}'   # accept what the catalog lists
```

**MCP: the provider tools, as you.** `faros mcp proxy` is a local stdio MCP
server. It forwards every call to the workspace's aggregate MCP endpoint with
your own faros login (refreshed as it expires, the hub CA taken from the
kubeconfig), so it offers every provider tool federated for you, your org's
own providers included.

- **Claude Code with the faros plugin**: already registered as `faros`
  (`/mcp` shows its state). Nothing to do.
- **Any other client**: `claude mcp add faros -- faros mcp proxy`,
  `codex mcp add faros -- faros mcp proxy`, or
  `{"command": "faros", "args": ["mcp", "proxy"]}` in an `mcpServers` config;
  then restart or reconnect the client.
- **A shell or script**: `fmcp` (section 0) starts one proxy per call.

It serves the workspace the `faros` context pointed at when it started:
after `faros use`, reconnect it (`/mcp` in Claude Code). It needs CLI v0.1.33
or later: check that `faros mcp --help` lists `proxy`. An older CLI does not
fail on `faros mcp proxy`, it prints the `faros mcp` help and exits 0, so
`fmcp` reports a jq parse error and an MCP client reports the server failed
to start; reinstall the CLI. Not logged in yet, every call answers
`… run 'faros login' first`; log in and call again. `HTTP 403: Forbidden:
invalid Host header "<hub>"` is the hub, not your login (section 8).

The token-based alternative (`faros mcp url|claude|codex`, and
`MCP_URL`/`MCP_TOKEN` from `faros env`) uses the workspace's long-lived
MCPServer ServiceAccount token: meant for machines without a faros login,
and it gets **no org-owned provider tools**
([references/mcp-and-edges.md](references/mcp-and-edges.md)).

## 3. Pick the right surface

Provider work (code, infrastructure, agents, edges, kuery) goes through the
MCP tools when you have them: typed inputs from `tools/list`, readable errors,
no headers or tokens to carry. The hub itself (orgs, workspaces, members,
enabling providers), App Studio, git-based commits and YAML you want to keep
stay on the CLI, kubectl and REST.

| You want to | MCP tool (as you) | CLI / kubectl / REST |
|---|---|---|
| Create, inspect, promote, publish an App Studio project | none (App Studio has no MCP server) | `faros app …`, REST `$AS/api/projects/…` |
| Record edits as a promotable commit | `code__commit_files` (files passed inline) | `faros commit <repositoryRef>` from a clone |
| Put a file (incl. binary) into a project without git | none | `PUT $AS/api/projects/{p}/files/content?path=`, the Code tab |
| Why a build failed; re-run it | `code__build_status {repositoryRef}`, `code__rebuild` | `faros app status` shows only the promotion state |
| Sync, run, read logs in a dev-mode instance | `infrastructure__dev_sync`, `dev_exec`, `dev_logs`, `dev_restart` | `faros sandbox …`; App Studio's `<p>-dev` gets files only from git (`faros app sync`) |
| Provision, change, delete a workload or database without App Studio | `infrastructure__provision`, `update_instance`, `delete_instance` | `Instance` CR with kubectl (section 5) |
| Read workspace resources | `infrastructure__list_instances`/`get_instance`, `code__list_repositories`, `agents__list_agents` | `kubectl` on the `faros` context (every kind, secrets included) |
| Hosted agents: create, run, schedule | `agents__*` (section 6) | REST `$HUB/services/providers/agents/api/…`, which adds chat streaming, the inbox and usage |
| Kubernetes edges | `edges__*` kube tools (with several edges connected, `cluster` names one) | `faros edge kubeconfig`, `faros connect`, kubectl |
| Linux server edges | none | `faros ssh` |
| Fleet-wide reads across edges | `kuery__kuery_query {spec}` (enable `kuery` first) | REST `POST $HUB/services/providers/kuery/api/query` |
| Orgs, workspaces, members, enabling providers | none | `faros` CLI, hub REST (`fc`) |

`exec` exists only where the template declares it (`application`,
`simple-webapp`); a `worker` component answers
`HTTP 404: exec is not declared for component worker`.

**`tools/list` is what you can call.** Through `faros mcp proxy` it holds every
Ready provider of your org's catalog; an org's own copy replaces the platform
provider of the same name. The ServiceAccount token misses the org-owned
ones, so in an org that self-hosts `infrastructure`, a token-based client has
no `infrastructure__*`. A provider you have not enabled still lists its tools
(calls fail with RBAC or NotFound errors), and kuery answers `{}` until it is
enabled.

**Any provider can run an older release than this skill describes**, platform
providers included; an org's own copy (`scope: org`) lags most often. Its
templates, sandbox agent and data plane are whatever that release shipped.
Probe the capability you need instead of assuming it, and when it is missing,
change the design or stop and tell the user. Never route around a missing
input (for example a credential in `env`).

| Before relying on | Check |
|---|---|
| A template input (e.g. `connections`) | `kubectl get template <t> -o jsonpath='{.spec.version} {.spec.schema.properties.<input>}'` — undeclared keys are accepted silently (`Valid=True`) and do nothing. `connections` needs `simple-webapp` ≥ 0.3.0, `worker`/`cron-job` ≥ 0.2.0 |
| Binary files in a dev sandbox | `faros sandbox status <inst> <comp>` — `Sync: utf-8 only (binary files are not synced …)` means binaries are skipped there |
| An MCP tool | `tools/list` |

## 4. Playbook: build and ship with App Studio

### 4.1 Preconditions

```bash
fc "$AS/api/projects/create-readiness"    # gitConnection.status must be "ready"
fc "$AS/api/projects/llm-settings" | jq '{configured, defaultModelID}'   # needed only for the assistant
```

`connection-missing` → create a code `Connection` (PAT needs `repo, workflow,
delete_repo, read:org, admin:public_key, read:packages`, or use the portal's
"Connect with GitHub"): [references/code.md](references/code.md).

### 4.2 Choose a template

| Template | Shape | URL | Sandbox components | Static assets go in | Dev toolchain |
|---|---|---|---|---|---|
| `application` | `web/` (Vite) + `api/` + Postgres on one host, `/api/*` → api | yes | `api`, `web` | `web/public/` | Node.js only |
| `simple-webapp` | one container, one port | yes | `app` | `public/` | Node.js only |
| `worker` | Deployment, no Service; **dev sandbox only** | no | `worker` (no `exec`) | — | Node.js |

Whatever the template, read its contract before writing code:
`kubectl get template <t> -o jsonpath='{.spec.agent.usage}'` (the scaffold's
root `AGENTS.md` repeats it).

The split: **production** accepts any language (Railpack auto-detects Go,
Python, … — a Go build for arm64 under QEMU took ~8 min to promotable);
the **dev sandbox** is Node only. A tree without `package.json` makes the
`simple-webapp` sandbox run `npx vite` as a static file server (every route
404 unless there is an `index.html`, `faros sandbox status` still says
`Running: true, reachable=true`) and `faros app sync` answers 502.

- A `worker` project has no scaffold and no build components: `faros app status`
  shows `build=unsupported`, it can never be promoted, and its dev instance
  gets no `connections` (App Studio exposes no route to set dev values). For
  `DATABASE_URL`/`REDIS_URL` or production, deploy a `worker` `Instance`
  directly (section 5).
- The `simple-webapp` scaffold's `AGENTS.md` is written for a Vite-only app
  ("there is no backend here"), while the template accepts any single HTTP
  process on `0.0.0.0:$PORT`. Edit `AGENTS.md` when you replace the scaffold
  with a server, or the assistant will refuse to write one.

App with a database → `application` (it provisions Postgres and injects
`DATABASE_URL`). **`application` has no `connections` input**: its api gets
only its own `DATABASE_URL`, so it cannot use a `redis-cache` or a second
database (putting that credential in `apiEnv` would make it world-readable).
Use an in-process cache, or build on `simple-webapp`, whose production schema
includes `connections`. The `application` contract: bind `0.0.0.0:$PORT`, same-origin
`/api/*`, keep `/api/health` answering (CI smoke-tests it), keep both `dev`
(sandbox) and `start` (Railpack production image) scripts working, no
Dockerfile, retry the first DB connect **and run migrations inside that
retry loop**, `sslmode=disable`. The shipped scaffold's own `api/server.mjs`
breaks that last rule (it retries only `select 1` and runs `create table`
after the loop, which fails with `Connection terminated unexpectedly` when
Postgres is still starting): move the schema setup inside the loop when you
rewrite the file.

### 4.3 Create

```bash
faros app list                                    # the name must be free…
kubectl get repositories.code.faros.sh <name>     # …and no orphaned Repository of that name
faros app create shop --template application --display-name Shop --wait
```

- One call creates the Repository (private GitHub repo), the scaffold commit,
  and the dev instance `<name>-dev`. `--wait` blocks until the repository is
  ready and the scaffold commit landed; only then clone.
- An explicit name is also the repository name. If a Repository
  of that name exists (e.g. left by a deleted project) creation fails with 409
  — adopt it (`existingRepositoryRef`) or pick another name. Without a name
  (prompt-only creation) the repository name is generated: always read it
  (`faros app status <p> -o json | jq -r .project.repository.ref`, or
  `.repository.ref` from `GET $AS/api/projects/<p>`), never assume it equals
  the project name.
- `--prompt` records what to build; it does not start an assistant turn.
- **Prompt-only creation (REST, no `templateName`) creates only the Project
  and the Repository** (`template: null`, no dev instance yet); a
  `displayName` you send is kept. The assistant's first `default` turn picks
  the template (`select_project_template`, or `PUT …/template`), and only
  then are the scaffold and the dev instance created — the git host's
  `README.md`/`LICENSE`/`.gitignore` boilerplate does not block the scaffold.
  Pass `templateName` (or `--template`) when you already know it.
- **Adopting an existing GitHub repo**: create a code `Repository` CR naming
  the repo (it adopts instead of creating, ready in ~10 s), then
  `faros app create gosvc-app --template simple-webapp --existing-repository gosvc-repo`
  (REST: `POST $AS/api/projects` with `existingRepositoryRef`).
  The project hydrates from the default branch and `repository.ready` is
  true at once; `.repository.adopted` stays `null`, so don't read it as the
  signal. The repo's pre-existing history is **never promotable**
  (`build=none`, checkpoints say `No source commit has landed yet.` even with
  green CI): make one `faros commit` first.
- Typical timings: repository ready ~10 s, scaffold commit 15–40 s, dev
  instance Ready 1–2.5 min. If `repository.ready` is still false after 2 min
  and `kubectl get repositories.code.faros.sh <name> -o jsonpath='{.status}'`
  prints nothing, the code provider is not reconciling at all (section 8);
  `--wait` timing out after 5 min (`repository not ready with a succeeded
  commit after 5m0s`) is the same symptom, not a reason to recreate.

### 4.4 Develop — pick one path per session

**A. Local editor + `faros commit` (recommended for coding agents).**

```bash
gh repo clone <owner>/<repo> && cd <repo>   # owner/repo = tail of .project.repository.htmlURL in `faros app status -o json`
git remote set-url --push origin no-push    # a stray `git push` now fails; `faros commit` only fetches
# edit; run the project's checks locally: npm install && npm run build, then
# what CI smoke-tests (simple-webapp: `PORT=<free> npm start` and curl /; application: below)
git pull --rebase --autostash origin main   # only if the assistant, the files route or the Code tab committed since you cloned
git add -A && git commit -m "Add cart"
faros commit <repositoryRef>        # ~7 s; resets your clone onto the faros SHA
faros app sync shop                 # workspace ← git, then sandbox ← workspace; lists skipped files
```

The `application` smoke test, with a throwaway Postgres on a random free host
port. A fixed port may already belong to another local Postgres, and the test
then talks to the wrong database:

```bash
docker run -d --rm --name pg-smoke -e POSTGRES_USER=appuser -e POSTGRES_PASSWORD=pw \
  -e POSTGRES_DB=appdb -p 127.0.0.1::5432 postgres:16
PGPORT=$(docker port pg-smoke 5432 | head -1 | cut -d: -f2)
PORT=18081 DATABASE_URL="postgres://appuser:pw@127.0.0.1:$PGPORT/appdb" npm --prefix api start & API_PID=$!
sleep 5; curl -s localhost:18081/api/health; kill $API_PID; docker stop pg-smoke
```

Stop local servers by PID, never `pkill -f 'node server.mjs'`: that pattern
matches every Node server on the machine.

App Studio hydrates and syncs a faros-recorded commit by itself within
seconds, so `faros app sync` right after `faros commit` usually reports
`0 changed` — that is not a failure. Hydrate only writes files: paths a commit
**deleted** stay in the workspace and the sandbox until you
`DELETE $AS/api/projects/<p>/files/content?path=<path>` (check `GET …/files`
after a commit that removes files that could affect the build).

`faros commit` refuses a dirty tree and a HEAD that doesn't contain
`origin/<branch>`, keeps the message ≤ 512 characters, sends binaries only
when the code provider supports them, and never pushes. Without a clone,
`code__commit_files {repositoryRef, message, files: [{path, content}], deletePaths}`
records the same kind of promotable commit from inline content (limits in
[references/code.md](references/code.md)).

**B. The App Studio assistant.**

```bash
fc -X POST "$AS/api/projects/shop/assistant/threads" -H 'Content-Type: application/json' -d '{"id":"t1","title":"cart"}'
fc -X POST "$AS/api/projects/shop/assistant/threads/t1/turns" -H 'Content-Type: application/json' \
  -d '{"content":"Add a cart page backed by /api/cart.","clientUserMessageID":"m1","collaborationMode":"default"}'
curl -sN "$AS/api/projects/shop/assistant/threads/t1/events" -H "Authorization: Bearer $TOKEN" \
  -H "X-Faros-Org: $ORG" -H "X-Faros-Workspace: $WS" > events.log   # ends after turn.completed
```

- Modes: `default` or `plan` (no edits). Reviews have their own route
  (`…/threads/{t}/reviews`).
- The events stream replays from sequence 1 unless you send `Last-Event-ID`,
  and closes after `turn.completed`, so `curl -N > file` doubles as "wait".
- **Read the outcome, not the status.** `turn.completed` says `completed` even
  when steps inside the turn failed. The turn carries the step outcome
  (`failedItems`, `rejectedItems`, and up to five `failures`, all omitted when
  zero); print it with the reply:

  ```bash
  sed -n 's/^data: //p' events.log | jq -r '
    if .type=="item.completed" and .payload.item.type=="agentMessage" then "MSG: \(.payload.item.content)"
    elif .type=="turn.completed" then .payload.turn
      | "TURN: \(.status) failed=\(.failedItems // 0) rejected=\(.rejectedItems // 0)",
        ((.failures // [])[] | "FAIL: \(.title): \(.message)")
    else empty end'
  ```

  `GET …/threads/{t}/turns/{turn}` returns the same summary later.
  `rejectedItems` are steps whose approval was denied, not failures.

  `stale_source` failures are retried by the assistant itself.
  `Private preview inspection is unavailable … FAROS_HUB_PUBLIC_URL` means it
  could not look at the page (an operator setting), so check the UI yourself.
- Approval mode (`approvalMode` on each turn, default `on_request`): edits and
  sandbox commands run unasked; `promote_project`, `infrastructure__provision`
  and commits pause on an `approval.requested` event, so a turn told to deploy
  waits for your answer. `never` denies every write rather than skipping
  prompts. Table and the answer route:
  [references/app-studio.md](references/app-studio.md), "Assistant".
- **The reconciler commits the assistant's edits by itself** 5–15 s after the
  turn, as `Update N files in <dirs>`. Don't ask the model to commit. The
  `project.committed` event lands *after* the stream closed, so to see the
  commit poll `(.repository.commits // [])[0]` (or `faros app status`);
  `commits` is `null`, not `[]`, on a fresh project.
- **Only workspace files reach git.** What the assistant does inside the
  sandbox (`npm install`, generated files) is not synced back, and it may
  hand-edit `package-lock.json` to compensate — with made-up integrity hashes
  that break the production build (`npm ci` → `EINTEGRITY`). Before
  promoting, check the lockfile (`npm ci` in a local clone); fix it locally
  and upload it with the files route if needed.
- A small app in one turn: ~3 min; a follow-up feature: ~1 min.

**When git integration is down.** The code provider is unavailable when
`faros app sync` fails with `checkout repository: provider MCP error -32602:
unknown tool "code__checkout_repository"`, `faros commit` or `fmcp` answers
`unknown tool "code__…"`, and `GET $HUB/api/providers` shows `code`
`ready: false` (section 8). You can still run your code in the dev sandbox:
upload each changed file with `PUT $AS/api/projects/<p>/files/content?path=<path>`
(route C below). The upload schedules a dev sync; `POST $AS/api/projects/<p>/sync-development`
forces one and reports each component. This reaches the workspace and the
sandbox only: no commit is recorded and nothing becomes promotable. The
uploaded paths stay marked uncommitted, and the reconciler commits them once
the code provider is back. Wait for that commit in `faros app status`, then
`git pull --rebase` your clone before the next `faros commit`. Until then,
don't run `faros app sync`: its hydrate step writes the repository's older
files over your uploads.

**C. Files and binary assets.** Upload in the Code tab (button
or drag-and-drop), attach any file ≤ 25 MiB in chat (the assistant places it
with `import_attachment`), let the assistant fetch a **direct file URL** with
`download_file` (a marketplace listing page is not a file), or use REST:

```bash
curl -s -X PUT "$AS/api/projects/shop/files/content?path=web/public/assets/jeep.glb" \
  -H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $ORG" -H "X-Faros-Workspace: $WS" \
  -H 'If-None-Match: *' --data-binary @jeep.glb        # 201 {path,size,version,binary}
fc "$AS/api/projects/shop/files/content?path=web/public/assets/jeep.glb" | jq '{binary,size,version}'
```

`path` is relative to the repository root, so put the file where the
component serves it: `web/public/<file>` on `application` (served at
`/<file>`), `public/<file>` on `simple-webapp`. On `application`, a file
outside `web/` and `api/` uploads, commits and syncs without error and is
never served.

Binary limits: 25 MiB per file, 48 MiB per commit/sync. Written files are
committed by the reconciler (~45 s) and synced to the sandbox like assistant
edits — **but binaries reach the sandbox only if its dev agent lists
`base64` in `syncEncodings`** (table in section 3). Otherwise
the sync still says `Synced` (`faros app sync` lists such files as `binary-unsupported`), the file is missing in dev
(a Vite app serves its HTML fallback for the path), and the file still ships
to production. Verify binaries in production then (4.6). Files over 25 MiB
are never committed or synced. Route semantics (412/409/413, `files/raw`, `files/upload`): [references/app-studio.md](references/app-studio.md).

### 4.5 Verify in the dev sandbox

A private preview answers every non-browser request with 302 to
`<hub>/auth/apps/authorize` — that proves DNS, TLS and the route, nothing more.
Test the app from inside instead:

```bash
faros sandbox status shop-dev api     # component per 4.2 table; prints Running:, Port: <n> (reachable=…), Source:, Sync:
faros sandbox exec shop-dev api -- node -e 'fetch("http://127.0.0.1:8080/api/<new-route>").then(r=>r.text()).then(console.log)'
faros sandbox logs shop-dev api
```

Hit something only your change has, so you know the new code is live;
a sync answers `Synced` even when nothing visible changed, so it proves
nothing. A sync restarts the process only for the reload rules the template
declares (`package.json`, lockfiles). Vite reloads its own sources, and the
`application` scaffold's api runs `node --watch` (keep that `dev` script), but
a plain Node server (`dev: node server.mjs`, e.g. one you wrote for
`simple-webapp`) keeps serving the old code after `Synced … restarted=false`:
run `faros sandbox restart <inst> <comp>` and probe again. The dev port is the `Port:` line of `faros sandbox status`
(the template's `development.components.<c>.port` is a symbolic name). Exec is argv-only (use `sh -c` for a shell), ≤ 120 s, each
argument ≤ 4096 bytes, and does not get the app's environment — name the port
(8080 unless the template says otherwise) instead of reading `$PORT`.

If exec says `… has no source revision; run 'faros sandbox sync …' first` on
an App Studio dev instance, do **not** run `faros sandbox sync` there (it
replaces App Studio's managed file set); run `faros app sync <p>` and retry
(the CLI's hint says so for App Studio instances).

### 4.6 Build, promote, publish

```bash
faros app status shop            # waits are yours: promotable ~3–5.5 min after the commit
faros app status shop -o json | jq -c '.promotion | {promotable, status: .build.status, missing: .build.missing}'   # compact poll
faros app promote shop --hostname-prefix shop
HOST=$(kubectl get instance shop-prod -o jsonpath='{.status.host}')   # or the Production: line of `faros app status`
faros app publish shop --mode public            # or restricted; private = back to default
```

The host is `<prefix>-<12 hex>.<apps domain>`, on the platform's apps domain
rather than the hub's (e.g. `shop-993e49bbfff1.bob.faros.sh` for hub
`console-dev.faros.sh`). Read it, never build it. For a few seconds after a
promote `faros app status` prints `Production:   - (promoted; the production
instance has not reported yet, …)` while the Instance already has its URL;
re-run it, or read the Instance as above.

`--mode private` unpublishes: the Instance flips to `access: private`,
anonymous requests get a 302 again and `GET …/publishing` reads
`published: false, mode: private`. The owner's own app token keeps working
(the owner passes the access review), so prove "gated" with an anonymous
request, not your token.

- **Every faros-recorded commit runs the full CI build**, including a
  binary-only one, so upload assets before the last code commit rather than
  after it; several builds can be in flight and only the newest commit's
  images make the project promotable.
- `build.status: none` with your SHA = CI or the package crawl hasn't caught up
  (not an error); none with an earlier commit's SHA (or none) = the commit wasn't recorded
  through faros. `incomplete` with one component missing right after green CI
  is the crawl too. To see CI itself,
  `fmcp code__build_status '{"repositoryRef":"<ref>"}'` returns the latest run's
  conclusion per job and the failing log tail; `code__rebuild` re-runs it.
- **The hostname prefix is locked by the first promote** (the same prefix
  again is fine; a different one → 400 `…is locked after the first
  deployment`). Check `faros app status` for an existing production before
  choosing one. Each promote rolls pods, even for the same commit.
- Prod Ready 10 s–1.5 min after the first promote; the certificate is usually
  the longer wait. A new hostname can fail
  TLS (curl exit 35) for a few minutes while its certificate is issued
  (observed 0–9 min); don't debug before 10. `faros app publish` may be run
  before prod is Ready: it re-reads the publication for up to 15 s, and a
  `(not ready: …)` suffix after that means prod itself is not Ready yet
  (`faros app status` shows its phase). Wait for the certificate with
  `until curl -s -o /dev/null --max-time 15 https://$HOST/; do sleep 15; done`
  (curl exits 35 until it is issued, then the app's own status code).
- After `--mode public`, anonymous requests can still get the 302 for
  10–20 s while the gate picks up the change (the CLI's output says so); wait with
  `until [ "$(curl -s -o /dev/null -w '%{http_code}' https://$HOST/)" = 200 ]; do sleep 5; done`
  before concluding the publish failed.
- **`restricted` and `private` are enforced the same way** (the Instance keeps
  `access: private`): you, other workspace admins, and anyone you add with
  `POST …/publishing/grants {user}` get in; nobody else does. `restricted`
  additionally records the app as published, invite-only.
- A re-promote rolls pods while the instance stays `Ready`, so probe something
  only the new version has to know it rolled out.
- After the first promote, `GET …/publishing` reads `published: false,
  mode: "private"` until you publish.
- **Testing a private app from a shell**: mint a short-lived
  token for that one app and send it as a bearer; the gate refuses raw hub
  tokens:

  ```bash
  APP=$(fc -X POST "$HUB/auth/apps/token" -H 'Content-Type: application/json' \
    -d '{"cluster":"'$CLUSTER'","group":"infrastructure.faros.sh","resource":"instances","name":"shop-prod"}' | jq -r .token)
  curl -s -H "Authorization: Bearer $APP" https://$HOST/api/health
  ```

  The token needs your own login and lasts ≤ 15 min: jobs and agents can't
  get one (section 5, "Machine callers").

### 4.7 Delete

`DELETE $AS/api/projects/{p}?uid=<uid>` deletes the project and its dev
instance but leaves the Repository and GitHub repo (they block reuse of the
name). `&deleteRepository=true` also deletes a non-adopted repository **and its
GitHub repo** — ask first (rule 8). Without `uid` the call is 400 `project UID is required;
refresh the project list and try again`. Success is 204; measured teardown:
the project 404s at once, the GitHub repo and `Repository` CR are gone within
~10 s, the dev instance within ~1 min.

## 5. Playbook: deploy without App Studio

A different product, chosen up front: a running workload with no repo, CI,
promotion or publishing flow; nothing here later becomes a project. Tenants
see two kinds: `Template` (catalog) and `Instance`.

```bash
kubectl get templates
kubectl get template simple-webapp -o jsonpath='{.spec.agent.usage}'
kubectl apply -f - <<'EOF'
apiVersion: infrastructure.faros.sh/v1alpha1
kind: Instance
metadata: { name: hello, labels: { faros.sh/template: simple-webapp } }
spec:
  template: simple-webapp                      # immutable
  values:
    name: hello
    image: ghcr.io/you/hello:v1                # production mode
    port: 8080
    access: public
    connections: { database: mydb }            # → DATABASE_URL from a `database` instance named mydb
EOF
kubectl get instance hello -o jsonpath='{.status.phase} {.status.url}'
```

The same through the MCP tools:

```bash
fmcp infrastructure__describe_template '{"name":"simple-webapp"}'   # schema, agent usage, dev contract in one call
fmcp infrastructure__provision '{"template":"simple-webapp","name":"hello","values":{"name":"hello","image":"ghcr.io/you/hello:v1","port":8080,"access":"public"}}'
fmcp infrastructure__get_instance '{"name":"hello"}'
fmcp infrastructure__update_instance '{"name":"hello","values":{"image":"ghcr.io/you/hello:v2"}}'   # RFC 7386 merge patch, in place
```

`provision` is not idempotent (check `list_instances` first). Prefer the
kubectl YAML when the definition should live in a repo.

- `connections.database` / `connections.cache` (simple-webapp, worker,
  cron-job; **not** `application`) take the `values.name` of a `database` /
  `redis-cache` instance in the same workspace and inject `DATABASE_URL` /
  `REDIS_URL`. `connections.database` also accepts an `application`
  instance's name (e.g. `shop-prod`), since it reads Secret
  `<name>-db-credentials`, so a job can share the app's database. Unset slots leave the variable unset. A slot naming a missing
  instance leaves the pod unable to start **while the Instance still reports
  `Ready`** — the only symptom is a Cloudflare 502 from `faros sandbox
  status`/`exec`; double-check the name against `kubectl get instances`. Check the template declares
  `connections` first (section 3; `simple-webapp` 0.3.0 and
  `worker`/`cron-job` 0.2.0 added it, and hubs still ship older catalogs):
  where it doesn't, the value is ignored and the app simply has no
  `DATABASE_URL`. A job that must share an `application`'s database then has
  no clean route: run it inside the app's api (for example an in-process
  scheduler) instead of a `cron-job`.
- **Live sandbox, no git loop:** set `farosMode: development` (no image), wait
  ~1 min for Ready, then `faros sandbox sync <inst> app ./dir`,
  `faros sandbox exec`, `faros sandbox logs` (or `infrastructure__dev_sync`,
  `dev_exec`, `dev_logs`; `dev_sync` adds files, `faros sandbox sync`
  replaces the whole file set). `simple-webapp`'s dev start runs
  `npm run dev -- --host 0.0.0.0 --port $PORT --config …`; a non-Vite `dev`
  script receives those flags, so ignore them and read `process.env.PORT`, and
  `faros sandbox restart` after each source sync (only Vite hot-reloads).
  `access: public` is honored in development mode too.
- **Changing `env` on a live dev-mode instance:** `kubectl apply` with new
  `values.env` updates the object but the running pod keeps its old env, and
  `faros sandbox restart` restarts the process, not the pod. For the live
  change run `faros sandbox env <i> <c> KEY=value --restart` (the data plane's
  `env` verb plus a restart); keep `values.env` in sync so it survives a
  re-render, and never pass secrets this way.
- **`cron-job` has no `command`/`args` input**: the image entrypoint must do
  the work. With a public image, drive it through `env`: `node:20-alpine` with
  `NODE_OPTIONS=--import=data:text/javascript;base64,$(base64 < job.mjs | tr -d '\n')`
  (top-level `await`, end with `process.exit(code)`; the script is
  world-readable like any `env` value). Its `connections` inject into every run.
- **A cron-job's runs can't be observed**: no last-run time, exit code or
  logs anywhere (`faros sandbox logs` is dev-mode only). Test the same image
  and env locally with `docker run` first, and make each run leave evidence
  you can read, e.g. a row the app exposes.
- Values that violate a declared field are admitted and reported as
  `Valid=False/InvalidValues`, but **keys the template doesn't declare are
  accepted silently** with `Valid=True` — check the schema (section 3 table)
  before relying on an input.
- `database`/`redis-cache` are `exposure: internal` (no URL, ever).
- Private images: a `dockerconfigjson` Secret `<instance>-registry` in
  namespace `default`. Never set `expose.fqdn`, `farosCluster`,
  `credentialsSecretName`, `farosRedeployRevision`, `farosNetworkPhase`,
  `farosActions*`.

**Machine callers.** A private or restricted app's gate lets in only people
(a browser sign-in, or a `fapp_` token minted from a user's own login, ≤ 15
min). A `cron-job`, `worker` or hosted agent can't pass it. What works:

- **Same workspace:** all of a workspace's instances share one runtime
  namespace, and a workload can call an app's Service directly, without the
  gate: `http://<status.apiServiceRef.name>:<apiPort>` (e.g.
  `http://shop-prod-api:8080`) or `…webServiceRef…` on `application`,
  `http://<status.appServiceRef.name>:<port>` on `simple-webapp`. Protect such
  routes in the app itself, e.g. a shared-secret header.
- **Hosted agents and anything outside the workspace:** no unattended path.
  Publish the data they need publicly, or pass it in (an agent run's `task`).

For the shared secret itself, see "Your own secrets" in
[references/infrastructure.md](references/infrastructure.md) section 4.

Details: [references/infrastructure.md](references/infrastructure.md).

## 6. Playbook: hosted agents

Agents are CRs; runs live in the provider's database, reachable only through
MCP or REST. OpenAI-compatible models only; the tenant brings the key.

```bash
fmcp agents__list_model_credentials                  # existing credentials, keys redacted
fmcp agents__create_agent '{"name":"digest","systemPrompt":"…","autonomy":"auto","modelCredential":"main","budgetUSD":"2"}'
fmcp agents__update_agent '{"name":"digest","backgroundFamilies":["core","web"]}'   # tool grants: update_agent only
fmcp agents__run_agent '{"agent":"digest","task":"…","wait":120}'                  # then agents__get_run {runId, wait}
```

`create_agent` takes no tool grants: set `interactiveFamilies`,
`backgroundFamilies` and the matching `…Toolsets`/`…Connections` with
`update_agent` (list fields replace the stored list). A run started this way
is a background run (no edge tools). REST
(`AG=$HUB/services/providers/agents`) is the same API plus streaming chat,
the usage rollups and `idempotencyKey` on runs:
`fc -X POST "$AG/api/agents/digest/runs" -H 'Content-Type: application/json' -d '{"task":"…","wait":120,"idempotencyKey":"d-1"}' | jq -r .run.output`.
Deep research = `spawn` + `web`. Approvals are human-only (`/api/inbox`,
the portal). More: [references/agents.md](references/agents.md).

**An agent's `web_fetch` is anonymous.** Pointed at a private or restricted
faros app, it stops at the gate's redirect and returns `HTTP 302 …` with
`This is a private faros app … web_fetch cannot sign in. No content was
fetched.`, and agents can't get an app token. Give an agent public data only,
or put the data in the run's `task`.

## 7. Playbook: edges

```bash
faros edge create home-lab --labels env=home      # or --type server for a Linux host
faros edge join-command home-lab
faros edge kubeconfig home-lab -o home-lab.kubeconfig && kubectl --kubeconfig home-lab.kubeconfig get nodes
faros connect home-lab && kubectl get nodes && faros disconnect   # or: switch kubectl itself
faros ssh my-vps -- uptime                        # server edges; no port forwarding
cat deploy.sh | faros ssh my-vps -- "cat > /tmp/deploy.sh"   # stdin is forwarded for one-shot commands
```

To run something on several edges at once, a `Workload` (spread by
`edgeSelector`) fans out one `Placement` per edge. It renders into
`spec.targetNamespace` on the edge (default `default`), and a private image
needs `spec.simple.imagePullSecrets` naming a `docker-registry` Secret you
created in that namespace on every selected edge first (through
`faros edge kubeconfig`). Expose an in-cluster Service to the hub with an
edges `Service` CR and its `…/proxy/` route (keep the trailing slash: older
edges providers answer a bare 404 without it). YAML for both:
[references/mcp-and-edges.md](references/mcp-and-edges.md).

With the MCP tools: the kubernetes toolset (`edges__pods_list`,
`edges__resources_list`, `edges__pods_log`, …; answers are kubectl-style
text) and one bundle per discovered Service. With one connected Kubernetes
edge every call goes to it; only with several do the tools take a `cluster`
parameter and `edges__cluster_list` appear. Fleet reads across
clusters: kuery, which must be enabled in the workspace first (it is in the
catalog and on `tools/list` even when it is not); pass
`objects.cluster: true` to see which edge each object is on. `faros connect <edge>` makes
kubectl context `faros-<edge>` current (undo with `faros disconnect`); in
scripts prefer `faros edge kubeconfig -o <file>`. More: [references/mcp-and-edges.md](references/mcp-and-edges.md).

## 8. Reading failures

Most alarming states in the first minutes of a resource's life are latency.
Identify which one you're looking at before waiting or rebuilding:

| Symptom | Usually | Confirm |
|---|---|---|
| New URL fails TLS (curl exit 35) | Certificate still issuing (observed 0–9 min) | An existing app on the same domain serves; `openssl s_client -connect <host>:443 -servername <host> </dev/null \| openssl x509 -noout -subject` prints `Could not find certificate from <stdin>` — that output *is* the "no cert yet" signal |
| `build.status: none`, SHA is yours | CI or the package crawl hasn't caught up | `code__build_status`; the crawl runs every 30 s for 10 min after a commit, else every 2 min |
| New project's repository `Provisioning` for under 2 min | Repository still being created | Poll `.repository.ready` |
| Repository `Provisioning` > 2 min with an empty status and no finalizer (`faros app status` prints `not ready for <age> with no status: the code provider is not reconciling`), or `unknown tool "code__…"` | **Not latency**: the code provider is not watching tenant workspaces. `GET $HUB/api/providers` shows `code` `ready: false` with `readinessReason` `BackendUnhealthy` (its watch is down) or `HeartbeatStale` (process down). The hub answers `…/services/providers/code/readyz` with its own `provider not ready: code`, not the provider's detail | Give it up to 10 min: sometimes the watch recovers on its own, sometimes (after a kcp outage) only a restart helps. Still `ready: false` → hand the `readinessReason` to the operator; nothing client-side fixes it. Don't recreate the project (409 on the name). Keep working in the sandbox: 4.4, "When git integration is down" |
| A commit stays `Running`, condition reason `RateLimited` | The GitHub quota behind the code `Connection` is spent; it retries at the reset (up to 15 min) | The condition message names the retry time |
| Private URL → 302 `/auth/apps/authorize` | The access gate wants a browser | Use an app token (4.6) or `faros sandbox exec` |
| Agent run output says `This is a private faros app` | **Not latency**: the app is private/restricted and `web_fetch` is anonymous | Section 6: public data or data in `task` |
| Instance Ready, no `status.url` | **Not latency**: `exposure: internal` | `kubectl get template <t> -o jsonpath='{.spec.exposure}'` |

**403s that aren't about your permissions.**
- `HTTP 403: Forbidden: invalid Host header "<hub>"` from `faros mcp proxy`,
  `fmcp`, `faros commit` or any MCP client, while REST and kubectl work: the
  hub's MCP endpoint refused a request that reached it through a proxy on the
  same host or pod. Hubs from v0.1.33 and earlier have this bug. Your token
  is fine and nothing client-side fixes it (don't try other hostnames):
  report it so the operator upgrades the hub. Until then use REST and
  kubectl. Commits can still be recorded by uploading files through the
  files route (4.4 C): the reconciler commits them from inside the cluster.
- Body mentions Cloudflare / `error code: 1010`: the edge blocked your HTTP
  client's user agent (Python's default). Use curl, or set a browser-like
  `User-Agent`. Real faros denials are Kubernetes `Status` JSON.
- `github: rate limited, resets in …`: the GitHub token behind the code
  `Connection` is out of quota. Commits wait and retry for up to 15 min
  (`RateLimited` condition) instead of failing. `github: forbidden — token
  lacks the required scope (403)` is a real scope problem.
- **Login fails with GitHub `API rate limit exceeded for user ID …`** while
  `gh api rate_limit` looks fine: the GitHub quota behind the hub's login is
  exhausted. If the code provider reports `rate limited` with the same reset
  time, the workspace's `Connection` token shares that quota with login —
  reconnect it with a PAT or the code provider's own "Connect with GitHub" app.
  Otherwise only the hourly reset helps.

**MCP results.** A failing tool is not an MCP error: the call succeeds with
`result.isError: true` and the reason in `result.content[0].text` (on success
`isError` is absent). `fmcp` turns that into a non-zero exit printing
`jq: error (at <stdin>:1): <the tool's text>`. Most tools answer JSON; the
`edges__*` kube tools answer kubectl-style text. Calling the endpoint over
raw HTTP instead: [references/mcp-and-edges.md](references/mcp-and-edges.md).
In zsh never `echo "$json"` (it expands `\n` and breaks jq) — pipe or
`printf '%s'`.

Everything else — every error string with its fix, and measured timings — is in
[references/troubleshooting.md](references/troubleshooting.md).
