# CLI reference: `env`, `app`, `commit`, `sandbox`, `mcp`, edges

Sources: `pkg/cli/cmd/{root,hubclient,env,app,commit,sandbox,mcp,mcp_proxy,mcp_clients,edge,edgekind,edge_upgrade,kubeconfig,connect,ssh}.go`
and the generated per-command pages in `docs/cli/railgrid_*.md`. Login,
tokens, `use`, `whoami`, `org`, `workspace`, `skills` and the hub REST
surface are in [access.md](access.md); section 8 below is the pointer table
for every command this file does not cover.

Two global flags on every command: `--kubeconfig <path>` (default
`$KUBECONFIG`, then `~/.kube/config`) and `--insecure-skip-tls-verify`.
`railgrid version` prints `railgrid version <v>` plus git commit, build date,
go version and platform (`pkg/version`, set by ldflags from
`git describe --tags --always --dirty --match 'v*'`; `dev` when unset);
`make build-railgrid` builds from source. A command group given an unknown
subcommand fails with `unknown command "<x>" for "railgrid <group>"` (it no
longer prints the group's help and exits 0).

## 1. Shared behavior

- `env`, `app`, `commit`, `sandbox` and `mcp proxy` take `--org` and
  `--workspace` (display name, case-insensitive, or UUID), declared as
  persistent flags on the command, so they work after a subcommand too.
  Hub calls use the kubeconfig's `railgrid` context when there is one, else
  the current context (`no "railgrid" context found in kubeconfig — run
  'railgrid login' first` when neither exists). Default tenant: the
  workspace whose `clusterName` equals the `/clusters/<id>` of that
  context's server; `--org`/`--workspace` retarget the cluster to the
  named workspace.
- Errors: `you are not a member of any organizations`,
  `workspace "<w>" matches in N organizations; pass --org (org/workspace: …)`,
  `no workspace matches "<w>"`,
  `cluster <id> (kubeconfig context "<ctx>") is not a workspace of any org you belong to; run 'railgrid use'`,
  `cluster <id> is not a workspace of any org you can list (last error: …)`,
  `workspace "<w>" is not ready yet (no cluster assigned); try again shortly`.
  Any error containing `401` gets ` (token missing or expired; run 'railgrid login')` appended.
- Requests carry `X-Railgrid-Org`, `X-Railgrid-Workspace` and
  `User-Agent: railgrid-cli/<version>` (explicit because a hub's front-door
  proxy may 403 some library user agents). Non-streaming calls time out after 2 min. API
  failures print `<METHOD> <path>: HTTP <code>: <message>` (the Kubernetes
  `Status` message, prefixed with its `reason` when there is one; else
  `{"error"|"message"}`; else the body, else the status text).
- MCP calls (`commit`) use the workspace's `default` MCPServer token from
  `GET /api/orgs/{org}/workspaces/{ws}/mcpservers/default/connect` (a
  ServiceAccount token: no org-owned provider tools, see
  [mcp-and-edges.md](mcp-and-edges.md)). Not minted yet:
  `the workspace MCP token is not ready yet; retry shortly`.
- `-o/--output json` (`app list|create|status|sync|promote|publish`,
  `sandbox sync|status`) prints the raw API JSON; any other value is
  `unsupported output format "<o>" (want json)`.

## 2. `railgrid env`

```
railgrid env [--json] [--no-mcp] [--org O] [--workspace W]
eval "$(railgrid env)"
```

Prints single-quoted `export` lines, in this order:

| Var | Value |
|---|---|
| `HUB` | hub base URL |
| `CLUSTER` | kcp cluster of the workspace |
| `ORG`, `WS` | org and workspace UUIDs (the `X-Railgrid-Org` / `X-Railgrid-Workspace` values) |
| `TOKEN` | your bearer (static token, or the OIDC id_token captured from the exec plugin; OIDC tokens expire, re-run to refresh) |
| `AS` | `$HUB/clusters/$CLUSTER/apis/ai.railgrid.ai/v1alpha1` — the tenant kube API base of App Studio's kinds. Project CRs are `$AS/projects`; a verb is a kcp custom subresource, `$AS/projects/<name>/<verb>` or `$AS/studios/studio/<verb>` (section 4). There is no `/services/providers/app-studio/api` any more. |
| `MCP_URL`, `MCP_TOKEN` | aggregate endpoint and long-lived token from the connect endpoint |

- `--json`: one object `{hub, cluster, org, workspace, token, appStudioURL, mcpURL?, mcpToken?}`.
- `--no-mcp`: skip the connect call; no `MCP_*`.
- Connect failure is a warning on stderr (`railgrid env: warning: …`), not an
  error; a not-yet-minted token exports `MCP_URL` only. A summary line
  `railgrid env: hub=<hub> org=<name> (<uuid>) ws=<name> (<uuid>) mcp=ready|token not ready|unavailable|skipped`
  goes to stderr, so `eval` sees only exports.
- No bearer from the kubeconfig:
  `the kubeconfig credentials for context "<ctx>" do not produce a bearer token; run 'railgrid login'`.

## 3. `railgrid mcp`

Subcommands: `proxy`, `url`, `claude`, `codex`.

### `mcp proxy`

`railgrid mcp proxy [--mcpserver-name default] [--org O] [--workspace W]` is a
stdio MCP server for clients to launch (`claude mcp add railgrid -- railgrid mcp proxy`,
`codex mcp add railgrid -- railgrid mcp proxy`, or
`{"mcpServers":{"railgrid":{"command":"railgrid","args":["mcp","proxy"]}}}`;
the railgrid Claude Code plugin registers it as `railgrid`). It forwards to
`$HUB/services/mcpserver/<cluster>/apis/railgrid.ai/v1alpha1/mcpservers/<name>/mcp`
and relays each JSON-RPC line on stdin as one POST made with the
kubeconfig's credentials (so as you: org-owned providers included, OIDC
refreshed, the hub CA trusted, so no `--ca-file` or `NODE_EXTRA_CA_CERTS`),
writes every JSON-RPC message of the reply (JSON or SSE) to stdout, one per
line, and logs to stderr (`railgrid mcp proxy: forwarding to <url> as the
logged-in user` on first use). It echoes the hub's `Mcp-Session-Id` and the
negotiated `MCP-Protocol-Version` on later requests. Requests run
concurrently; `notifications/cancelled` aborts the matching POST; a 202/204
answer produces no output; at EOF it finishes the requests in flight and
exits, so `printf '<tools/call>\n' | railgrid mcp proxy` is a one-shot call.
Without `--org`/`--workspace` it needs no hub call to start: the target is
the `railgrid` context's cluster (`kubeconfig context "<ctx>" does not point
at a workspace; run 'railgrid use'` otherwise), resolved on the first
message, so a client started before `railgrid login` recovers without a
restart; after `railgrid use`, reconnect the client. A 401 reloads the
kubeconfig and retries once, then answers
`the hub rejected your credentials (HTTP 401); run 'railgrid login'`. Any
other failure (including `the hub sent no reply`) is a JSON-RPC error
(code -32000) on the request's id.

### `mcp url`

The other three subcommands hand out the workspace MCPServer's long-lived
ServiceAccount token instead (no org-owned provider tools; see
[mcp-and-edges.md](mcp-and-edges.md)).

`railgrid mcp url` takes exactly one of `--mcpserver-name <name>` (no default)
or `--edge <name>` (`specify exactly one of --mcpserver-name <aggregate-mcp-name> or --edge <edge-name>`,
`--mcpserver-name and --edge are mutually exclusive`). It reads the
kubeconfig's **current** context (`no current context in kubeconfig`) and
prints the endpoint on stdout, then Claude Code (`claude mcp add --transport http <name> "<url>" -H "Authorization: Bearer <token>"`),
Claude Desktop (`mcpServers` JSON with a `headers` block) and Codex
(`export RAILGRID_MCP_TOKEN=…`, `codex mcp add <name> --url … --bearer-token-env-var RAILGRID_MCP_TOKEN`)
snippets. The name is `railgrid-<mcpserver-name>`, or for `--edge`
`railgrid-kubernetes-cluster-<edge>` (there is no `--name` here).

- `--mcpserver-name`: fetches the token from the hub's connect endpoint and
  uses its `endpointURL`, so the snippets carry a working token on OIDC hubs
  too — but only when the `railgrid` context targets the same workspace as
  the current context (`current context targets <a>, the railgrid context <b>`).
  It ends with the stdio alternative:
  `claude mcp add <name> -- railgrid mcp proxy --mcpserver-name <name>`.
- `--edge`: the edge's `mcp` verb,
  `$HUB/clusters/<cluster>/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/<edge>/mcp`
  (Kubernetes edges only).
- Otherwise, and for `--edge`, it falls back to the kubeconfig's static
  token, and without one the snippets show `<your-token>`; notes go to stderr
  (`Could not fetch the MCP token from the hub (<err>).`,
  `The hub has not minted this MCP server's token yet; re-run shortly.`,
  `Your kubeconfig logs in through OIDC (no static token). 'railgrid env' prints a current TOKEN; it expires.`).
