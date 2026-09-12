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
```

The rest of this file calls REST through a small helper and the App Studio
base URL:

```bash
fc() { curl -s -H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $ORG" -H "X-Faros-Workspace: $WS" "$@"; }
# AS is exported by `faros env`: $HUB/services/providers/app-studio
```

OIDC tokens are short-lived: re-run `eval "$(faros env)"` in each new shell
call. `MCP_TOKEN` is a long-lived ServiceAccount token.

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
   are the only authority. Tool names are `<provider>__<tool>`.
6. **Check readiness before promising outcomes.** `exposure: internal` never
   gets a URL; a build is promotable only when every component has an image
   for the exact faros-recorded commit; a private URL can't be tested with curl.
7. **Secrets stay out of prompts, logs and commits.** Reference Secrets by name.
   Template `env` maps are world-readable.
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
installs system-wide. Alternatives, when those tools are already present:
`kubectl krew index add faros https://github.com/faroshq/krew-index.git && kubectl krew install faros/faros`
(then `kubectl faros …` or `faros …`), `go install github.com/faroshq/faros/cmd/faros@latest`
(Go 1.26+), or the release tarball `kubectl-faros_<Linux|Darwin>_<x86_64|aarch64|arm64>.tar.gz`
from `https://github.com/faroshq/faros/releases`, which unpacks a binary
named `kubectl-faros`: rename it to `faros`. Windows gets
`kubectl-faros_Windows_<x86_64|arm64>.zip`; the installer script is Linux and macOS only.

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

**MCP.** `faros env` already exports `MCP_URL`/`MCP_TOKEN` (from
`GET /api/orgs/{org}/workspaces/{ws}/mcpservers/default/connect`).
To wire a client: `faros mcp url --mcpserver-name default` prints the URL plus
ready-made `claude mcp add` / Codex / Claude Desktop snippets with a real token.

## 3. Pick the right surface

| You want to | Use |
|---|---|
| Create, inspect, promote, publish an App Studio project | `faros app …` or REST `$AS/api/projects/…` (App Studio has no MCP server) |
| Record local edits as a promotable commit | `faros commit <repositoryRef>` (wraps `code__commit_files`) |
| Put a file (incl. binary) into a project without git | Code tab, or `PUT $AS/api/projects/{p}/files/content?path=` |
| Run a command / sync files in a dev-mode instance | `faros sandbox …` (data plane; works even without `infrastructure__*` MCP tools). `exec` exists only where the template declares it (`application`, `simple-webapp`); a `worker` component has sync, logs and restart but answers `HTTP 404: exec is not declared for component worker` |
| Workspace resources (instances, templates, repos, agents CRs, secrets) | `kubectl` on the `faros` context |
| Provision a container/database without App Studio | `Instance` CR (section 5) or `infrastructure__provision` |
| Hosted agents and runs | REST `$HUB/services/providers/agents/api/…` or `agents__*` |
| Edge clusters/servers | `faros edge kubeconfig` (file), `faros connect`/`disconnect` (switch kubectl), `faros ssh`, `edges__*` |
| Fleet-wide reads across edge clusters | `kuery__kuery_query {spec}` or REST `POST $HUB/services/providers/kuery/api/query`, after enabling `kuery` in the workspace ([references/mcp-and-edges.md](references/mcp-and-edges.md)) |

**MCP tools exist only for providers federated into your aggregate.** Human
bearers get platform providers plus their org's own (an org copy shadows the
platform provider of the same name). **ServiceAccount bearers — including the
`MCP_TOKEN` from the connect endpoint — get no org-owned providers and no
shadowed platform copy.** So in an org that self-hosts `infrastructure`,
`infrastructure__*` is absent for the usual MCP token: use `faros sandbox`,
kubectl, or REST instead. Check `tools/list` before planning around a tool.

**A self-hosted provider runs whatever release its owner deployed.** When the
catalog shows `scope: org` for a provider (typically `infrastructure`), its
templates, sandbox agent and data plane can lag the rest of the hub. Probe the
capability you need instead of assuming it:

| Before relying on | Check |
|---|---|
| A template input (e.g. `connections`) | `kubectl get template <t> -o jsonpath='{.spec.version} {.spec.schema.properties.<input>}'` — undeclared keys are accepted silently (`Valid=True`) and do nothing |
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

