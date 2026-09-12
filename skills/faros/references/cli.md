# CLI reference: `env`, `app`, `commit`, `sandbox`, `mcp url`

Sources: `pkg/cli/cmd/{env,app,commit,sandbox,mcp,hubclient,root}.go`. The
rest of the command tree (login, use, edge, ssh, agent, dev, …) is in
[access.md](access.md). `faros version` prints the build's version string
(`git describe --tags --always --dirty --match 'v*'` via ldflags,
`pkg/version`); `make build-faros` builds from source.

## 1. Shared behavior

- `env`, `app`, `commit`, `sandbox` each take `--org` and `--workspace`
  (display name, case-insensitive, or UUID), declared as persistent flags on
  the command, so they work after a subcommand too. Default: the workspace
  whose `clusterName` equals the `/clusters/<id>` of the kubeconfig's `faros`
  context (falls back to the current context). The only root-level flag is
  `--kubeconfig`.
- Errors: `workspace "<w>" matches in N organizations; pass --org (org/workspace: …)`,
  `no workspace matches "<w>"`,
  `cluster <id> (kubeconfig context "<ctx>") is not a workspace of any org you belong to; run 'faros use'`,
  `workspace "<w>" is not ready yet (no cluster assigned); try again shortly`.
  Any 401 gets ` (token missing or expired; run 'faros login')` appended.
- Requests carry `X-Faros-Org`, `X-Faros-Workspace` and
  `User-Agent: faros-cli/<version>` (explicit because Cloudflare 403s some
  library user agents). REST calls time out after 2 min. API failures print
  `<METHOD> <path>: HTTP <code>: <Status message>`.
- MCP calls (`commit`) use the workspace's `default` MCPServer token from
  `…/mcpservers/default/connect` (a ServiceAccount token: no org-owned
  provider tools, see [mcp-and-edges.md](mcp-and-edges.md)). Not minted yet:
  `the workspace MCP token is not ready yet; retry shortly`.
- `-o json` (`app`, `sandbox sync|status`) prints the raw API JSON; any
  other value is `unsupported output format "<o>" (want json)`.

## 2. `faros env`

```
faros env [--json] [--no-mcp] [--org O] [--workspace W]
eval "$(faros env)"
```

Prints single-quoted `export` lines, in this order:

| Var | Value |
|---|---|
| `HUB` | hub base URL |
| `CLUSTER` | kcp cluster of the workspace |
| `ORG`, `WS` | org and workspace UUIDs (the `X-Faros-Org` / `X-Faros-Workspace` values) |
| `TOKEN` | your bearer (static token, or the OIDC id_token captured from the exec plugin; OIDC tokens expire, re-run to refresh) |
| `AS` | `$HUB/services/providers/app-studio` |
| `MCP_URL`, `MCP_TOKEN` | aggregate endpoint and long-lived token from the connect endpoint |

- `--json`: one object `{hub, cluster, org, workspace, token, appStudioURL, mcpURL?, mcpToken?}`.
- `--no-mcp`: skip the connect call; no `MCP_*`.
- Connect failure is a warning on stderr (`faros env: warning: …`), not an
  error; a not-yet-minted token exports `MCP_URL` only. A summary line
  `faros env: hub=… org=… ws=… mcp=ready|token not ready|unavailable|skipped`
  goes to stderr, so `eval` sees only exports.
- No bearer from the kubeconfig:
  `the kubeconfig credentials for context "<ctx>" do not produce a bearer token; run 'faros login'`.

## 3. `faros mcp url`

`faros mcp url --mcpserver-name <name>` fetches the MCPServer token from
the hub's connect endpoint and uses its `endpointURL`, so the Claude Code /
Claude Desktop / Codex snippets carry a working long-lived token on OIDC
hubs too. It only does so when the `faros` context targets the same
workspace as the current context
(`current context targets <a>, the faros context <b>`). Otherwise, and for
`--edge`, it falls back to the kubeconfig's static token, and without one
the snippets show `<your-token>`; notes go to stderr
(`The hub has not minted this MCP server's token yet; re-run shortly.`,
`Your kubeconfig logs in through OIDC (no static token). 'faros env' prints a current TOKEN; it expires.`).
The flag is `--mcpserver-name` (there is no `--name`).

## 4. `faros app` (alias `apps`)

App Studio REST ([app-studio.md](app-studio.md)) as you.

