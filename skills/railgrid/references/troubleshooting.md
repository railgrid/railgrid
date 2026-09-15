# Troubleshooting reference: error strings and latency

Exact strings are in backticks; `…` marks elided detail.

## 1. Symptom → cause → fix

### Login, CLI, hub REST

| Symptom | Cause | Fix |
|---|---|---|
| `command not found: railgrid` / `railgrid: not found` | The CLI is not installed; nothing to do with the hub | `curl -fsSL https://downloads.railgrid.ai/install.sh \| sh` (SKILL.md section 2); krew, `go install` or the release tarball otherwise |
| Installer prints `Installed railgrid …` but `railgrid` still isn't found | `~/.local/bin` (the default `INSTALL_DIR`) is not on `PATH` | `export PATH="$HOME/.local/bin:$PATH"` in this shell and the profile, or reinstall with `INSTALL_DIR=/usr/local/bin sudo -E sh` |
| Installer: `missing required tool: curl\|tar\|uname`, `unsupported OS: …`, `unsupported architecture: …` | The host lacks a prerequisite, or the script doesn't cover it (Linux and Darwin only; x86_64, aarch64/arm64, ppc64le) | Install the tool; on Windows unpack `kubectl-railgrid_Windows_<arch>.zip` from the releases page; otherwise skip the CLI and use raw REST with a token (SKILL.md section 2, "No way to install anything") |
| Installer: `could not resolve latest release tag from GitHub` | `api.github.com` unreachable or rate-limited | Set `RAILGRID_VERSION=vX.Y.Z` so no lookup is needed |
| `kubectl railgrid` works but `railgrid` doesn't (or vice versa) | krew installs the plugin as `kubectl-railgrid` only; the curl installer installs `railgrid` only | Either name runs the same binary; symlink one to the other if a script needs it |
| `no hub configured` | No hub URL | `--hub-url` or `RAILGRID_HUB_URL` |
| `no interactive terminal; pass --org and --workspace` | CI / no TTY | Pass both flags to `railgrid use` |
| `… (token missing or expired; run 'railgrid login')` | 401 from the hub; OIDC token expired | `railgrid login`; re-run `eval "$(railgrid env)"` for a fresh `TOKEN` |
| `cluster <id> (kubeconfig context "<ctx>") is not a workspace of any org you belong to; run 'railgrid use'` | Kubeconfig points at a workspace you can't list | `railgrid use`, or pass `--org`/`--workspace` |
| `workspace "<w>" matches in N organizations; pass --org …` | Same display name in two orgs | Add `--org` |
| Login via auth.railgrid.ai fails with GitHub `API rate limit exceeded for user ID …` while `gh api rate_limit` looks fine | Likely (not verified): the hub's GitHub OAuth app quota for that user is exhausted; the same OAuth app may back both Dex login and the code provider's "Connect with GitHub" connection, and `gh` uses a different token | Wait for the hourly reset; ask the operator to use separate GitHub OAuth apps for login and the code connection |
| 400 `no workspace selected` on a bare `/api` call | Workspace-scoped path without a cluster | Use `/clusters/<clusterName>/…` |
| 403 `address workspaces by cluster ID` | You used a `root:…` path | Resolve `clusterName` via `/api/orgs/{org}/workspaces` |
| 403 on a provider REST call (Kubernetes `Status` body) | Missing `X-Railgrid-Org` / `X-Railgrid-Workspace`, or not a member | Send both headers; check membership |
| 403 whose body mentions `cloudflare` or `error code: 1010` | Cloudflare blocked the HTTP client's user agent (Python's default), not authorization | Use `curl`, or set a browser-like `User-Agent`; real railgrid denials are `Status` JSON |
| jq: `Invalid string: control characters … must be escaped` | zsh `echo "$json"` expanded `\n` | Pipe straight to jq or `printf '%s'` |

### MCP