| Template | Shape | URL | Dev toolchain |
|---|---|---|---|
| `application` | `web/` (Vite) + `api/` + Postgres on one host, `/api/*` → api | yes | Node.js only |
| `simple-webapp` | one container, one port | yes | Node.js only |
| `worker` | Deployment, no Service; **dev sandbox only** | no | Node.js |

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
`DATABASE_URL`). Read the contract before writing code:
`kubectl get template application -o jsonpath='{.spec.agent.usage}'`. The
scaffold's root `AGENTS.md` repeats it: bind `0.0.0.0:$PORT`, same-origin
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
# edit; run the project's checks locally: npm install && npm run build, then
# what CI smoke-tests — application: a local postgres:16 + `PORT=<free port>
# DATABASE_URL=… npm start` and curl /api/health; simple-webapp: `PORT=<free> npm start` and curl /
git add -A && git commit -m "Add cart"
faros commit <repositoryRef>        # ~7 s; resets your clone onto the faros SHA
faros app sync shop                 # workspace ← git, then sandbox ← workspace; lists skipped files
```

App Studio hydrates and syncs a faros-recorded commit by itself within
seconds, so `faros app sync` right after `faros commit` usually reports
`0 changed` — that is not a failure. Hydrate only writes files: paths a commit
**deleted** stay in the workspace and the sandbox until you
`DELETE $AS/api/projects/<p>/files/content?path=<path>` (check `GET …/files`
after a commit that removes files that could affect the build).

`faros commit` refuses a dirty tree and a HEAD that doesn't contain
`origin/<branch>`, keeps the message ≤ 512 characters, sends binaries only
when the code provider supports them, and never pushes.

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

**C. Files and binary assets.** Upload in the Code tab (button
or drag-and-drop), attach any file ≤ 25 MiB in chat (the assistant places it
with `import_attachment`), let the assistant fetch a **direct file URL** with
`download_file` (a marketplace listing page is not a file), or use REST:

```bash
curl -s -X PUT "$AS/api/projects/shop/files/content?path=public/assets/jeep.glb" \
  -H "Authorization: Bearer $TOKEN" -H "X-Faros-Org: $ORG" -H "X-Faros-Workspace: $WS" \
  -H 'If-None-Match: *' --data-binary @jeep.glb        # 201 {path,size,version,binary}
fc "$AS/api/projects/shop/files/content?path=public/assets/jeep.glb" | jq '{binary,size,version}'
```

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
faros sandbox status shop-dev api                    # running, portReachable, sourceRevision
faros sandbox exec shop-dev api -- node -e 'fetch("http://127.0.0.1:8080/api/<new-route>").then(r=>r.text()).then(console.log)'
faros sandbox logs shop-dev api
```