| Command | Flags | Behavior |
|---|---|---|
| `app list` (alias `ls`) | `-o json` | `GET /api/projects`; table `NAME DISPLAY NAME PHASE TEMPLATE REPOSITORY AGE` |
| `app create <name>` | `--template`, `--display-name`, `--description`, `--prompt`, `--existing-repository <ref>`, `--wait`, `--timeout` (5m), `-o json` | `POST /api/projects` with `name`. `--template` is required unless `--prompt` is given (then `inferDevelopmentTemplate: true`; the prompt does not start an assistant turn). No flag for `existingRepositoryRef` — adopt through REST ([app-studio.md](app-studio.md)). `--wait` polls every 5 s until `repository.ready` and at least one `Succeeded` commit (the point from which clone and `faros commit` work); timeout error `project <n>: repository not ready with a succeeded commit after <t>; check 'faros app status <n>'`. |
| `app status <name>` | `-o json` | `GET` project, `promotion`, `publishing`; prints project/phase/template, repository ref + ready + URL (+ message when not ready; after 2 min with no status and no commit it adds `not ready for <age> with no status: the code provider is not reconciling …`), last 3 commits, dev URL, `promotable`/build status/commit/missing, production phase+URL, publishing mode/URL/grants. `-o json` = `{project, promotion, promotionError?, publishing, publishingError?}`. |
| `app sync <name>` | `-o json` | `POST hydrate-workspace {}` then `POST sync-development`; prints the ref and short SHA loaded (written/skipped counts), one line per component (`Synced, N changed, M deleted, restarted, revision R`), and each skipped file with its reason; a `binary-unsupported` skip adds a hint. Use it instead of `faros sandbox sync` for App Studio dev instances. |
| `app promote <name>` | `--hostname-prefix`, `--commit <sha>`, `-o json` | `POST promote` with `values.expose.hostnamePrefix` and/or `commitSHA`; prints instance, commit, rollout, per-component image. The prefix is locked after the first production deploy: pass it on the first promote, later the same value or nothing. Every promote rolls pods. |
| `app publish <name>` | `--mode public\|restricted\|private` (required), `-o json` | `public`/`restricted` → `POST publishing {mode}`; `private` → `DELETE publishing` (unpublish, drop grants) and prints `private (unpublished; …)` regardless of the response. `public`/`restricted` are accepted before prod is Ready; their output reflects the POST response only (`(not ready: Pending)`) — re-check with `app status`. |

Naming: the server uses an explicit name verbatim for the Project and the
Repository and answers 409 on a collision (see [app-studio.md](app-studio.md)),
so with `faros app create` the repository ref equals the name, despite the
help text's "An existing Repository of that name returns 409"; still read it from
`app status`.

## 5. `faros commit <repositoryRef>`

```
faros commit <repositoryRef> [--branch main] [--remote origin] [--dry-run] [--org O] [--workspace W]
```

Records the commits on `HEAD` that are not on `<remote>/<branch>` through
`code__commit_files`, so they get a `RepositoryCommit` and are promotable.
Run inside a clone; never `git push` to a faros-managed repo.

1. Refuses a dirty tree: `uncommitted changes; run 'git add -A && git commit' first`.
2. `git fetch -q <remote> <branch>`. Same tree as HEAD → `nothing to send; HEAD matches <remote>/<branch>` (exit 0).
3. Refuses when HEAD does not contain the base:
   `HEAD does not contain <remote>/<branch> (it moved upstream); run 'git rebase <remote>/<branch>' first`.
4. Diffs base..HEAD (`--raw --no-renames`): A/M/T → files, D →
   `deletePaths`. Symlinks (`120000`) and submodules (`160000`) are errors;
   a newly executable file is a warning (the mode may not survive, failing
   step 7).
5. Message: local commit subjects oldest first (first = title, rest a
   bullet list), truncated to 512 bytes on a rune boundary.
6. Text (UTF-8, no NUL) goes as-is; other files base64 with
   `encoding: "base64"`, ≤ 25 MiB each, 48 MiB for the whole change
   (`change is over 48 MiB in total (reached at <p>); split it into smaller commits`).
   Binaries are sent only if the tool's `inputSchema` declares
   `files.items.properties.encoding` (checked via `tools/list`); otherwise:
   `<paths>: binary file(s) not supported: the hub's code provider doesn't support binary files yet (code__commit_files accepts UTF-8 text only) — drop them from this change or commit them another way`.
7. On `Succeeded` with a SHA: fetch again, require `<remote>/<branch>^{tree}`
   == `HEAD^{tree}`, then `git reset -q --hard <remote>/<branch>` so your
   branch carries the faros-recorded SHA. Prints the SHA on stdout, the
   commit URL on stderr. Tree mismatch leaves the branch alone
   (`recorded <sha>, but <base>'s tree differs from your HEAD …`).
   Any other phase: `commit not confirmed (phase=<p>); result: …` plus a
   `kubectl get repositorycommits.code.faros.sh -l code.faros.sh/repository=<repo>` hint.
   A tool error (e.g. queued behind a GitHub rate limit, see
   [code.md](code.md)) is `commit_files failed: <tool text>`.