| Symptom | Cause | Fix |
|---|---|---|
| `railgrid mcp url` prints `Bearer <your-token>` | No token to embed: `--edge` on an OIDC hub, the `railgrid` context targets another workspace than the current context, or the MCPServer token is not minted yet (stderr says which) | `eval "$(railgrid env)"` for `MCP_TOKEN`/`TOKEN`, or `…/mcpservers/default/connect` |
| stderr `The hub has not minted this MCP server's token yet; re-run shortly.` / `railgrid env` `mcp=token not ready` | MCPServer token Secret not ready | Retry shortly |
| 401 on the MCP endpoint | Bearer missing, or not a member of that workspace | Use `railgrid mcp proxy`, the connect token or your hub token; 429 = retry after 60 s |
| 503 on the MCP endpoint | Verifier unavailable, or (ServiceAccount bearers) the workspace-path lookup is down | Retry |
| `railgrid mcp proxy` prints the `railgrid mcp` help (listing only `url`) and exits 0; `fmcp` → `jq: parse error`; the client's `railgrid` MCP server fails to start | The installed CLI predates `railgrid mcp proxy` (v0.1.33); older CLIs print a group's help for an unknown subcommand instead of failing | Reinstall the CLI (`curl -fsSL https://downloads.railgrid.ai/install.sh \| sh`), check `railgrid mcp --help` lists `proxy`, reconnect the client. Newer CLIs answer `unknown command "<x>" for "railgrid mcp"` |
| `HTTP 403: Forbidden: invalid Host header "<hub>"` from the proxy, `fmcp`, `railgrid commit` or an MCP client, while REST, kubectl and app tokens work | Hub bug in v0.1.33 and earlier: the MCP SDK's DNS-rebinding guard rejects requests that reach the hub over a loopback socket (a proxy on the same host or pod, `kubectl port-forward`, a host-run local hub at `console.127.0.0.1.sslip.io`). Not your token, not RBAC (those are 401 or a `Status` body), not Cloudflare (its body says so) | Operator upgrades the hub; nothing client-side helps, don't switch hostnames. Meanwhile REST and kubectl work, and files uploaded through the App Studio files route are still committed by the reconciler in-cluster |
| `unknown tool "<provider>__<tool>"` (also inside other errors, e.g. `provider MCP error -32602: unknown tool "code__checkout_repository"`) for a tool the skill names | That provider is not Ready, so the aggregate does not federate it (or it is org-owned and you use the ServiceAccount token: the `<provider>__*` row below) | `GET $HUB/api/providers` → `ready`, `readinessReason`; for `code`, see the code-provider row under App Studio |
| `fmcp` prints `jq: error (at <stdin>:1): <text>` and exits 5 | The tool (or the proxy) reported an error; `<text>` is its message | Read the text: it is the provider's own error, matched in the tables here |
| `railgrid mcp proxy` tool calls answer `… run 'railgrid login' first` or `the hub rejected your credentials (HTTP 401); run 'railgrid login'` | No `railgrid` context yet, or the login expired past its refresh token | `railgrid login`; the next call works without restarting the client |
| The client lists the `railgrid` MCP server as failed to start | `railgrid` is not on the PATH the client launches it with (GUI apps such as Claude Desktop often lack the shell's PATH) | Install the CLI, or put its absolute path (`command -v railgrid`) in the client's `command` |
| A provider's new or renamed tool is missing, or a removed one still listed | The aggregate caches each Ready provider's tool list per bearer for 30 s and refreshes it in the background | Call again after 30 s; the request after that sees the change. Reconnect the client if it cached `tools/list` itself |
| Raw HTTP: `400 Accept must contain both 'application/json' and 'text/event-stream'` | Wrong `Accept` | Send `Accept: application/json, text/event-stream`, or use `railgrid mcp proxy` |
| Raw HTTP: 200 but the call failed | Tool errors come back as `result.isError: true` with text in `result.content[0].text`; on success the field is absent, not `false` | Test `(.result.isError // false)`, never the status |
| A `<provider>__*` tool does not exist | Provider is org-scoped (BYO): federated only for a human bearer in a team workspace, never for the connect token (a ServiceAccount), and the org copy hides the platform copy of the same name | Connect through `railgrid mcp proxy` (calls as you), call `tools/list` with your own hub `TOKEN`, or use kubectl / the provider's REST API ([mcp-and-edges.md](mcp-and-edges.md)) |
| `hub access memberships.invite (org scope) for provider "<p>" has not been accepted in this workspace…` (403), or `… does not declare hub access …` | The provider tried a hub REST call with its delegated token that no one accepted for it (org-owned providers always need acceptance; platform ones only where an admin declined or the operator set `--provider-hub-access-platform-default=false`) | An org admin enables the provider again and ticks the capability in the Enable dialog (Providers page → *Review access*) |
| `provider <method> response exceeds the 96 MiB limit; the result is too large to federate` | Tool result over the aggregate's cap | Ask for less (fewer files, no `binaryEncoding`) |
| kuery query answers `{}`, `GET …/kuery/api/edges` → `{"edges":[]}` | `kuery` is not enabled in this workspace (its tools are federated regardless), or its edge engagement fails provider-side (`GET …/api/status` → `engagedEdges: 0`) | Enable the provider with its four claims; edges engage within ~1 min. If `engagedEdges` stays 0 with connected Kubernetes edges, the operator checks the kuery provider logs |
| kuery MCP tool → `… not known to kuery yet` / 401 | The provider has not yet mapped this workspace's cluster ID (it learns it on its first reconcile after start or enable) | Retry in a few seconds |
| `cat f \| railgrid ssh x -- "cat > /tmp/f"` exits 0 but writes an empty file | The hub's edges provider ignores the CLI's `stdin=1` (it predates stdin forwarding) | Upgrade the edges provider; until then embed the content in the command (`printf '%s' '…' > /tmp/f`, or base64) |
| `kubectl` suddenly targets an edge, `railgrid env`/`railgrid app` complain about the workspace | `railgrid connect <edge>` made context `railgrid-<edge>` current | `railgrid disconnect` (or `railgrid use`) returns to `railgrid` |

### App Studio

| Symptom | Cause | Fix |
|---|---|---|
| `railgrid sandbox exec` on `<project>-dev` → `… has no source revision; run 'railgrid sandbox sync …' first` | App Studio changed files but hasn't synced authoritatively yet | `railgrid app sync <p>`, retry. Don't `railgrid sandbox sync` an App Studio dev instance — it replaces App Studio's managed files |
| Production build fails in `npm ci` with `EINTEGRITY … wanted sha512-… got sha512-…` after an assistant turn | The assistant's sandbox `npm install` never reached git and it hand-edited `package-lock.json` | Regenerate the lockfile locally (`npm install`), upload it (`PUT …/files/content`) or `railgrid commit` it |
| Binary file 404s / serves the HTML fallback in the dev preview but works in production | The sandbox's dev agent doesn't list `base64` in `syncEncodings`; `sync-development` still reports `Synced` | Verify binaries in production; the provider's operator must roll the sandbox to an agent with base64 sync |
| A file uploaded with `PUT files/content` 404s (or serves the HTML fallback) in dev **and** production; upload, commit and sync all succeeded | On `application`, the path is outside `web/` and `api/` (e.g. `public/…` at the repo root), so no component serves it | Upload to `web/public/<file>` (served at `/<file>`); on `simple-webapp` use `public/<file>` |
| `turn.completed` says `completed` but the change is missing or unverified | Tool steps inside the turn failed; the turn status doesn't reflect them | Read `.payload.turn.failedItems` and `.failures[]` (`title`, `message`); `rejectedItems` are denied approvals (SKILL.md 4.4 B) |
| Assistant turn never finishes, last event `approval.requested` | The approval mode asked (a promote, provisioning or a commit under `on_request`; everything under `always_ask`) | `POST …/turns/{turn}/approval {requestID, decision}`; don't switch to `never`, which denies every write |
| `create-readiness` → `connection-missing` | No validated code `Connection` | Create one ([code.md](code.md)) |
| Create → 409 `a code Repository named "<n>" already exists (possibly left by a deleted project); adopt it with existingRepositoryRef or choose another name` | An explicit `name` is the repository name and is never suffixed | Adopt with `existingRepositoryRef`, pick another name, or delete the repo |
| Create → `name must be a valid DNS label` | Explicit `name` is not a DNS label | Lowercase letters, digits, `-` |
| Fresh project: `repository.status: Provisioning`, `Creating repository "<n>".` | Repository CR still being created; latency | Poll `.repository.ready == true` |
| Repository still `Provisioning` after 2 min and `kubectl get repositories.code.railgrid.ai <n> -o jsonpath='{.status}'` prints nothing (no conditions, no finalizer); or `railgrid app sync` → `HTTP 502: BadGateway: checkout repository: provider MCP error -32602: unknown tool "code__checkout_repository"` | The code provider is not watching tenant workspaces: `GET /api/providers` → `code` `ready: false`, `readinessReason` `BackendUnhealthy` (watch down) or `HeartbeatStale` (process down); `…/services/providers/code/readyz` through the hub only says `provider not ready: code`. Every new project, commit, checkout and build lookup stalls; a fresh project's dev sandbox may start empty (`ENOENT /workspace/package.json`) | Wait up to 10 min (the watch sometimes recovers; after a kcp outage it needs a restart), then hand the `readinessReason` to the operator, who checks the provider's logs and its APIExport endpoint slice. Nothing client-side fixes it. Meanwhile: SKILL.md 4.4, "When git integration is down" |
| `code__commit_files` → `repository "<name>" not found` on a new project | Project Ready, Repository not yet | Same: poll `.repository.ready` |
| `code__commit_files` on a prompt-created project → `repository "<project>" not found` | Repository name differs from the project name | Use `.repository.ref` |
| 409 `wait for or stop the active assistant run before <action>` | An assistant run owns the project (template, hydrate, sync, delete, file writes) | Wait until `turns/active` is 204 |
| `collaborationMode must be default or plan` / `review runs must use the dedicated /assistant/threads/{thread}/reviews endpoint` | Turns route: unknown mode / `review` (case is ignored) | Use `default`/`plan`; POST `…/reviews` for review |
| `PUT files/content` → 412 | `If-Match` version stale, `If-None-Match: *` and file exists, or `If-Match: *` and `file does not exist` | Re-read `files/content` for the version |
| `files/upload` → 409 `file "<p>" already exists; upload with overwrite=true to replace it` | Existing target | `overwrite=true` |
| 413 `file exceeds the 26214400-byte binary limit` / `text file "<p>" is too large: N > 262144 bytes` / `upload exceeds the 50331648-byte request limit` | Workspace limits: 25 MiB binary, 256 KiB text, 48 MiB per upload | Smaller files; split uploads |
| `files/raw` → 409 `file changed while it was being read; retry` | Concurrent write | Retry |
| `publishing/grants` → 403 with the hub's message | You are not an admin of the workspace (inviting adds an org member) | Ask a workspace admin, or `railgrid workspace members add <email> --invite` as one |
| DELETE project → 409 `repository "<r>" was adopted (imported) … never deletes adopted repositories; …` or `… is not owned by project "<p>"; it was not deleted` | `deleteRepository=true` on an adopted/foreign repo | Delete without `deleteRepository`; remove the repo via the code provider |
| Attachment → 413 `attachment is N bytes; maximum is M` | Over 25 MiB (any file), or the per-kind bound | Smaller file |
| Assistant: `binary files cannot be placed while this run uses an isolated coding sandbox; …` | `import_attachment`/`download_file` in run-sandbox mode | Upload in the Code tab (`files/upload`) |
| Assistant: `<url> returned a web page (text/html), not a file; …` | `download_file` got HTML | Use the direct file/raw URL, or attach the file |
| Assistant: `only binary files changed (…), and this workspace's Code provider does not accept binary commits yet; …` | `code__commit_files` does not declare `files[].encoding` | Binaries stay uncommitted in the workspace; text commits normally |
| `private preview inspection is unavailable: the app-studio deployment has no usable RAILGRID_HUB_PUBLIC_URL (chart value hub.publicURL, …)` | Deployment misconfiguration | Operator sets `hub.publicURL` |
| Dev sync 409 `workspace sync revision is older than the applied revision` | A plain/CLI sync moved the agent's applied revision past the sender's | Continue numbering from the applied revision; App Studio renumbers and retries once by itself |
| `production setting "expose.hostnamePrefix" is locked after the first deployment` | Production was already deployed with another prefix by an earlier promote (yours, the portal's, or the assistant's `promote_project`); App Studio never promotes by itself | Read `GET …/promotion` `.production`; promote with `{}` or the same prefix |
| `promotion.build.status: none` after pushing | Commit not recorded through railgrid | Use `code__commit_files` / `railgrid commit` |
| `promotion.build.status: incomplete` | A component has no `sha-<commit>` image | `code__build_status`, then `code__rebuild` |

### Code provider

| Symptom | Cause | Fix |
|---|---|---|
| `github: rate limited, resets in <dur> (at <time>): …` | The Connection's GitHub token is out of quota (per token; shared by every project's commits, checkouts, build checks and the package crawl) | Wait; commits retry by themselves |
| `github: forbidden — token lacks the required scope (403): …` | A scope problem | PAT scopes `repo`, `workflow`, `delete_repo`, `read:org`, `admin:public_key`, `read:packages` |
| `github: credential rejected (401): …` | Token revoked/expired | Replace the Secret behind the `Connection` |
| `Connection` `Validated=False`: `Get "https://api.github.com/user": net/http: invalid header field value for "Authorization"` | The token Secret ends in a newline (`gh auth token \| kubectl create secret … --from-file=token=/dev/stdin`); older code providers sent it verbatim | Recreate the Secret from `… \| tr -d '\n'` or with `--from-literal`; current providers trim it |
| An edges Service `…/proxy` (no trailing slash) → bare `404 page not found` | Older edges providers routed only `…/proxy/…` | Use `…/proxy/`; current providers redirect a GET to it |
| `RepositoryCommit "<n>" is queued behind a GitHub rate limit (…); the provider retries it until <time>, then marks it Failed. …` | Commit accepted, waiting for the reset (Ready reason `RateLimited`, up to 15 min after `startedAt`) | Watch the RepositoryCommit for `Succeeded`; do not resend |
| `RepositoryCommit "<n>" did not finish within the 1m15s wait (phase <p>); …` | Commit still running after the tool's wait (a tool error, not success) | Watch the RepositoryCommit |
| `commit message is N characters; the limit is 512 — shorten the body` | Message cap | Shorten the body, keep the subject |
| `file "<p>" is too large: N > M bytes` | 2 MiB text / 25 MiB binary per file; 48 MiB, 500 files per commit | Split the commit |
| `unsupported encoding "<e>": use "utf-8" or "base64"` / `invalid base64 content` | Bad `files[].encoding` or non-canonical base64 (line breaks rejected) | Standard padded base64, one line |
| `<paths>: binary file(s) not supported: the hub's code provider doesn't support binary files yet …` (`railgrid commit`) | `code__commit_files` does not declare `files[].encoding` | Drop the binaries from the change |
| `HEAD does not contain origin/<branch> (it moved upstream); run 'git rebase origin/<branch>' first` | Upstream moved: the assistant, the files route or the Code tab committed since you cloned | `git pull --rebase --autostash origin <branch>`, re-run |
| `recorded <sha>, but origin/<branch>'s tree differs from your HEAD …` | Someone else committed, or a file mode was lost | Reconcile with `git rebase` |
| Many `Failed` commits with identical messages | The reconciler sends a fresh commit after each `Failed` one (e.g. a rate limit resetting more than 15 min after `startedAt`, a scope error) | Read one commit's Ready condition message and fix that cause |

