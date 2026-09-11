# Troubleshooting reference: error strings and latency

Exact strings are in backticks; `…` marks elided detail.

## 1. Symptom → cause → fix

### Login, CLI, hub REST

| Symptom | Cause | Fix |
|---|---|---|
| `no hub configured` | No hub URL | `--hub-url` or `FAROS_HUB_URL` |
| `no interactive terminal; pass --org and --workspace` | CI / no TTY | Pass both flags to `faros use` |
| `… (token missing or expired; run 'faros login')` | 401 from the hub; OIDC token expired | `faros login`; re-run `eval "$(faros env)"` for a fresh `TOKEN` |
| `cluster <id> (kubeconfig context "<ctx>") is not a workspace of any org you belong to; run 'faros use'` | Kubeconfig points at a workspace you can't list | `faros use`, or pass `--org`/`--workspace` |
| `workspace "<w>" matches in N organizations; pass --org …` | Same display name in two orgs | Add `--org` |
| Login via auth.faros.sh fails with GitHub `API rate limit exceeded for user ID …` while `gh api rate_limit` looks fine | Likely (not verified): the hub's GitHub OAuth app quota for that user is exhausted; the same OAuth app may back both Dex login and the code provider's "Connect with GitHub" connection, and `gh` uses a different token | Wait for the hourly reset; ask the operator to use separate GitHub OAuth apps for login and the code connection |
| 400 `no workspace selected` on a bare `/api` call | Workspace-scoped path without a cluster | Use `/clusters/<clusterName>/…` |
| 403 `address workspaces by cluster ID` | You used a `root:…` path | Resolve `clusterName` via `/api/orgs/{org}/workspaces` |
| 403 on a provider REST call (Kubernetes `Status` body) | Missing `X-Faros-Org` / `X-Faros-Workspace`, or not a member | Send both headers; check membership |
| 403 whose body mentions `cloudflare` or `error code: 1010` | Cloudflare blocked the HTTP client's user agent (Python's default), not authorization | Use `curl`, or set a browser-like `User-Agent`; real faros denials are `Status` JSON |
| jq: `Invalid string: control characters … must be escaped` | zsh `echo "$json"` expanded `\n` | Pipe straight to jq or `printf '%s'` |

### MCP

| Symptom | Cause | Fix |
|---|---|---|
| `faros mcp url` prints `Bearer <your-token>` | No token to embed: `--edge` on an OIDC hub, the `faros` context targets another workspace than the current context, or the MCPServer token is not minted yet (stderr says which) | `eval "$(faros env)"` for `MCP_TOKEN`/`TOKEN`, or `…/mcpservers/default/connect` |
| stderr `The hub has not minted this MCP server's token yet; re-run shortly.` / `faros env` `mcp=token not ready` | MCPServer token Secret not ready | Retry shortly |
| 401 on the MCP endpoint | Bearer missing, or not a member of that workspace | Use the connect token or your hub token; 429 = retry after 60 s |
| 503 on the MCP endpoint | Verifier unavailable, or (ServiceAccount bearers) the workspace-path lookup is down | Retry |
| `400 Accept must contain both 'application/json' and 'text/event-stream'` | Wrong `Accept` | Send `Accept: application/json, text/event-stream` |
| HTTP 200 but the call failed | Tool errors come back as `result.isError: true` with text in `result.content[0].text`; on success the field is absent, not `false` | Test `(.result.isError // false)`, never the status |
| A `<provider>__*` tool does not exist | Provider is org-scoped (BYO): federated only for a human bearer in a team workspace, never for the connect token (a ServiceAccount), and the org copy hides the platform copy of the same name | Call `tools/list` with your own hub `TOKEN`, or use kubectl / the provider's REST API ([mcp-and-edges.md](mcp-and-edges.md)) |
| `provider <method> response exceeds the 96 MiB limit; the result is too large to federate` | Tool result over the aggregate's cap | Ask for less (fewer files, no `binaryEncoding`) |
| `kuery__kuery_query` → `validating /properties/spec: … want one of "null, array"` (or, with a byte array, `cannot unmarshal array into Go value of type v1alpha1.QuerySpec`) | The tool's schema declares `spec` as bytes; no spec can pass | `POST $HUB/services/providers/kuery/api/query` with the same body |
| kuery query answers `{}`, `GET …/kuery/api/edges` → `{"edges":[]}` | `kuery` is not enabled in this workspace (its tools are federated regardless), or its edge engagement fails provider-side (`GET …/api/status` → `engagedEdges: 0`) | Enable the provider with its four claims; if `engagedEdges` stays 0 with connected edges, the operator checks the kuery provider logs |