`--dry-run` prints `write <path> (<n> bytes[, binary])`, `delete <path>` and
the message, without calling the hub.

## 6. `faros sandbox` (alias `sbx`)

Data plane of a development-mode Instance (`<project>-dev` for App Studio),
`$HUB/services/providers/infrastructure/dataplane/clusters/<cluster>/instances/<i>/…`
([infrastructure.md](infrastructure.md) section 8). Production instances
answer 409. Component paths are relative to the component's
`workspacePath` (application template: `api/` → component `api`,
`web/` → `web`).

| Command | Flags | Behavior |
|---|---|---|
| `sync <instance> <component> [dir]` | `--restart auto\|always` (auto), `-o json` | Files: in a git work tree `git ls-files -co --exclude-standard`, else a walk skipping `node_modules/`, `dist/`, `.git/`; paths with a `.git`, `node_modules` or `.assistant-snapshots` segment are dropped. **Authoritative**: sends `sourceRevision` = Unix seconds (or applied+1 if higher) and the digest, so it replaces the component's managed file set. Binaries go base64 only if the component's `process` status lists `base64` in `syncEncodings` (≤ 25 MiB each, 48 MiB total); otherwise skipped with `faros sandbox: skipping N binary file(s); <i>/<c>'s dev agent does not advertise base64 sync (update the instance to sync them): …`. Prints `Synced: N changed, M deleted, restarted=…, revision R`; reload errors on stderr. |
| `exec <instance> <component> -- <argv…>` | `--timeout` (120s, max 120s), `--workdir` | Only for components whose template declares the `exec` verb (`application`, `simple-webapp`; a `worker` answers `HTTP 404: exec is not declared for component worker`). Reads `process`; no applied revision → `<i>/<c> has no source revision; run 'faros sandbox sync <i> <c> <dir>' first (exec needs an authoritative sync)`. Then `start` with a random `Idempotency-Key` and the applied revision/digest, polls every 1 s. Prints stdout/stderr and **exits with the command's exit code**; 124 if still running at timeout+10 s; 1 if it ended without an exit code. No shell. |
| `logs <instance> <component>` | `-f/--follow` | Prints the `log` verb. `-f` re-reads it every 2 s and prints the new tail; a shrunk log prints `--- log restarted ---`. Ctrl-C stops. |
| `restart <instance> <component>` | | `POST restart`; prints `restarted <i>/<c>`. Restarts the process, not the pod: needed after syncing sources to a non-Vite dev server; it does not pick up `values.env` changes made with kubectl (use `env`). |
| `env <instance> <component> KEY=value…` | `--restart` | `POST env {env:{…}}` on the running dev process (prints the applied keys and, without `--restart`, the restart command to run); `--restart` restarts right after. Live process only — keep the Instance's `values.env` in sync; no secrets. |
| `status <instance> [component]` | `-o json` | Instance: `GET …/status` (phase, URL, conditions). Component: `process` (running, port/reachable, source revision + short digest or `- (not synced authoritatively yet)`, and always a `Sync:` line — either the encodings, or `utf-8 only (binary files are not synced to this component)`). |

Notes:

- Exec never gets the app's env or secrets. A dev agent that advertises
  `syncEncodings` also sets `PORT` and `FAROS_COMPONENT` for exec; others set
  only `HOME LANG NPM_CONFIG_CACHE PATH PWD TMPDIR FAROS_EXEC_SESSION`. Name
  the port instead of reading `$PORT`.
- Exec arguments are limited to 4096 bytes each (`start argv[N] must be
  non-empty, at most 4096 bytes`).
- `exec` on an App Studio dev instance (label `app-studio.faros.sh/project`)
  that reports no source revision prints `run 'faros app sync <project>'
  first`; for other instances the hint is `faros sandbox sync`. Don't
  `faros sandbox sync` an App Studio instance: it replaces App Studio's
  managed files.
- A CLI sync and App Studio's own sync both write the component; App Studio
  renumbers past the CLI's revision and its next sync replaces the files
  (see [app-studio.md](app-studio.md), Dev sandbox).

## 7. Loop from a terminal

```bash
eval "$(faros env)"
faros app create shop --template application --display-name Shop --wait
faros app status shop                          # repository ref, commits, dev URL
gh repo clone <owner>/<repository ref> shop && cd shop
git add -A && git commit -m "Add cart"         # commit locally, never push
faros commit <repository ref>
faros app sync shop                            # never `faros sandbox sync` an App Studio <project>-dev
faros sandbox exec shop-dev api -- node -e 'console.log(1)'
faros sandbox logs shop-dev api -f
faros app promote shop --hostname-prefix shop  # locked after the first promote
faros app publish shop --mode public
```