### Infrastructure and data plane

| Symptom | Cause | Fix |
|---|---|---|
| `railgrid sandbox status` shows `Sync: utf-8 only` (and exec has no `PORT`) even on a brand-new sandbox after the provider was upgraded | The provider's sandbox agent image isn't the new release: an unpinned `railgrid-dev-agent:latest` stays cached on nodes (`IfNotPresent`) | Operator: set the chart's `development.agentImage` to the release tag or digest (release charts default to their own version); changing it re-renders every dev sandbox |
| `railgrid sandbox status/exec` → HTTP 502 Cloudflare `origin_bad_gateway` while the Instance shows `Ready` | The component's pod isn't running — e.g. `connections` names an instance that doesn't exist, so its Secret never appears | Check `spec.values.connections` against `kubectl get instances`; fix the name |
| `railgrid sandbox sync` → `skipping N binary file(s); <i>/<c>'s dev agent does not advertise base64 sync …` | That sandbox's agent can't take binaries | Nothing to do client-side; see the App Studio row above |
| Exec prints `Invalid URL … 127.0.0.1:undefined` | Code read `$PORT`, which exec doesn't always get | Name the port (8080 unless the template says otherwise) |
| Exec → 400 `start argv[N] must be non-empty, at most 4096 bytes` | One argument over 4 KiB | Pass data through a synced file instead of argv |
| `GET …/dataplane/…/components/<c>/status` → 405 `method GET not allowed for verb <c>/status` | Component status is the `process` verb | `GET …/components/<c>/process` (or `railgrid sandbox status <i> <c>`) |
| Instance `Valid=True` but an input has no effect (e.g. `connections` → no `DATABASE_URL`) | The template doesn't declare that key; undeclared keys are accepted silently. Hubs of every scope can run older catalogs: `connections` arrived in `simple-webapp` 0.3.0 and `worker`/`cron-job` 0.2.0 | `kubectl get template <t> -o jsonpath='{.spec.version} {.spec.schema.properties}'`; use a template that declares it, or change the design (never pass the credential through `env`) |
| Instance `Valid=False` / `InvalidValues` | `spec.values` violates the template schema | `describe_template` |
| Instance Ready but no `status.url` | Template `exposure: internal`; not latency | Never poll for a URL |
| New instance URL fails TLS, curl exit 35, `sslv3 alert handshake failure` | Per-host edge certificate still issuing (base domain below the Cloudflare zone apex) | Wait (see §2; observed 0–9 min); operator fix: `*.<baseDomain>` edge cert |
| `openssl s_client … \| openssl x509 -noout -subject` → `Could not find certificate from <stdin>` | No certificate for that host yet — the same issuance latency | Wait; the command prints the host's CN once issued |
| Pod stuck in `CreateContainerConfigError` after setting `connections.database`/`cache` | Named instance's Secret does not exist (wrong name, other workspace, not provisioned) | Provision the `database`/`redis-cache` in the same workspace, or fix the name |
| Exec → `sourceRevision is required for start: component "<c>" reports no applied source revision — sync its workspace first …` | Nothing synced yet, or a reload pending | Any sync (`dev_sync`, `railgrid sandbox sync`), then retry |
| Exec → `Idempotency-Key is required for start` / `action must be "start", "run", "poll", or "cancel"` | Wrong exec shape | Use `run` (one call) or `start` + `poll` with the header |
| Exec `run` returns `state: "running"` | Command outlived the ≤ 90 s wait | `poll` with the `sessionID` (or repeat `dev_exec` with the same `idempotencyKey`) |
| `railgrid sandbox exec` exits 124 | Still running at `--timeout` + 10 s | Shorter command, or poll yourself |
| `railgrid sandbox exec` → `<i>/<c> has no source revision; run 'railgrid sandbox sync …' first …` | No applied revision | `railgrid sandbox sync` |
| `railgrid sandbox exec` → `HTTP 404: exec is not declared for component worker` | The template declares no `exec` verb for that component (`worker`) | Use `railgrid sandbox logs`; test from an `application`/`simple-webapp` sandbox instead |
| Synced source changes are not visible; `Synced … restarted=false`, `railgrid sandbox status` shows the same `attempt N` | A non-Vite dev server keeps running the old code (reload rules cover only `package.json`/lockfiles) | `railgrid sandbox restart <i> <c>`, then probe a route only the new code has |
| `kubectl apply` changed `values.env` on a dev-mode Instance but the process still sees the old value, even after `railgrid sandbox restart` | The pod's env is read at container start; `restart` restarts the process only | `POST …/components/<c>/env {"env":{…}}` then `railgrid sandbox restart` (keep the kubectl change so it survives a re-render) |
| `railgrid app sync` → 422 `component "<c>" has no package.json in the workspace root; the Node.js (node) development sandbox needs one …` | An empty `worker` project, or an adopted non-Node tree | Commit a `package.json` (even a Node shim), or skip the sandbox: CI + promote still work |
| Sync 413 `sync request body exceeds 100663296 bytes; …` / `sync request too large: …` | Over 96 MiB body, 500 files, 25 MiB/file or 48 MiB decoded | Fewer files per request |
| `dev_sync` → `nothing was synced — component "<c>" cannot receive binary files …` | A target component's dev agent does not list `base64` in `syncEncodings` | Drop the binaries from the call; check `process` → `syncEncodings` |
| Workspace `read` → 413 `… above the 1048576-byte text limit …` / 422 `… is not UTF-8 text: it is a binary file …` | Opaque file in the run-sandbox workspace API | Not readable as text by design |
| Private app → 302 `/auth/apps/authorize` | Browser flow; not a failure | Programs: mint a `fapp_` token ([infrastructure.md](infrastructure.md) §5) or test inside the sandbox |
| Private app → 401 `{"error":"invalid_token",…,"tokenEndpoint":…,"instance":{…}}` | Sent a raw hub token or an invalid/expired/other-app `fapp_` token | `POST <hub>/auth/apps/token` with those `instance` coordinates |
| Private app → 403 `access_denied` | No grant for your account | Ask the owner to share (publishing grants) |
| Private app → 502 `unavailable` | Gate cannot reach the hub (or hub URL is plain http) and no cached verdict | Retry; operator checks the gate's hub URL |
| `POST /auth/apps/token` → 401 `invalid bearer token` | Not a hub user credential; ServiceAccount tokens (incl. the MCP connect token) are always refused | Use `$TOKEN` from `railgrid env`. For a job or worker there is no token: call the app's in-namespace Service ([infrastructure.md](infrastructure.md) §5 "Machine callers") |
| A `cron-job`/`worker` gets 302 or 401 from a private/restricted app's URL | The gate admits only people | Call `http://<status.apiServiceRef.name>:<apiPort>` (application) or `http://<appServiceRef.name>:<port>` (simple-webapp) instead; authenticate the route in the app |
| `cron-job` Instance `Ready`, but did it run? | Runs expose no status, logs or exit codes | Only indirect evidence: have each run write something the app shows; test the image locally first |
| Need a secret in an `application`/`cron-job` container, no input for it | No template takes a Secret reference; `env` maps are world-readable | [infrastructure.md](infrastructure.md) §4 "Your own secrets" |
| `POST /auth/apps/token` → 404 `instance has no published host` / 400 `malformed token request` | Not published / bad coordinates or `ttlSeconds` outside 60–900 | Publish first; fix the body |