- A context whose server has no `/clusters/<name>`:
  `cannot determine cluster name from server URL "<u>"; expected path to contain /clusters/<name>`.

### `mcp claude`, `mcp codex`

Flags: `--mcpserver-name` (default `default`); `--name` (default
`railgrid-<mcpserver-name>`); `--ca-file`; `--dry-run`; `claude` also
`--scope user|local|project` (default `user`;
`--scope must be user, local or project, got "<s>"`). Both resolve the
endpoint for the current context's workspace through the connect endpoint
(`fetching MCP server "<n>" from the hub (run 'railgrid login' and 'railgrid use' first): …`,
`the hub has not minted a token for MCP server "<n>" yet; re-run shortly`),
probe the hub's certificate, then register the endpoint with the local
client, replacing an entry of the same name (`claude mcp remove <name> --scope <s>`
then `claude mcp add --transport http --scope <s> <name> <url> --header "Authorization: Bearer <token>"`;
`codex mcp remove <name>` then `codex mcp add <name> --url <url> --bearer-token-env-var RAILGRID_MCP_TOKEN`).
On success: `Added MCP server "<name>" (<url>) to Claude Code, <scope> scope.` /
`… to Codex.`, then `Start Claude Code:` / `Start Codex (it reads the MCP token from RAILGRID_MCP_TOKEN):`
with `export RAILGRID_MCP_TOKEN='…'` for Codex, and
`Then check the connection with: claude mcp list` / `codex mcp list`:

| Certificate | Claude Code | Codex |
|---|---|---|
| Publicly trusted | `claude` | `codex` |
| Signed by `--ca-file` | `NODE_EXTRA_CA_CERTS=<abs ca> claude` (or the `"env"` block of `~/.claude/settings.json`) | `CODEX_CA_CERTIFICATE=~/.railgrid/ca/<host>.pem codex`, a bundle of the system roots (`$SSL_CERT_FILE` or the distribution bundle) plus the CA; Codex 0.129.0+ |
| Neither | `NODE_TLS_REJECT_UNAUTHORIZED=0 claude`, which disables verification for the whole session, Anthropic API included | prints `codex` with a warning: Codex cannot skip verification, re-run with `--ca-file` |

A local hub from `railgrid dev init` needs `--ca-file <cluster>-ca.crt`
(`railgrid-hub-ca.crt` by default). A `--ca-file` that does not verify the hub
fails with `--ca-file <f> does not verify <host>: …` (`--ca-file <f> holds no PEM certificate`
for a non-PEM file); a non-certificate dial failure is `connecting to <addr>: …`.
`--dry-run`, or a client binary not on PATH, prints the two commands instead
(`<client> is not on your PATH. Run these once it is:`); the latter then exits
non-zero with `<client> not found on PATH; nothing was configured`.

## 4. `railgrid app` (alias `apps`)

App Studio ([app-studio.md](app-studio.md)) as you, through the hub's kcp
proxy: Project CRs are read with ordinary kube GETs on
`$AS/projects[/<name>]`, and everything else is a kcp custom subresource on
App Studio's APIExport — `$AS/projects/<name>/<verb>` for one project and
`$AS/studios/studio/<verb>` for workspace-wide verbs (the Studio singleton is
named `studio`; declared in `providers/app-studio/manifest.yaml`, served by
`providers/app-studio/api/dataplane_table.go`). Verbs used: `view`,
`promotion`, `publishing` (GET), `hydrate-workspace`, `sync-development`,
`promote`, `publishing` (POST), `publishing` (DELETE), and
`studios/studio/create-project`.

| Command | Flags | Behavior |
|---|---|---|
| `app list` (alias `ls`) | `-o json` | `GET $AS/projects` (the Project CRs); table `NAME DISPLAY NAME PHASE TEMPLATE REPOSITORY AGE` (`Deleting` while a deletion timestamp is set; `No projects found.`). `-o json` prints `{"items":[{name, displayName, description, phase, template, deleting, repository{ref}?, createdAt, updatedAt?}]}` projected from the CRs. |
| `app create <name>` | `--template`, `--display-name`, `--description`, `--prompt`, `--existing-repository <ref>`, `--wait`, `--timeout` (5m), `-o json` | Ensures the Studio exists (`GET $AS/studios/studio`, else `POST $AS/studios` with `spec.search.size`/`spec.browser.size` `small`; 409 is fine; otherwise `creating the workspace's App Studio Studio (is App Studio enabled in this workspace?): …`), then `POST $AS/studios/studio/create-project` with `{name, displayName, description, prompt, templateName, inferDevelopmentTemplate, existingRepositoryRef}`. `--template` is required unless `--prompt` is given (`--template is required (or pass --prompt to let App Studio choose)`; with `--prompt` alone `inferDevelopmentTemplate: true`, and the prompt does not start an assistant turn). `--existing-repository <ref>` adopts a code `Repository` you created first instead of creating one ([app-studio.md](app-studio.md)). A taken name is `project "<n>" not created (HTTP 409): <server message>`. Prints `project <n> created (phase <p>, template <t>, repository <ref>)`. `--wait` prints `railgrid app: waiting for repository and scaffold commit of <n>…` on stderr and polls `GET …/<n>/view` every 5 s until `repository.ready` and at least one `Succeeded` commit (the point from which clone and `railgrid commit` work; 17 s measured on one hub) — it does not wait for the dev instance, which `app status` reports as `Dev URL: -` for another 1–2 min; timeout error `project <n>: repository not ready with a succeeded commit after <t>; check 'railgrid app status <n>'`. |
| `app status <name>` | `-o json` | `GET …/view`, `…/promotion`, `…/publishing`; prints `Project:` (name, phase, template), `Repository:` ref + `ready=` + URL (+ `(message)` when not ready; after 2 min with no status message and no commit it adds `not ready for <age> with no status: the code provider is not reconciling (kubectl get repositories.code.railgrid.ai <ref> -o yaml has no status); wait for the operator, don't recreate the project`), `Commits:` the latest 3 (`<sha7> <phase> <message> <age> ago`), `Dev URL:`, `Promotion:` `promotable=<bool> build=<status> [commit=<sha7>] [missing=a,b]`, `Production:` phase + URL (`- (never promoted)` before the first promote; `- (promoted; the production instance has not reported yet, re-run in a few seconds)` for a few seconds after one), `Publishing:` `private` or `<mode> <url> [(not ready: <phase>)] [error: …] [grants=N]`. A failed promotion/publishing read prints `unavailable: <err>` on that line. `-o json` = `{project, promotion?, promotionError?, publishing?, publishingError?}`. |
| `app sync <name>` | `-o json` | `POST …/hydrate-workspace {}` (`hydrating the workspace: …` on failure; an instant `HTTP 502` bad-gateway page from the hub's front door while `GET $HUB/api/providers` shows `code` Ready is App Studio's handler, not git — SKILL.md 4.4 "When git → workspace is down" and section 8) then `POST …/sync-development {}` (`syncing the development instance: …`); progress on stderr. Prints `workspace: loaded from <repositoryRef>@<ref> (<sha7>), N written, M skipped` (+ `  skipped <path>` lines), then per component `<instance>/<component>: <phase>, N changed, M deleted, restarted=<bool>, revision R[, K skipped]` with `  skipped <path> (<reason>)` lines; a `binary-unsupported` reason adds `binary-unsupported: the component's dev agent does not accept binary files; update the instance to sync them`. `-o json` = `{hydrate, sync}` as returned. Use it instead of `railgrid sandbox sync` for App Studio dev instances. |
| `app promote <name>` | `--hostname-prefix`, `--commit <sha>`, `-o json` | `POST …/promote` with `values.expose.hostnamePrefix` and/or `commitSHA`; prints `promoted <n> to <instance> (commit <sha>, rollout <revision>)` and `  <component> built=<bool> <image>` per component. The prefix is locked after the first production deploy: pass it on the first promote, later the same value or nothing. Every promote rolls pods. |
| `app publish <name>` | `--mode public\|restricted\|private` (required; `--mode must be public, restricted or private`), `-o json` | `public`/`restricted` → `POST …/publishing {mode}`; `private` → `DELETE …/publishing` (unpublish, drop grants) and prints `<n>: private (unpublished; anonymous requests are redirected to sign-in, your own app tokens still work)` regardless of the response. `public`/`restricted` are accepted before prod is Ready; the text output then re-reads `GET …/publishing` every 2 s for up to 15 s, so a remaining `(not ready: <phase>)` means prod is not Ready yet. A ready `public` line adds `(anonymous requests may still be redirected to sign-in for ~20s)`. `-o json` prints the response as is (`{}` for an empty body), without waiting. |