### App Studio

| Symptom | Cause | Fix |
|---|---|---|
| `faros sandbox exec` on `<project>-dev` → `… has no source revision; run 'faros sandbox sync …' first` | App Studio changed files but hasn't synced authoritatively yet | `faros app sync <p>`, retry. Don't `faros sandbox sync` an App Studio dev instance — it replaces App Studio's managed files |
| Production build fails in `npm ci` with `EINTEGRITY … wanted sha512-… got sha512-…` after an assistant turn | The assistant's sandbox `npm install` never reached git and it hand-edited `package-lock.json` | Regenerate the lockfile locally (`npm install`), upload it (`PUT …/files/content`) or `faros commit` it |
| Binary file 404s / serves the HTML fallback in the dev preview but works in production | The sandbox's dev agent doesn't list `base64` in `syncEncodings`; `sync-development` still reports `Synced` | Verify binaries in production; the provider's operator must roll the sandbox to an agent with base64 sync |
| `create-readiness` → `connection-missing` | No validated code `Connection` | Create one ([code.md](code.md)) |
| Create → 409 `a code Repository named "<n>" already exists (possibly left by a deleted project); adopt it with existingRepositoryRef or choose another name` | An explicit `name` is the repository name and is never suffixed | Adopt with `existingRepositoryRef`, pick another name, or delete the repo |
| Create → `name must be a valid DNS label` | Explicit `name` is not a DNS label | Lowercase letters, digits, `-` |
| Fresh project: `repository.status: Provisioning`, `Creating repository "<n>".` | Repository CR still being created; latency | Poll `.repository.ready == true` |
| Repository still `Provisioning` after 2 min and `kubectl get repositories.code.faros.sh <n> -o jsonpath='{.status}'` prints nothing (no conditions, no finalizer) | The code provider's controllers are not reconciling at all (seen after a kcp outage: `error starting endpoint watcher … connection refused`, then silence); every new project, commit and checkout stalls | Operator restarts the code provider; nothing on the client side helps |
| `code__commit_files` → `repository "<name>" not found` on a new project | Project Ready, Repository not yet | Same: poll `.repository.ready` |
| `code__commit_files` on a prompt-created project → `repository "<project>" not found` | Repository name differs from the project name | Use `.repository.ref` |
| 409 `wait for or stop the active assistant run before <action>` | An assistant run owns the project (template, hydrate, sync, delete, file writes) | Wait until `turns/active` is 204 |
| `collaborationMode must be default or plan` / `review runs must use the dedicated /assistant/threads/{thread}/reviews endpoint` | Turns route: unknown mode / `review` (case is ignored) | Use `default`/`plan`; POST `…/reviews` for review |
| `PUT files/content` → 412 | `If-Match` version stale, `If-None-Match: *` and file exists, or `If-Match: *` and `file does not exist` | Re-read `files/content` for the version |
| `files/upload` → 409 `file "<p>" already exists; upload with overwrite=true to replace it` | Existing target | `overwrite=true` |
| 413 `file exceeds the 26214400-byte binary limit` / `text file "<p>" is too large: N > 262144 bytes` / `upload exceeds the 50331648-byte request limit` | Workspace limits: 25 MiB binary, 256 KiB text, 48 MiB per upload | Smaller files; split uploads |
| `files/raw` → 409 `file changed while it was being read; retry` | Concurrent write | Retry |
| DELETE project → 409 `repository "<r>" was adopted (imported) … never deletes adopted repositories; …` or `… is not owned by project "<p>"; it was not deleted` | `deleteRepository=true` on an adopted/foreign repo | Delete without `deleteRepository`; remove the repo via the code provider |
| Attachment → 413 `attachment is N bytes; maximum is M` | Over 25 MiB (any file), or the per-kind bound | Smaller file |
| Assistant: `binary files cannot be placed while this run uses an isolated coding sandbox; …` | `import_attachment`/`download_file` in run-sandbox mode | Upload in the Code tab (`files/upload`) |
| Assistant: `<url> returned a web page (text/html), not a file; …` | `download_file` got HTML | Use the direct file/raw URL, or attach the file |
| Assistant: `only binary files changed (…), and this workspace's Code provider does not accept binary commits yet; …` | `code__commit_files` does not declare `files[].encoding` | Binaries stay uncommitted in the workspace; text commits normally |
| `private preview inspection is unavailable: the app-studio deployment has no usable FAROS_HUB_PUBLIC_URL (chart value hub.publicURL, …)` | Deployment misconfiguration | Operator sets `hub.publicURL` |
| Dev sync 409 `workspace sync revision is older than the applied revision` | A plain/CLI sync moved the agent's applied revision past the sender's | Continue numbering from the applied revision; App Studio renumbers and retries once by itself |
| `production setting "expose.hostnamePrefix" is locked after the first deployment` | Production was already deployed with another prefix by an earlier promote (yours, the portal's, or the assistant's `promote_project`); App Studio never promotes by itself | Read `GET …/promotion` `.production`; promote with `{}` or the same prefix |
| `promotion.build.status: none` after pushing | Commit not recorded through faros | Use `code__commit_files` / `faros commit` |
| `promotion.build.status: incomplete` | A component has no `sha-<commit>` image | `code__build_status`, then `code__rebuild` |