### Agents

| Symptom | Cause | Fix |
|---|---|---|
| Agent run has no edge tools | Background run; only chat and channel runs get `edges__*` | Use chat/channel runs |
| Agent output says `This is a private railgrid app … No content was fetched.` | The app is private/restricted; `web_fetch` is anonymous and stops at the gate's redirect to sign-in | Agents can't mint app tokens: publish the endpoint publicly or pass the data in `task` |
| Agent MCP tools say "open the agents UI once" | Provider has not seen the workspace over the UI path yet | Open the Agents page once, retry |

## 2. Latency is not failure

Several states look like errors for the first minutes of a resource's life.
Decide "not yet" vs "wrong" before acting.

| Symptom | Usually | Confirm it is only latency |
|---|---|---|
| New `Instance` URL fails the TLS handshake (curl exit 35) | Certificate for that hostname still issuing; observed 0–9 min, don't debug before 10 | An existing instance on the same base domain serves 200; `openssl s_client -connect <host>:443 -servername <host>` shows no matching CN yet |
| `promotion.build.status: none` right after green CI | Package crawl hasn't seen the image (every 30 s for 10 min after a commit succeeds; else every 2 min) | `build.commitSHA` already equals your commit |
| `build.status: incomplete`, one component `missing`, CI green | Crawl has seen one package, not the other | Both jobs succeeded in `code__build_status` |
| Fresh project: repository `Provisioning` | Repository CR still being created | `.repository.ready` turns true |
| RepositoryCommit `Running`, Ready reason `RateLimited` | Waiting for the GitHub reset | Condition message `GitHub rate limit; retrying in <N>s` |
| Private URL returns 302 to `/auth/apps/authorize` | The gate wants a browser sign-in | Route and TLS are fine |
| Anonymous 302 for 10–20 s right after `railgrid app publish --mode public` | The gate hasn't picked up the access change | `GET …/publishing` already says `public`; retry |
| `railgrid app status` prints `Production:   - (promoted; the production instance has not reported yet, …)` | Project status lags the Instance by a few seconds | `kubectl get instance <p>-prod -o jsonpath='{.status.url}'` already has it |
| `Instance` Ready but no `status.url` | **Not latency**: `exposure: internal` | `kubectl get template <t> -o jsonpath='{.spec.exposure}'` |