Naming: the server uses an explicit name verbatim for the Project and its
code Repository (never suffixed) and answers 409 on a collision — often a
Repository left behind by a deleted project. Without a validated Git
connection the project starts with no repository, and one connected later
gets a suffixed name, so still read the ref from `app status`.

## 5. `railgrid commit <repositoryRef>`

```
railgrid commit <repositoryRef> [--branch main] [--remote origin] [--dry-run] [--org O] [--workspace W]
```

Records the commits on `HEAD` that are not on `<remote>/<branch>` through
the code provider's `code__commit_files` MCP tool on the workspace aggregate
(section 1; the same executor as the provider's `commit/v1` action, see
[code.md](code.md)), so they get a `RepositoryCommit` and are promotable.
Run inside a clone; never `git push` to a railgrid-managed repo.

1. Refuses a dirty tree: `uncommitted changes; run 'git add -A && git commit' first`.
2. `git fetch -q <remote> <branch>`. Same tree as HEAD →
   `railgrid commit: nothing to send; HEAD matches <remote>/<branch>` on stderr (exit 0).
3. Refuses when HEAD does not contain the base:
   `HEAD does not contain <remote>/<branch> (it moved upstream); run 'git rebase <remote>/<branch>' first`.
4. Diffs base..HEAD (`--raw --no-renames`): A/M/T → files, D →
   `deletePaths`. Symlinks (`<p> is a symlink; commit_files writes regular text files only`)
   and submodules (`… is a submodule; …`) are errors; a newly executable
   file is a warning (`railgrid commit: warning: <p> is executable; commit_files may not preserve the mode, which fails the final tree check`).
   No file differences at all: `no file changes between the base and HEAD`.
5. Message: local commit subjects oldest first (first = title, rest a
   bullet list), truncated to 512 bytes on a rune boundary.
6. Text (UTF-8, no NUL) goes as-is; other files base64 with
   `encoding: "base64"`, ≤ 25 MiB each (`<p> is binary and <size>; binary files are limited to 25 MiB each`),
   48 MiB for the whole change
   (`change is over 48 MiB in total (reached at <p>); split it into smaller commits`).
   Binaries are sent only if the tool's `inputSchema` declares
   `files.items.properties.encoding` (checked via `tools/list`); otherwise:
   `<paths>: binary file(s) not supported: the hub's code provider doesn't support binary files yet (code__commit_files accepts UTF-8 text only) — drop them from this change or commit them another way`.
7. Prints `railgrid commit: sending N file(s), M deletion(s) to <repo>@<branch>`
   on stderr and calls the tool. On `Succeeded` with a SHA: fetch again,
   require `<remote>/<branch>^{tree}` == `HEAD^{tree}`, then
   `git reset -q --hard <remote>/<branch>` so your branch carries the
   railgrid-recorded SHA. Prints the SHA on stdout and
   `railgrid commit: recorded <sha> (<commit URL>); local branch reset onto <base>` on stderr.
   Tree mismatch leaves the branch alone
   (`recorded <sha>, but <base>'s tree differs from your HEAD (someone else committed, or a file mode was not preserved); left your branch alone — reconcile with 'git rebase <base>'`).
   Any other phase: `commit not confirmed (phase=<p>); result: …` plus a
   `check: kubectl get repositorycommits.code.railgrid.ai -l code.railgrid.ai/repository=<repo>` hint.
   A tool error (e.g. queued behind a GitHub rate limit, see
   [code.md](code.md)) is `commit_files failed: <tool text>`.