### Code provider

| Symptom | Cause | Fix |
|---|---|---|
| `github: rate limited, resets in <dur> (at <time>): …` | The Connection's GitHub token is out of quota (per token; shared by every project's commits, checkouts, build checks and the package crawl) | Wait; commits retry by themselves |
| `github: forbidden — token lacks the required scope (403): …` | A scope problem | PAT scopes `repo`, `workflow`, `delete_repo`, `read:org`, `admin:public_key`, `read:packages` |
| `github: credential rejected (401): …` | Token revoked/expired | Replace the Secret behind the `Connection` |
| `RepositoryCommit "<n>" is queued behind a GitHub rate limit (…); the provider retries it until <time>, then marks it Failed. …` | Commit accepted, waiting for the reset (Ready reason `RateLimited`, up to 15 min after `startedAt`) | Watch the RepositoryCommit for `Succeeded`; do not resend |
| `RepositoryCommit "<n>" did not finish within the 1m15s wait (phase <p>); …` | Commit still running after the tool's wait (a tool error, not success) | Watch the RepositoryCommit |
| `commit message is N characters; the limit is 512 — shorten the body` | Message cap | Shorten the body, keep the subject |
| `file "<p>" is too large: N > M bytes` | 2 MiB text / 25 MiB binary per file; 48 MiB, 500 files per commit | Split the commit |
| `unsupported encoding "<e>": use "utf-8" or "base64"` / `invalid base64 content` | Bad `files[].encoding` or non-canonical base64 (line breaks rejected) | Standard padded base64, one line |
| `<paths>: binary file(s) not supported: the hub's code provider doesn't support binary files yet …` (`faros commit`) | `code__commit_files` does not declare `files[].encoding` | Drop the binaries from the change |
| `HEAD does not contain origin/<branch> (it moved upstream); run 'git rebase origin/<branch>' first` | Upstream moved (e.g. the reconciler committed) | `git rebase origin/<branch>`, re-run |
| `recorded <sha>, but origin/<branch>'s tree differs from your HEAD …` | Someone else committed, or a file mode was lost | Reconcile with `git rebase` |
| Many `Failed` commits with identical messages | The reconciler sends a fresh commit after each `Failed` one (e.g. a rate limit resetting more than 15 min after `startedAt`, a scope error) | Read one commit's Ready condition message and fix that cause |