Reference timeline for one App Studio project, measured on a dev hub
("commit → promotable" is CI time plus the package crawl):

| From → to | Took |
|---|---|
| `POST /api/projects` → repository ready | ~10 s |
| create → scaffold commit `Succeeded` | 15–40 s |
| create → dev instance Ready | ~2.5 min |
| `code__commit_files` → commit recorded | ~7 s |
| commit → `promotable: true` | 3.2–4.5 min (first sample set) |
| first promote → prod Ready | 1–1.5 min |
| first promote → new hostname serves TLS | 0–9 min (runs: ~0, ~0, 2 m 01 s, 2 m 16 s, 2 m 30 s, 2 m 50 s, ~3, ~4, 4 m 10 s, ~5, 5 m 20 s, 5 m 30 s, ~7, 8 m 51 s) |
| commit → `promotable: true` (second sample set, Node) | 2 m 45 s, 3 m 15 s, 3 m 51 s, 4 m 00 s, 4 m 40 s, 4 m 57 s, 5 m 01 s |
| promote → prod Ready (third sample set) | ~10 s (simple-webapp), 27 s (application) |
| assistant follow-up turn (one UI feature) | 58 s, committed 7 s later |
| commit → `promotable: true` (Go, multi-arch Railpack under QEMU) | 8 m 10 s — `build.status: none` with your SHA the whole time |
| promote → prod Ready (second sample set) | 42 s, 42 s, 50 s |
| assistant turn end → reconciler commit | 5–15 s |