Hit something only your change has, so you know the new code is live;
a sync answers `Synced` even when nothing visible changed, so it proves
nothing. A sync restarts the process only for the reload rules the template
declares (`package.json`, lockfiles). Vite reloads its own sources, but a
plain Node server (`dev: node server.mjs`) keeps serving the old code after
`Synced … restarted=false` — run `faros sandbox restart <inst> <comp>` and
probe again. The dev port is the `Port:` line of `faros sandbox status`
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
faros app promote shop --hostname-prefix shop
faros app publish shop --mode public            # or restricted; private = back to default
```

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
  is the crawl too.
- **The hostname prefix is locked by the first promote** (the same prefix
  again is fine; a different one → 400 `…is locked after the first
  deployment`). Check `faros app status` for an existing production before
  choosing one. Each promote rolls pods, even for the same commit.
- Prod Ready ~35 s–1.5 min after the first promote. A new hostname can fail
  TLS (curl exit 35) for a few minutes while its certificate is issued
  (observed 0–9 min); don't debug before 10. `faros app publish` may be run
  before prod is Ready; its `(not ready: Pending)` suffix reflects only the
  POST response — `faros app status` a moment later shows the real state.
  Wait for the certificate with
  `until curl -s -o /dev/null --max-time 15 https://$HOST/; do sleep 15; done`
  (curl exits 35 until it is issued, then the app's own status code).
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
  curl -s -H "Authorization: Bearer $APP" https://<app-host>/api/health
  ```

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

- `connections.database` / `connections.cache` (simple-webapp, worker,
  cron-job) take the `values.name` of a `database` /
  `redis-cache` instance in the same workspace and inject `DATABASE_URL` /
  `REDIS_URL`. Unset slots leave the variable unset. A slot naming a missing
  instance leaves the pod unable to start **while the Instance still reports
  `Ready`** — the only symptom is a Cloudflare 502 from `faros sandbox
  status`/`exec`; double-check the name against `kubectl get instances`. Check the template declares
  `connections` first (section 3): where it doesn't, the value is ignored and
  the app simply has no `DATABASE_URL`.
- **Live sandbox, no git loop:** set `farosMode: development` (no image), wait
  ~1 min for Ready, then `faros sandbox sync <inst> app ./dir`,
  `faros sandbox exec`, `faros sandbox logs`. `simple-webapp`'s dev start runs
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
  the work. With a public image, drive it through `env` (e.g. `node:20-alpine`
  with `NODE_OPTIONS=--import=data:text/javascript;base64,…`). Its
  `connections` inject into every run.
- Values that violate a declared field are admitted and reported as
  `Valid=False/InvalidValues`, but **keys the template doesn't declare are
  accepted silently** with `Valid=True` — check the schema (section 3 table)
  before relying on an input.
- `database`/`redis-cache` are `exposure: internal` (no URL, ever).
- Private images: a `dockerconfigjson` Secret `<instance>-registry` in
  namespace `default`. Never set `expose.fqdn`, `farosCluster`,
  `credentialsSecretName`, `farosRedeployRevision`, `farosNetworkPhase`,
  `farosActions*`.

Details: [references/infrastructure.md](references/infrastructure.md).

## 6. Playbook: hosted agents

`AG=$HUB/services/providers/agents`. Agents are CRs; runs live in the
provider's database (REST or MCP only). OpenAI-compatible models only; the
tenant brings the key.

```bash
fc "$AG/api/credentials"                 # existing model credentials (hasAPIKey)
fc "$AG/api/agents" | jq '.items[].spec.models.chat'
fc -X POST "$AG/api/agents" -H 'Content-Type: application/json' -d '{"name":"digest","systemPrompt":"…","autonomy":"auto","modelCredential":"main","backgroundFamilies":["core","web"],"budgetUSD":"2"}'
fc -X POST "$AG/api/agents/digest/runs" -H 'Content-Type: application/json' --max-time 150 \
  -d '{"task":"…","wait":120,"idempotencyKey":"d-1"}' | jq -r '.run.output'   # output is under .run
```

`POST /runs` is a background run (no edge tools); reusing an
`idempotencyKey` returns the original run (`reused: true`). Deep research =
`spawn` + `web`. Approvals are human-only (`/api/inbox`). More:
[references/agents.md](references/agents.md).

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
`edgeSelector`) fans out one `Placement` per edge — but it always renders into
namespace `default` on the edge and `simple` mode cannot pull private images;
for a namespace of your own or a private ghcr image, apply a Deployment (plus
a `docker-registry` Secret) through `faros edge kubeconfig`. Expose an
in-cluster Service to the hub with an edges `Service` CR and its `…/proxy`
route. YAML for both: [references/mcp-and-edges.md](references/mcp-and-edges.md).

On MCP: `edges__cluster_list`, the kubernetes toolset (`edges__pods_list`, …,
`cluster` parameter) and one bundle per discovered Service. Fleet reads across
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
| Repository `Provisioning` > 2 min, `kubectl get repositories.code.faros.sh <n> -o jsonpath='{.status}'` empty, no finalizer (`faros app status` prints `not ready for <age> with no status: the code provider is not reconciling`) | **Not latency**: the code provider's controllers are not engaged with kcp. `GET /api/providers` shows `code` `ready: false` and its `/readyz` names the endpoint it is retrying; other fresh Repositories are statusless too | Wait — the provider retries its kcp watch with backoff and catches up by itself; if `ready` stays false for long, the operator checks its logs. Don't recreate the project (409 on the name) |
| A commit stays `Running`, condition reason `RateLimited` | The GitHub quota behind the code `Connection` is spent; it retries at the reset (up to 15 min) | The condition message names the retry time |
| Private URL → 302 `/auth/apps/authorize` | The access gate wants a browser | Use an app token (4.6) or `faros sandbox exec` |
| Instance Ready, no `status.url` | **Not latency**: `exposure: internal` | `kubectl get template <t> -o jsonpath='{.spec.exposure}'` |

**403s that aren't about your permissions.**
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

**MCP over plain HTTP.** `tools/call` is a JSON-RPC POST with
`Accept: application/json, text/event-stream` (anything else → 400). Replies
may be SSE (`data: {…}`, take the last). A failing tool is HTTP 200 with
`result.isError: true` and the reason in `result.content[0].text`; on success
`isError` is **absent** (test `(.result.isError // false)`) and that text is
the tool's output — JSON as a string for most tools, plain text for the
`edges__*` kube tools. In zsh never `echo "$json"`
(it expands `\n` and breaks jq) — pipe or `printf '%s'`.

Everything else — every error string with its fix, and measured timings — is in
[references/troubleshooting.md](references/troubleshooting.md).