### Infrastructure and data plane

| Symptom | Cause | Fix |
|---|---|---|
| `faros sandbox status` shows `Sync: utf-8 only` (and exec has no `PORT`) even on a brand-new sandbox after the provider was upgraded | The provider's sandbox agent image isn't the new release: an unpinned `faros-dev-agent:latest` stays cached on nodes (`IfNotPresent`) | Operator: set the chart's `development.agentImage` to the release tag or digest (release charts default to their own version); changing it re-renders every dev sandbox |
| `faros sandbox status/exec` → HTTP 502 Cloudflare `origin_bad_gateway` while the Instance shows `Ready` | The component's pod isn't running — e.g. `connections` names an instance that doesn't exist, so its Secret never appears | Check `spec.values.connections` against `kubectl get instances`; fix the name |
| `faros sandbox sync` → `skipping N binary file(s); <i>/<c>'s dev agent does not advertise base64 sync …` | That sandbox's agent can't take binaries | Nothing to do client-side; see the App Studio row above |
| Exec prints `Invalid URL … 127.0.0.1:undefined` | Code read `$PORT`, which exec doesn't always get | Name the port (8080 unless the template says otherwise) |
| Exec → 400 `start argv[N] must be non-empty, at most 4096 bytes` | One argument over 4 KiB | Pass data through a synced file instead of argv |
| `GET …/dataplane/…/components/<c>/status` → 405 `method GET not allowed for verb <c>/status` | Component status is the `process` verb | `GET …/components/<c>/process` (or `faros sandbox status <i> <c>`) |
| Instance `Valid=True` but an input has no effect (e.g. `connections` → no `DATABASE_URL`) | The template doesn't declare that key; undeclared keys are accepted silently | `kubectl get template <t> -o jsonpath='{.spec.schema.properties}'`; use a template that declares it |
| Instance `Valid=False` / `InvalidValues` | `spec.values` violates the template schema | `describe_template` |
| Instance Ready but no `status.url` | Template `exposure: internal`; not latency | Never poll for a URL |
| New instance URL fails TLS, curl exit 35, `sslv3 alert handshake failure` | Per-host edge certificate still issuing (base domain below the Cloudflare zone apex) | Wait (see §2; observed 0–9 min); operator fix: `*.<baseDomain>` edge cert |
| `openssl s_client … \| openssl x509 -noout -subject` → `Could not find certificate from <stdin>` | No certificate for that host yet — the same issuance latency | Wait; the command prints the host's CN once issued |
| Pod stuck in `CreateContainerConfigError` after setting `connections.database`/`cache` | Named instance's Secret does not exist (wrong name, other workspace, not provisioned) | Provision the `database`/`redis-cache` in the same workspace, or fix the name |
| Exec → `sourceRevision is required for start: component "<c>" reports no applied source revision — sync its workspace first …` | Nothing synced yet, or a reload pending | Any sync (`dev_sync`, `faros sandbox sync`), then retry |
| Exec → `Idempotency-Key is required for start` / `action must be "start", "run", "poll", or "cancel"` | Wrong exec shape | Use `run` (one call) or `start` + `poll` with the header |
| Exec `run` returns `state: "running"` | Command outlived the ≤ 90 s wait | `poll` with the `sessionID` (or repeat `dev_exec` with the same `idempotencyKey`) |
| `faros sandbox exec` exits 124 | Still running at `--timeout` + 10 s | Shorter command, or poll yourself |
| `faros sandbox exec` → `<i>/<c> has no source revision; run 'faros sandbox sync …' first …` | No applied revision | `faros sandbox sync` |
| `faros sandbox exec` → `HTTP 404: exec is not declared for component worker` | The template declares no `exec` verb for that component (`worker`) | Use `faros sandbox logs`; test from an `application`/`simple-webapp` sandbox instead |
| Synced source changes are not visible; `Synced … restarted=false`, `faros sandbox status` shows the same `attempt N` | A non-Vite dev server keeps running the old code (reload rules cover only `package.json`/lockfiles) | `faros sandbox restart <i> <c>`, then probe a route only the new code has |
| `kubectl apply` changed `values.env` on a dev-mode Instance but the process still sees the old value, even after `faros sandbox restart` | The pod's env is read at container start; `restart` restarts the process only | `POST …/components/<c>/env {"env":{…}}` then `faros sandbox restart` (keep the kubectl change so it survives a re-render) |
| `faros app sync` → Cloudflare 502 `origin_bad_gateway` from `sync-development` on a brand-new `worker` project | The workspace is empty; the sandbox has nothing to run | Commit files first (`faros commit`), then sync |
| Sync 413 `sync request body exceeds 100663296 bytes; …` / `sync request too large: …` | Over 96 MiB body, 500 files, 25 MiB/file or 48 MiB decoded | Fewer files per request |
| `dev_sync` → `nothing was synced — component "<c>" cannot receive binary files …` | A target component's dev agent does not list `base64` in `syncEncodings` | Drop the binaries from the call; check `process` → `syncEncodings` |
| Workspace `read` → 413 `… above the 1048576-byte text limit …` / 422 `… is not UTF-8 text: it is a binary file …` | Opaque file in the run-sandbox workspace API | Not readable as text by design |
| Private app → 302 `/auth/apps/authorize` | Browser flow; not a failure | Programs: mint a `fapp_` token ([infrastructure.md](infrastructure.md) §5) or test inside the sandbox |
| Private app → 401 `{"error":"invalid_token",…,"tokenEndpoint":…,"instance":{…}}` | Sent a raw hub token or an invalid/expired/other-app `fapp_` token | `POST <hub>/auth/apps/token` with those `instance` coordinates |
| Private app → 403 `access_denied` | No grant for your account | Ask the owner to share (publishing grants) |
| Private app → 502 `unavailable` | Gate cannot reach the hub (or hub URL is plain http) and no cached verdict | Retry; operator checks the gate's hub URL |
| `POST /auth/apps/token` → 401 `invalid bearer token` | Not a hub user credential; ServiceAccount tokens (incl. the MCP connect token) are always refused | Use `$TOKEN` from `faros env` |
| `POST /auth/apps/token` → 404 `instance has no published host` / 400 `malformed token request` | Not published / bad coordinates or `ttlSeconds` outside 60–900 | Publish first; fix the body |