`--dry-run` prints `write  <path> (<n> bytes[, binary])`, `delete <path>` and
`message:` followed by the message, without calling the hub (it still runs
the git steps above).

## 6. `railgrid sandbox` (alias `sbx`)

Data plane of a development-mode Instance (`<project>-dev` for App Studio):
the infrastructure provider's `instances/{verb}` custom subresources
`$HUB/clusters/<cluster>/apis/infrastructure.railgrid.ai/v1alpha1/instances/<i>/<verb>[?component=<c>]`
(verbs `env`, `exec`, `log`, `process`, `proxy`, `restart`, `runtime-status`,
`sync`, `workspace`; the component is always the `component` query
parameter, never a path segment; [infrastructure.md](infrastructure.md)
section 8). Production instances answer 409. Component paths are relative
to the component's `workspacePath` (application template: `api/` →
component `api`, `web/` → `web`; simple-webapp: the repo root → component
`app`; worker: component `worker`).

| Command | Flags | Behavior |
|---|---|---|
| `sync <instance> <component> [dir]` | `--restart auto\|always` (auto), `-o json` | Files under `dir` (default `.`): in a git work tree `git ls-files -co --exclude-standard`, else a walk skipping `node_modules/`, `dist/`, `.git/`; paths with a `.git`, `node_modules` or `.assistant-snapshots` segment and non-regular files are dropped (`no files to sync under <dir>`). **Authoritative**: reads `process`, sends `sourceRevision` = Unix seconds (or applied+1 if higher) and the sha256 digest, so it replaces the component's managed file set. Binaries go base64 only if the component's `process` status lists `base64` in `syncEncodings` (≤ 25 MiB each: `<p> is <size>; binary files sync at most 25 MiB each`; 48 MiB total: `sync is over 48 MiB in total (reached at <p>); sync a smaller directory or ignore large files`); otherwise skipped with `railgrid sandbox: skipping N binary file(s); <i>/<c>'s dev agent does not advertise base64 sync (update the instance to sync them): …` (`no text files to sync under <dir>` if nothing is left). `POST sync`; prints `<phase>: N changed, M deleted, restarted=<bool>, revision R`; a `reloadError` goes to stderr as `railgrid sandbox: reload error: …`. |
| `exec <instance> <component> -- <argv…>` | `--timeout` (120s, max 120s: `--timeout must be between 1s and 2m0s`), `--workdir` (relative to the component workspace) | Argument shape is checked (`usage: railgrid sandbox exec <instance> <component> -- <argv...>`, `exec needs argv after --`). Only for components whose template declares the `exec` verb (`application`, `simple-webapp`; a `worker` answers `HTTP 404: exec is not declared for component worker`; an instance not yet Ready answers `HTTP 409: exec is unavailable until the Instance is Ready and network phase is runtime`). Reads `process`; no applied revision → `<i>/<c> has no source revision; run 'railgrid sandbox sync <i> <c> <dir>' first (exec needs an authoritative sync)`. Then `POST exec {action: start, argv, workdir, timeoutSeconds, sourceRevision, sourceDigest}` with a random `Idempotency-Key` (`exec start failed: …`), and `{action: poll, sessionID}` every 1 s (`exec poll failed: …`). Prints stdout/stderr (`railgrid sandbox: output truncated` when clipped) and **exits with the command's exit code**; 124 with `exec still <state> after <t> (session <id>)` if still running at timeout+10 s; 1 with `railgrid sandbox: command ended in state "<s>" without an exit code` if it ended without one. No shell. |
| `logs <instance> <component>` | `-f/--follow` | `GET log`, streamed to stdout. `-f` re-reads it every 2 s and prints the new tail; a shrunk log prints `--- log restarted ---`. Ctrl-C stops. |
| `restart <instance> <component>` | | `POST restart`; prints `restarted <i>/<c>`. Restarts the process, not the pod: needed after syncing sources to a non-Vite dev server; it does not pick up `values.env` changes made with kubectl (use `env`). |
| `env <instance> <component> KEY=value…` | `--restart` | `POST env {env:{…}}` on the running dev process (`want KEY=value, got "<x>"`); prints `set K1, K2 on <i>/<c> (run 'railgrid sandbox restart <i> <c>' for the process to pick them up)`, or with `--restart` runs `POST restart` and prints `set … on <i>/<c> and restarted the process` (`env applied (…) but restart failed: …`). Live process only — keep the Instance's `values.env` in sync; no secrets (they travel and land in clear). |
| `status <instance> [component]` | `-o json` | Instance: `GET runtime-status` → `Phase:`, `URL:`, `Message:` and a `CONDITION STATUS REASON MESSAGE` table. Component: `GET process` → `Running: <bool> (configured=<bool>, attempt N)`, `Port: <p> (reachable=<bool>)`, `Source:` revision + short digest or `- (not synced authoritatively yet)`, and always a `Sync:` line — either the encodings, or `utf-8 only (binary files are not synced to this component)`. |

Notes:

- Exec never gets the app's env or secrets. The dev agent sets `HOME`
  (`/tmp`), `LANG`, `NPM_CONFIG_CACHE`, `PATH`, `PWD`, `TMPDIR` and
  `RAILGRID_EXEC_SESSION`, plus `PORT` (the component's dev server port) and
  `RAILGRID_COMPONENT` when it knows them. Name the port instead of relying
  on `$PORT`.
- Exec arguments are limited to 4096 bytes each and may not contain NUL
  (`start argv[N] must be non-empty, at most 4096 bytes, and contain no NUL`).
- `exec` on an App Studio dev instance (label `app-studio.railgrid.ai/project`,
  or a `Project` owner reference; by naming convention `<project>-dev` when
  the instance cannot be read) that reports no source revision prints
  `<i>/<c> has no source revision; run 'railgrid app sync <project>' first (exec needs an authoritative sync, and <i> is managed by App Studio project <project>, so 'railgrid sandbox sync' would replace its file set)`.
  Don't `railgrid sandbox sync` an App Studio instance: it replaces App
  Studio's managed files.
- A CLI sync and App Studio's own sync both write the component; App Studio
  renumbers past the CLI's revision and its next sync replaces the files
  (see [app-studio.md](app-studio.md), Dev sandbox).
- `status` shows `Source: - (not synced authoritatively yet)` and `exec`
  fails with `has no source revision` whenever a managed file no longer
  matches the synced manifest. On `simple-webapp` the start command itself
  does that: it appends `.railgrid-vite.config.mjs` to `.gitignore` on every
  (re)start, so syncing a `.gitignore` without that line loses the revision
  at the next restart (`--restart always`, `restart`, or an `auto` restart
  for `package.json`). Keep the line in `.gitignore` or leave `.gitignore`
  out of the directory you sync; a no-op re-sync restores the revision.

## 7. Edges: `edge`, `connect`, `disconnect`, `ssh`

Edges are the edges provider's kinds in your workspace
(`edges.railgrid.ai/v1alpha1`: `KubernetesCluster`, `LinuxServer`,
`MacOSServer`), read with the `railgrid` context's credentials. The CLI
addresses them by name across all three kinds; a name shared by several
kinds is `edge "<n>" is ambiguous: N edges share that name; qualify it with its type — kubernetes/<n> or server/<n>`
(the qualifier is the type `kubernetes|server|macos`, the kind, or the
resource; `unknown edge type "<q>" in reference "<r>" (want kubernetes, server or macos)`),
except that `connect`/`edge kubeconfig` prefer the cluster and `ssh` the
server. Not found: `edge "<n>" not found (searched KubernetesCluster + LinuxServer + MacOSServer)`.
No edges API at all:
`the edges provider is not enabled in this workspace (no edges.railgrid.ai API); enable it in the console's Providers page, then retry`.
Any command that asks for confirmation takes `-y/--yes`; without a TTY it
errors with `confirmation needed but stdin is not a terminal; pass --yes`.

| Command | Flags | Behavior |
|---|---|---|
| `edge create <name>` | `--type kubernetes\|server\|macos` (kubernetes; `unknown edge type "<t>" (want kubernetes, server or macos)`), `--labels k=v,…`, `--harness auto\|none\|claude,codex` (auto) | Creates the kind for the type (`creating edge "<n>": …`), prints `✓ Edge "<n>" created`, and for host edges a `Harness:` line (`--harness` is refused on a Kubernetes edge: `--harness is only meaningful on a host edge; use --type server or --type macos`; it is written to `spec.harness` and sticks, unlike the agent's flag). Waits up to 30 s for `status.joinToken` (`Warning: could not retrieve join token: …` then a hint to run `edge join-command`), then prints the join guide: install the CLI, then per type `helm install railgrid-agent oci://ghcr.io/railgrid/charts/railgrid-agent …` / `railgrid agent join --hub-url … --edge-name … --type kubernetes --token …` / `railgrid agent run …` (kubernetes), `railgrid agent join\|run … --type server` (server), `sudo railgrid agent join … --type macos --worker-user "$USER" [--cluster <id>]` / `railgrid agent run … --type macos` (macos). |
| `edge join-command <name>` | | Reprints the join guide (type from the kind); polls up to 10 s for a missing token (`join token not available for edge "<n>": …`). |
| `edge list` (alias `ls`; `edge` itself aliases `edges`) | `-o wide\|json\|yaml\|name` | Table `NAME TYPE PHASE CONNECTED AGENT VERSION AGE` sorted by name; `wide` adds `HOSTNAME LAST HEARTBEAT LABELS`; `json`/`yaml` print a `v1 List` of the objects; a kind the workspace's edges API does not serve is skipped. Empty: `No edges found. Create one with: railgrid edge create <name> [--type server]`. Not logged in: `not logged in — run: railgrid login --hub-url <hub-url>`. |
| `edge get <name>` (aliases `describe`, `show`) | `-o json\|yaml` | `Name, Type, Phase, Connected, Agent version, Hostname, Last heartbeat, Proxy URL` (externalized onto the hub host), `Created, Labels`, `Conditions:` (`type=status (reason): message`), then `Next: railgrid connect <n>   (or: railgrid edge kubeconfig <n> -o <file>)`, `Next: railgrid ssh <n>`, or `macOS hosts are service-only: reach them through the EdgeServices they publish (no kubectl or SSH).` |
| `edge upgrade <name>` | | Compares `status.agentVersion` with this CLI's version: `Agent "<n>" is up to date (<v>)`, else `Agent "<n>" is running <a>. Latest is <v>.` (`unknown (agent has not yet reported its version)`) followed by the steps: kubernetes → `railgrid agent upgrade <n>` or `helm upgrade railgrid-agent oci://ghcr.io/railgrid/charts/railgrid-agent --namespace railgrid-system --reuse-values …`; server → download the binary and `sudo systemctl restart railgrid-agent-<n>`; macos → download and `sudo launchctl kickstart -k system/com.railgrid.agent.<n>`. |
| `edge delete <name>` (aliases `rm`, `remove`) | `-y/--yes` | `Delete edge "<n>"? This cannot be undone. [y/N]` on stderr (`aborted` on no), then `Edge "<n>" deleted.`; the agent on it loses hub access. |
| `edge kubeconfig <name>` | `-o/--output <file>`, `--merge` (mutually exclusive) | Server = the edge's `k8s` verb, `$HUB/clusters/<cluster>/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/<n>/k8s` (kubectl appends `/api/v1/…` as the verb's tail), with the hub's TLS settings. Default: a standalone one-context kubeconfig `railgrid-<n>` on stdout (credentials copied from the `railgrid` user), or written 0600 to `-o` (`Kubeconfig written to <file>` on stderr). `--merge` adds cluster+context `railgrid-<n>` (referencing the `railgrid` user entry, so a re-login refreshes every edge) without switching: `Context "railgrid-<n>" added to <path>` / `Use it with: kubectl --context railgrid-<n> get nodes`. |
| `connect [<edge>]` | | Same merge as `--merge`, then makes `railgrid-<edge>` the current context: `Connected to edge "<n>": kubectl now uses context "railgrid-<n>".` / `Run 'railgrid disconnect' to return to the hub workspace.` No argument opens a picker of Kubernetes edges, connected first (`no interactive terminal; pass the edge name: railgrid connect <edge>`; `no Kubernetes edges in this workspace; create one with 'railgrid edge create <name>'`). Hub commands keep using the `railgrid` context while connected. |
| `disconnect` | | Makes `railgrid` current again: `Disconnected from "<ctx>": kubectl now uses the hub context "railgrid".` (`kubectl already uses the hub context "railgrid".`; `no "railgrid" context in the kubeconfig; run 'railgrid login'`). The `railgrid-<edge>` contexts stay for `kubectl --context`. |
| `ssh <name> [-- command [args…]]` | none | WebSocket dial of the LinuxServer's `ssh` verb, `wss://<hub>/clusters/<cluster>/apis/edges.railgrid.ai/v1alpha1/linuxservers/<n>/ssh`, with the kubeconfig's bearer in `Authorization` (the exec plugin runs, so OIDC logins work). Interactive (no `--`) needs a TTY (`stdin is not a terminal; use 'railgrid ssh <name> -- <command>' for non-interactive use`), puts it in raw mode and forwards resizes. With `--` the rest of the line is joined into `?cmd=<command>&stdin=1`: a piped or file stdin is forwarded as base64 `cmd` messages followed by `eof`, so `cat f \| railgrid ssh x -- "cat > /tmp/f"` copies a file; a terminal stdin sends `eof` at once so commands that read stdin do not hang. Output is written until the hub closes the socket; the remote exit status is not propagated. No `-L`/`-R` port forwarding, no scp. |

`connect`/`edge kubeconfig` errors: `edge "<n>" is a Linux server, not a Kubernetes cluster; use: railgrid ssh <n>`,
`edge "<n>" is a macos edge, not a Kubernetes cluster; it is service-only (no kubectl or SSH)`,
`edge "<n>" is not connected (phase <p>); start the agent on it and retry — 'railgrid edge join-command <n>' prints how`,
`edge "<n>" has no proxy URL in status yet; retry shortly`.
`ssh` errors: `edge "<n>" not found in this workspace (railgrid edge list)`,
`edge "<n>" is a Kubernetes cluster, not a Linux server; use: railgrid connect <n>`,
`edge "<n>" is a macos edge; SSH is only available for Linux server edges`,
`edge "<n>" is not connected; start the agent on it ('railgrid edge join-command <n>' prints how)`,
and a failed dial `connecting to hub SSH endpoint <url>: websocket: bad handshake (<status>[: <body>])`
— 401 is credentials, 403 RBAC, 502 no agent tunnel.

## 8. Everything else (pointer table)

Not documented here; the per-command pages are `docs/cli/railgrid_<cmd>.md`.

| Command | Where | One line |
|---|---|---|
| `login`, `logout`, `use` (aliases `switch`, `ctx`), `whoami` (alias `status`), `token`, `get-token` (hidden exec plugin) | [access.md](access.md) section 1 | Session: hub, org and workspace selection, bearer for curl |
| `org …`, `workspace …` (aliases `ws`, `workspaces`), `*/members …` | [access.md](access.md) section 1 | Organizations, workspaces and memberships over hub REST |
| `skills list\|install` | [access.md](access.md) section 1 | Installs `skills/<name>/` from GitHub into Claude Code / Codex |
| `agent run\|join\|install\|uninstall\|upgrade\|token create`, `install` | `docs/cli/railgrid_agent*.md` | Edge agent lifecycle, run **on the edge host** (what the join guide from `edge create` tells the operator to paste), not laptop workflow |
| `runner run` | `docs/cli/railgrid_runner_run.md` | Loopback coding runner (`--harness codex\|claude`) the edge agent supervises on a harness host; not a hub-driving command |
| `dev init\|update\|delete` | [access.md](access.md) section 10 | Local kind-based hub for development (`https://console.127.0.0.1.sslip.io:9443`, token `dev-token`) |
| `init` | `docs/cli/railgrid_init.md` | Runs a hub in-process; server side |
| `version`, `completion`, `docs` (hidden), `kcp-workspace` (hidden) | — | Build info, shell completion, `make docs-cli`, raw kcp `kubectl ws` navigation |

## 9. The newer `app` subcommands, and what still needs the verb

Added after the first `app` set; an older CLI answers `unknown command`, and
the verb column is what they call (`fc` and `$AS` from SKILL.md section 0;
details in [app-studio.md](app-studio.md)). MCP has no App Studio tools.

| Command | Behavior | Verb |
|---|---|---|
| `app preview <p> [--mode public\|restricted\|private]` | Shows or sets the dev preview's access; `public` prints the ~20 s convergence note; `app status` shows the same as `Preview:` | `GET\|POST\|DELETE $AS/projects/<p>/preview` |
| `app checkpoints <p>` | Template / Source / Production stages with state, reason and fix; `app status` lists the not-done ones under `Blocked:` | `GET $AS/projects/<p>/checkpoints` |
| `app files ls\|get\|put\|rm <p> [path] [local]` | Workspace files, binary-safe; `put` reads a local file or stdin (`--create-only` = `If-None-Match: *`), `get --out <file>` | `GET …/files`, `GET …/files-raw?path=`, `PUT\|DELETE …/files-content?path=` |
| `app sync <p> --from <dir>` | Pushes a local tree into `<p>-dev` through `infrastructure__dev_sync` (additive; binaries base64; nothing committed, nothing replaced) instead of hydrate + sync-development | MCP `infrastructure__dev_sync` (workspace MCP token, like `commit`) |
| `mcp call <tool> [json] [--args-file f\|-] [--list]` | One `tools/call` (or `tools/list`) through the proxy path, as you; prints structured content or the text, exits non-zero on a tool error | the aggregate MCP endpoint |

Still verb-only:

| You want to | Call |
|---|---|
| Grant one person a private preview / production | `POST $AS/projects/<p>/preview-grants` / `…/publishing-grants -d '{"user":"<email>","invite":true}'` |
| Sync the sandbox without hydrating from git | `POST $AS/projects/<p>/sync-development -d '{}'` (what `app sync` does second); `…/restart-development`, `…/development-logs` |
| Drive the assistant | `POST $AS/projects/<p>/create-session`, `POST $AS/sessions/<s>/turn`, `GET …/events` (SSE), `…/approval/<turn>` |
| Change the template of a prompt-only project | `POST $AS/projects/<p>/set-template -d '{"template":"simple-webapp"}'` |
| Rename or describe a project | `PATCH $AS/projects/<p>` (merge patch of `spec.displayName` / `spec.description`) |
| Re-seed scaffold files into an empty project, or add the missing build workflow to a non-empty one | `POST $AS/projects/<p>/scaffold` (non-empty workspace: writes only the template's workflow when absent, `seededWorkflow` in the reply; else `seeded: 0`) |
| Roll the workspace back to a commit | `POST $AS/projects/<p>/restore-workspace -d '{"commitSHA":"…","expectedSourceRevision":N}'` |
| Delete a project | `kubectl delete project <p>` (or `DELETE $AS/projects/<p>` with the UID precondition); annotate `ai.railgrid.ai/delete-repository=true` first to drop the GitHub repo too |
| Per-user approval mode | `GET\|PATCH $AS/projects/<p>/approval-mode` |

`railgrid app sync <p> --from <dir>` sends `infrastructure__dev_sync` through
the same path as `railgrid mcp proxy` (your login, your RBAC) from v0.2.15;
earlier builds used the workspace MCPServer token, which some hubs refuse for
`instances/sync`. `railgrid app files get <p> <path> --out <file>` writes a
binary to disk (stdout otherwise). A sandbox command given a project name
instead of `<project>-dev` gets a 404 that says so. `railgrid app status -o
json` carries `project`, `preview`, `publishing`, `promotionError` and
`checkpoints.items[]` (`key`, `label`, `state done|blocked|pending`, `reason`,
`remediation`); `railgrid app checkpoints -o json` is the same `items` array.

## 10. Loop from a terminal

```bash
eval "$(railgrid env)"
railgrid app create shop --template application --display-name Shop --wait
railgrid app status shop                          # repository ref, commits, dev URL
gh repo clone <owner>/<repository ref> shop && cd shop
git add -A && git commit -m "Add cart"         # commit locally, never push
railgrid commit <repository ref>
railgrid app sync shop                            # never `railgrid sandbox sync` an App Studio <project>-dev
railgrid sandbox exec shop-dev api -- node -e 'console.log(1)'
railgrid sandbox logs shop-dev api -f
railgrid app promote shop --hostname-prefix shop  # locked after the first promote
railgrid app publish shop --mode public
```