### Agents

| Symptom | Cause | Fix |
|---|---|---|
| Agent run has no edge tools | Background run; only chat and channel runs get `edges__*` | Use chat/channel runs |
| Agent MCP tools say "open the agents UI once" | Provider has not seen the workspace over the UI path yet | Open the Agents page once, retry |
| `GET /api/schedules/{name}` → 405 | The route does not exist (only list, `PUT`, `DELETE`, `…/run`) | Read it from `GET /api/schedules` or `kubectl get schedules.agents.faros.sh <name>` |
| Agent created with `maxToolTurns`/`timeoutSeconds` but `spec.limits` is `{}` | `POST /api/agents` ignores those fields; only `PUT` (and `agents__update_agent`) sets them | `PUT /api/agents/<name>` with the limits after creating |
| Agent `status` stays `{}` after successful runs | Status is not populated on current builds | Judge by `GET /api/runs?agent=<name>` |

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
| first promote → new hostname serves TLS | 0–9 min (eleven runs: ~0, ~0, 2 m 50 s, ~4, ~5, 5 m 20 s, 5 m 30 s, ~7, 8 m 51 s) |
| commit → `promotable: true` (second sample set) | 2 m 45 s, 4 m 00 s, 4 m 40 s, 4 m 57 s, 5 m 01 s |
| promote → prod Ready (second sample set) | 42 s, 42 s, 50 s |
| assistant turn end → reconciler commit | 5–15 s |
