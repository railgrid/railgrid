# App Studio reference

Base URL for every route: `https://<hub>/services/providers/app-studio` (written `$AS` below). Every
call needs `Authorization: Bearer`, `X-Faros-Org`, `X-Faros-Workspace`.
Errors are Kubernetes `Status` JSON; lists are `{"items":[…]}`.

## 1. What App Studio owns and what it does not

- Owns: `Project`, `Session`, `Studio` CRs (`ai.faros.sh/v1alpha1`, all
  cluster-scoped), the workspace file store, the assistant, and the REST API.
- Does not own git (the **code** provider does) or runtime (the
  **infrastructure** provider does). App Studio holds no runtime kubeconfig.
- Has no MCP server. It is an MCP *client* that calls `code__*` tools on
  the workspace aggregate endpoint as the project's ServiceAccount. CLI:
  `faros app`, `faros sandbox`, `faros commit` drive these routes from a
  terminal ([cli.md](cli.md)).
- Depends on `code` and `infrastructure` being enabled in the workspace.

## 2. CRDs

### Project

`spec`: `displayName` (required), `description`, `repository`
(`repositoryRef`, `name`, `connectionRef`, `adopted`), `template.name`
(infrastructure Template; empty means no dev environment), `memory`
(`goals[]`, `requirements[]`, `constraints[]`), `sharing.preview.mode`
(`private|public`), `sharing.publishing.mode` (`private|shared|public`),
`environments[]` (`name`, `mode artifact|live`, `promotion manual|auto`,
`bindings[]`).

`bindings[]`: `name`, `provider`, `kind providerResource|providerReference`,
`resourceRef {apiVersion, kind, resource, name}`, `values` (opaque),
`allowedActions[] {name, version, schemaDigest, grantedBy, grantedAt, revoked, revokedBy, revokedAt}`.

`status`: `phase`, `updatedAt`, `environments[] {name, mode, phase, bindings[] {name, provider, phase, url, previewURL, outputs}}`.

Derived names: dev instance `<project>-dev`, prod instance `<project>-prod`
(truncated at 30 chars plus an 8-hex hash when needed). Environment names
`development` and `production`; binding names `dev` and `prod`.

### Session

Projection of one assistant thread: `spec.projectRef`, `spec.threadID`,
`spec.actorID`; `status.title`, `phase active|archived`, `activeTurnID`,
`activeTurnStatus`. Owned by the Project, purged on delete.

### Studio

Singleton `metadata.name: studio`. `spec.search {disabled, size small|medium|large}`
and `spec.browser {…}` provision a shared SearXNG and Playwright browser as
infrastructure instances for the workspace. Status reports each as
`Ready|Pending|Disabled`.

## 3. Route table

### Health and settings

```
GET   /healthz  /readyz  /metrics
GET   /api/projects/llm-settings                         → {provider,baseURL,model,configured,defaultModelID,models[{id,name,provider,baseURL,model,configured,default}]}
PATCH /api/projects/llm-settings                         {provider?,baseURL?,model?,apiKey?}   single-model form
POST  /api/projects/llm-settings/models                  {name,provider?,baseURL?,model,apiKey} → model entry
PATCH /api/projects/llm-settings/models/{model}          {name?,provider?,baseURL?,model?,apiKey?}
DELETE /api/projects/llm-settings/models/{model}
PATCH /api/projects/llm-settings/default                 {modelID}
POST  /api/projects/llm-settings/models/discover         list models the endpoint serves
POST  /api/projects/llm-settings/test                    live probe
GET   /api/projects/create-readiness                     → {gitConnection:{ready,status ready|provider-missing|connection-missing|validating|failed,connectionRef,message}}
GET   /api/projects/development-templates                → {templates:[{name,displayName,description,category,components{comp:path},previewAccessModes[],hasScaffold}]}
GET   /api/projects/import-repositories                  → {repositories:[{ref,name,connectionRef,htmlURL}]}
POST  /api/projects/plan                                 {prompt,templateName?} → {displayName,repositoryName,template,components,scaffold{repository,ref},availableTemplates[]}
```

Model settings are workspace-wide and OpenAI-compatible (Anthropic via
`https://api.anthropic.com/v1`, OpenAI, OpenRouter, custom). API keys are
write-only.

### Projects

```
GET    /api/projects                                     → {items:[ProjectView]}
POST   /api/projects                                     CreateProjectRequest → 201 ProjectView
POST   /api/projects/stream                              same, SSE events status{message} / created{Project} / error{message}
GET    /api/projects/{p}                                 ProjectView
PATCH  /api/projects/{p}                                 {displayName?,description?,sharing?}
DELETE /api/projects/{p}?uid=<uid>[&deleteRepository=true]   uid is required; names can be reused after async delete
GET    /api/projects/{p}/thumbnail[?revision=]           PNG
GET|PATCH /api/projects/{p}/memory                       {goals?,requirements?,constraints?}
PUT    /api/projects/{p}/template                        {template} → {template,components,project}; switching deletes the old dev instance and re-hydrates
GET    /api/projects/{p}/checkpoints                     → {items:[{key Template|Git|CI|Production,label,state done|pending|blocked|error,reason,remediation{kind auto|manual,tool,actionUrl,message}}]}
```

`CreateProjectRequest`: `name?`, `displayName?`, `description?`, `prompt?`,
`templateName?`, `inferDevelopmentTemplate?`, `connectionRef?`,
`existingRepositoryRef?`.

Naming: an explicit `name` must already be a DNS
label (`name must be a valid DNS label`) and is used verbatim for the Project
**and** the new code `Repository`. If a `Repository` of that name exists
(often one a deleted project left behind) the create is a 409:
`a code Repository named "<n>" already exists (possibly left by a deleted project); adopt it with existingRepositoryRef or choose another name`.
A taken Project name is not suffixed either (the Project create fails).
Only when `name` is omitted are names derived from `displayName`/the prompt
preflight and suffixed until free.

Delete: repositories survive by default (the claim is released). With
`deleteRepository=true` the code `Repository` App Studio created
for this project is deleted first, as the caller; its finalizer deletes the
GitHub repo. Refusals happen before any side effect: 400
`deleteRepository must be true or false`; 409
`repository "<r>" was adopted (imported) into this project and App Studio never deletes adopted repositories; …`;
409 `repository "<r>" is not owned by project "<p>"; it was not deleted`.

`ProjectView`: `name`, `uid`, `deleting`, `displayName`, `description`,
`phase`, `template`, `repository {ref, name, connectionRef, htmlURL, status, ready, commits[{name, phase, branch, commitSHA, commitURL, message, fileCount, createdAt, completedAt}]}`
(`ref` is the code `Repository` name every `code__*` call wants; it can differ
from the project name),
`memory`, `sharing`, `environments[]`, `createdAt`, `updatedAt`,
`sourceRevision`, `thumbnail`.

**`phase` is not the readiness gate for committing.** A newly created project
returns `phase: Ready` while its `Repository` is still being reconciled. That
window reports `repository.status: Provisioning` (sometimes with the message
`Creating repository "<name>".`, sometimes with no message at all) while the
reconciler finalizer `ai.faros.sh/instances` is absent, or for 10 minutes
after creation. If it lasts more than ~2 min and the `Repository` CR has no
`status` at all, the code provider is not reconciling
([troubleshooting.md](troubleshooting.md)).
`RepositoryMissing` means the CR existed and is gone. A
`code__commit_files` issued in that window fails with
`repository "<name>" not found`, which reads like a wrong `repositoryRef` and
sends you looking for a typo that is not there.

Gate on `repository.ready == true` (and `repository.htmlURL` being populated)
before the first commit:

```bash
until curl -s "$AS/api/projects/$P" -H "$A" $T | jq -e '.repository.ready == true' >/dev/null; do sleep 5; done
```

Observed on a fresh project, so treat it as a startup race rather than
evidence that projects routinely outlive their repositories. The same two
fields are still the honest check whenever you are unsure which objects
actually exist; `kubectl get repositories.code.faros.sh` confirms from the
other side.

### Files and workspace

```
GET    /api/projects/{p}/files                           → {files:[{path,size,…}]}   flat sorted tree
GET    /api/projects/{p}/files/content?path=<p>          → {path,content,version,binary,truncated,size}   all fields always present; 404 "file not found"
GET    /api/projects/{p}/files/raw?path=<p>[&download=1] raw bytes
PUT    /api/projects/{p}/files/content?path=<p>          body = raw file bytes
DELETE /api/projects/{p}/files/content?path=<p>          optional If-Match
POST   /api/projects/{p}/files/upload                    multipart
POST   /api/projects/{p}/hydrate-workspace               {ref?} → {repositoryRef,ref,commitSHA,written[],skipped[]}   git → workspace via code__checkout_repository
POST   /api/projects/{p}/restore-workspace               {commitSHA,expectedSourceRevision} → {…,written[],deleted[],sourceRevision,skipped[]?}
POST   /api/projects/{p}/scaffold                        re-seed template starter files into an EMPTY workspace
```

`files/content` GET: text beyond 256 KiB is `truncated` with no `version`; a
binary file (NUL byte or invalid UTF-8) has `content: ""`, `binary: true` and
a whole-file `version` (`sha256:<hex>`).

`files/raw`: `ETag` is the quoted version,
`If-None-Match` (list, `*`, weak prefix tolerated) → 304; `Cache-Control:
private, no-cache`, `X-Content-Type-Options: nosniff`,
`Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox`.
`Content-Type` from the extension (extra table for glb/gltf/obj/stl/usdz,
woff/woff2/ttf/otf, ico, mp3/wav/ogg, mp4/webm, zip; unknown →
`application/octet-stream`); html/xhtml/xml are served as
`text/plain; charset=utf-8`. `Content-Disposition` is `inline`, `attachment`
with `?download=1`. 409 `file changed while it was being read; retry`.

Writes share one gate: 503 `project workspace store is not
configured`, 409 `project is being deleted`, 409
`wait for or stop the active assistant run before changing project files`.
Every write marks the paths uncommitted (the reconciler commits them) and
schedules a dev sync, like an assistant edit.

- `PUT files/content`: body is the whole file (≤ 25 MiB binary, 256 KiB text;
  413 `file exceeds the 26214400-byte binary limit` or
  `text|binary file "<p>" is too large: N > M bytes`). No precondition =
  upsert. `If-None-Match: *` = create only (any other value 400
  `If-None-Match supports only * (create only)`). `If-Match: <version>`
  (quotes and `W/` stripped) = replace only that version; `If-Match: *` =
  must exist (412 `file does not exist`). Both headers → 400. Stale or
  existing target → 412. Answers 201 created / 200 replaced with
  `{path,size,version,binary}`.
- `DELETE files/content`: `If-Match` optional (absent or `*` = current
  version); 204; 404 `file not found`; stale → 412.
- `POST files/upload`: multipart, one or more `file` parts (the part
  filename is the path, `\` → `/`), optional `dir` (target directory, "" =
  root), optional `overwrite=true|false`. ≤ 100 files and 48 MiB of file
  bytes per request (413 `upload exceeds the 50331648-byte request limit`),
  25 MiB per binary. Every target is preflighted before anything is written:
  duplicate path 400, existing target without overwrite 409
  `file "<p>" already exists; upload with overwrite=true to replace it`.
  → 200 `{files:[{path,size,version,binary}]}`.

Other paths that change files: assistant tools (`create_file`,
`replace_file`, `edit_file`, `delete_file`, `move_file`, `import_attachment`,
`download_file`), hydrate, restore, scaffold. Hydrate writes files but does
**not** mark them for re-commit and does **not** delete workspace files the
ref no longer has (a commit that removed `index.html` leaves it in the
workspace and the sandbox; `DELETE files/content` it yourself); scaffold and
restore do mark files. Restore does
not fail when the checkout skipped paths; it returns them in `skipped` and
is meant to keep their workspace copies. Ambiguous: the code
passes the checkout's skip entries verbatim, and those carry reason suffixes
(`<path> (binary)`, ` (file too large)`), so they may not match the
workspace path and the file may be deleted anyway. Check `deleted[]` after a
restore of a commit with large or binary files.

### Dev sandbox

```
POST /api/projects/{p}/sync-development                  workspace → dev instance, per component
POST /api/projects/{p}/restart-development[?component=]
GET  /api/projects/{p}/development-logs[?component=]     streamed
GET  /api/projects/{p}/development-status                raw instance status
POST /api/projects/{p}/authorize-development-preview     → {target,ready,previewURL,message,reason,desiredAccess,observedAccess,accessConverged}
POST|DELETE /api/projects/{p}/preview-bridge/sessions[/{session}]   DOM-annotation iframe bridge
GET|POST /api/projects/{p}/preview                        {mode public|restricted}; DELETE resets to private
GET|POST /api/projects/{p}/preview/grants ; POST …/preview/grants/{grant} (revoke)
```

No start or stop route: the sandbox exists while `spec.template` is set. App
Studio has no exec route of its own; the assistant uses `exec_command`, and
you use the infrastructure data plane's `exec` on `<project>-dev` directly
([infrastructure.md](infrastructure.md) section 8; `faros sandbox exec`,
MCP `infrastructure__dev_exec`).
`sync-development` returns `{result: {<component>: {phase, changed, restarted, sourceRevision, sourceDigest, skipped?: [{path, reason}]}}}` — `reason` is `binary-unsupported` (the agent lacks base64 sync), `too-large` (over the per-file limit) or `sync-limit` (over the sync's total size or file count); `skipped` is present only when something was skipped. Runtime
mutations such as `npm install` inside the sandbox are not synced back.
Against a still-empty workspace (a `worker` project before its first commit)
`sync-development` can answer a Cloudflare 502 `origin_bad_gateway`: the
sandbox has nothing to run yet; commit or hydrate first.

Mixing syncs: the dev agent stamps a revision on plain syncs too (`faros sandbox sync` uses Unix-seconds revisions), which can
put the agent's applied revision ahead of App Studio's FileStore revision. On
a 409 containing `older than the applied revision` or
`already applied with a different digest`, App Studio reads the applied
revision from the component's `process` status, renumbers to applied+1 and
retries once; the offset is kept per component (in memory) and also applied
to exec and thumbnail checks. So a CLI sync does not wedge App Studio's
sync, but App Studio's next sync replaces whatever the CLI pushed.

Binaries reach a component only if its agent's status (data-plane verb `process`; `GET …/components/<c>/status` is 405) lists `base64` in
`syncEncodings` (cached 10 min); otherwise they are skipped with a log line
and text still syncs. Per sync: 500 files, 48 MiB decoded, 25 MiB per binary;
binaries past the bounds are dropped (logged), never the whole sync.

### Assistant

Thread → Turn → Item.

```
GET|POST   /api/projects/{p}/assistant/threads                    GET ?includeArchived&limit&cursor → {items,nextCursor}; POST {id?,title?}
PATCH|DELETE /api/projects/{p}/assistant/threads/{t}              {title?,archived?}
GET        /api/projects/{p}/assistant/threads/{t}/items          ?limit&beforeSequence
GET        /api/projects/{p}/assistant/threads/{t}/events         SSE; replays from sequence 1 unless Last-Event-ID; ends after turn.completed; closing does not cancel
POST       /api/projects/{p}/assistant/threads/{t}/turns          {content,clientUserMessageID,modelID?,collaborationMode default|plan (case-insensitive),skills?[],contextResources?[],contentParts?[]} → {thread,turn,continuationOfTurnID?}
POST       /api/projects/{p}/assistant/threads/{t}/reviews        {target,clientUserMessageID,modelID?,skills?}  read-only Review turn
GET        /api/projects/{p}/assistant/threads/{t}/turns/active   204 when idle (404 until the thread exists); check it before `PUT files/content`, which is 409 during a turn
GET        /api/projects/{p}/assistant/threads/{t}/turns/{turn}   {turn,effectiveSettings?}
POST       …/turns/{turn}/steer                                    {content,clientUserMessageID}
POST       …/turns/{turn}/interrupt                                {clientRequestID} → {turnID,status}
POST       …/turns/{turn}/continue
POST       …/turns/{turn}/approval  and  …/turns/{turn}/input      {requestID,decision allow|deny} / {requestID,answer|answers}
GET|PATCH  /api/projects/{p}/assistant/approval-mode              {mode on_request|always_ask|never}
GET|POST   /api/projects/{p}/assistant/attachments                POST multipart field `file` (+clientAttachmentID, draft) → {id,filename,contentType,kind,sizeBytes,sha256,createdAt,draft?,expiresAt?}
GET|DELETE /api/projects/{p}/assistant/attachments/{a}
```

`collaborationMode`: trimmed and lowercased before use, so `Default`/`PLAN`
work; empty = `default`. On the turns route `review` is 400
`review runs must use the dedicated /assistant/threads/{thread}/reviews endpoint`,
anything else 400 `collaborationMode must be default or plan`.

Attachments: any file type is accepted. `kind` is `image`
(PNG/JPEG/WebP ≤ 8 MiB, magic bytes checked), `text` (`.txt`/`.md` UTF-8
≤ 1 MiB), else `file` — including larger images and text, JSON, models,
fonts (≤ 25 MiB; type from the declared type or extension, else
`application/octet-stream`). Over-size → 413
`attachment is N bytes; maximum is M`. Per turn: 8 attachments, 50 MiB total,
current-turn images ≤ 20 MiB. Unbound drafts: 128 MiB and 64 per project.
`file` attachments reach the model as metadata only (filename, ID, type,
size, and a hint to call `import_attachment`); their bytes are never sent.

Thread event `project.committed`: appended to the turn of the
project's latest assistant run when the reconciler settles a commit;
payload `{commitSHA, commitURL?, branch?, repositoryRef, files[]}` (files
include deletions). Projects without an assistant thread get none.

Event stream shape: each `data:` line is
`{threadID, turnID?, sequence, type, itemID?, payload:{thread|turn|item}, createdAt}`
with `type` ∈ `thread.created`, `turn.started`, `item.started`, `item.delta`,
`item.completed`, `plan`, `turn.completed`. An `item.completed` carries
`.payload.item.type` ∈ `userMessage`, `agentMessage` (`.content`), `plan`,
`dynamicToolCall` (`.data = {kind inspect|edit|…, title, target, status
succeeded|failed, severity}`); `turn.completed` carries
`.payload.turn.status`. To list what the assistant did:

```bash
sed -n 's/^data: //p' events.log | jq -r 'select(.type=="item.completed") | .payload.item | select(.type=="dynamicToolCall") | .data | [.status,.kind,.title,(.target|tostring)] | @tsv'
```

Turn statuses: `in_progress`, `completed`, `failed`, `interrupted`. A provider
restart interrupts the active turn; resume from items plus the event stream.
Spend guards: 200 iterations per turn, 2,000,000 rollout tokens, an org
monthly USD cap (default 100). Exhaustion fails the turn with
`iteration_limited`, `budget_limited`, or `org_spend_cap_exceeded`.

Native assistant tools (not MCP): `plan_project_changes`,
`check_project_readiness`, `prepare_project_deployment`, `get_runtime_status`,
`get_preview_url`, `inspect_development_preview`, `interact_development_preview`,
`get_runtime_logs`, `restart_runtime`, `set_runtime_env`, `exec_command`,
`ask_follow_up`, `define_initial_project_plan`, `create_file`, `replace_file`,
`edit_file`, `delete_file`, `move_file`, `import_attachment`,
`download_file`, `select_project_template`,
`commit_project_files`, `web_search`, `web_fetch`, `ls`, `read_file`, `glob`,
`grep`, `load_skill`, `read_skill_resource`, `read_attachment`,
`check_project_build`, `get_build_logs`, `rebuild_project`,
`get_project_checkpoints`, `promote_project`, `inspect_development_templates`,
`verify_development_runtime`, and allow-listed `browser_*` tools. The model is
instructed never to commit unless asked.

Binary placement tools, write risk, same approval
and dirty/sync handling as `create_file`; create-only unless
`overwrite: true` (`<path> already exists; pass overwrite=true to replace it or choose another path`):

- `import_attachment {attachmentID, path, overwrite?}` copies a chat
  attachment (≤ 25 MiB) into the workspace.
- `download_file {url, path, overwrite?}` fetches one direct http(s) URL:
  60 s timeout, ≤ 5 redirects (http/https only), no proxy, no URL
  credentials, ≤ 25 MiB, empty body refused. The dial guard refuses loopback,
  private (incl. `fc00::/7`), link-local, multicast, unspecified, and
  `0.0.0.0/8`, `100.64.0.0/10`, `192.0.0.0/24`, `198.18.0.0/15`,
  `240.0.0.0/4`, `fec0::/10`, `64:ff9b::/96`, `64:ff9b:1::/48` (same guard as
  `web_fetch`). A `text/html`/xhtml response is refused unless the target
  path ends `.html`/`.htm`: `<url> returned a web page (…), not a file; …`.
- Both refuse while a per-run coding sandbox is active:
  `binary files cannot be placed while this run uses an isolated coding sandbox; ask the user to upload the file in the Code tab (or attach it again after this run) instead`.

`read_file` on a binary returns no content but a complete version, so
`delete_file`/`move_file` can act on it. Private preview inspection with no
usable `FAROS_HUB_PUBLIC_URL` fails with
`private preview inspection is unavailable: the app-studio deployment has no usable FAROS_HUB_PUBLIC_URL (chart value hub.publicURL, an absolute HTTPS origin such as https://hub.example.com); ask the platform operator to set it`
(an operator fix, not an app bug).

MCP tools the assistant consumes on the aggregate: `code__commit_files`,
`code__checkout_repository`, `code__build_status`, `code__rebuild`,
`infrastructure__list_templates`, `infrastructure__describe_template`,
`infrastructure__provision`, `infrastructure__list_instances`,
`infrastructure__get_instance`, `databricks__list_tables`,
`databricks__describe_table`, `agents__run_agent`, `agents__get_run`,
`agents__list_runs`, `agents__list_agents`.

### Build, promote, release

```
GET  /api/projects/{p}/promotion   → {template,instance,productionSchema,immutableProductionInputs[],productionValues,requestedRolloutRevision,observedRolloutRevision,promotable,
                                     build:{status built|incomplete|none|unsupported,commitSHA,components[{name,imageInput,built,image,digest,tag}],missing[],run{…}},
                                     production:{phase,url}}
GET  /api/projects/{p}/releases    → {items:[{name,phase,branch,commitSHA,commitURL,message,createdAt,completedAt,releaseID,deployable,live,missing[],components[]}]}
POST /api/projects/{p}/promote     {values?,commitSHA?,releaseID?} → {environment,instance,rolloutRevision,commitSHA,releaseID,components[]}
```

The promote response does not embed the project; re-read
`GET /api/projects/{p}` for project state.

Build resolution: the newest successful `RepositoryCommit` CR for the
project's repository gives `commitSHA`; for every launchable component the
code provider's `Package` CRs must contain a version tagged exactly
`sha-<commitSHA>`; its digest is what gets deployed. Commits made outside
faros produce no `RepositoryCommit` and are therefore invisible here.
Workflow file: the template's `spec.development.build.workflowPath`
(`.github/workflows/build.yaml` for shipped scaffolds). When the template
declares none, App Studio tries `faros-app-studio-build.yml`, then
`build.yaml` if that call errors.

Promote writes the `production` environment binding: instance
`<project>-prod`, `farosMode: production`, user values merged, image inputs
set to digests, fresh `farosRedeployRevision`, a `dockerconfigjson` pull
Secret `<instance>-registry` minted from the code Connection token. Platform
owned and always overriding: `name`, `farosMode`, image inputs,
`farosRedeployRevision`, `farosCluster`, `credentialsSecretName`, `access`
(managed by publishing). `expose.hostnamePrefix` is a first-deploy input.

### Publishing

```
GET|POST|DELETE /api/projects/{p}/publishing            GET → {published, publication:{name,uid,mode,host,url,ready,phase,target}}; POST {mode public|restricted}; DELETE = private and drop grants
GET   /api/projects/{p}/publishing/members               workspace members with rbacIdentity
GET|POST /api/projects/{p}/publishing/grants             POST {user,invite?}   invite = email pre-provisions a pending User
POST  /api/projects/{p}/publishing/grants/{grant}        revoke
```

Mechanics: the prod instance's `spec.access` flips in place; the
infrastructure access gate enforces it; invitations are a per-app
ClusterRole `faros-app-access.<instance>` (rules: `get` on
`instances/access` for that name, and kcp `access` on nonResourceURL `/`)
plus one ClusterRoleBinding per member, subject `faros:<email>`.

### Integrations (provider actions)

```
GET    /api/projects/{p}/integrations                    → {items:[…]}
POST   /api/projects/{p}/integrations                    {environment?,alias,provider,kind:"providerReference",resourceRef{apiVersion,kind,resource,name},allowedActions[{name,version,schemaDigest}],consentAccepted?}
PATCH  /api/projects/{p}/integrations/{alias}            {allowedActions[],consentAccepted?}
DELETE /api/projects/{p}/integrations/{alias}
POST   /api/projects/{p}/integrations/{alias}/invoke     {action,actionVersion,input} → {requestID,provider,action,actionVersion,resourceRef,result|error{code,message,retryable}}
       (also …/invoke/{action}, …/actions, …/actions/{action})
```

Alias regex `^[A-Za-z_][A-Za-z0-9_-]{0,62}$`. Grant creation re-reads the
caller-scoped `GET /api/providers` catalog and requires exact provider,
action, version, bound resource, and `schemaDigest`; deprecated actions are
refused; `consentAccepted` is required when the catalog says so. Invoke
re-verifies the digest against the live catalog (409 on drift), then
forwards to
`POST $HUB/services/providers/{provider}/actions/clusters/{cluster}/{resource}/{name}/{action}/{version}`
with a two-minute budget and optional `Idempotency-Key`, `X-Request-ID`,
`X-Faros-Action-Deadline-Ms`. Only shipped action today: Databricks
`query_table/v1` (sync, read-only, `columns` ≤ 64, `limit` 1..100).

In-app SDK: `@faros/actions-node` (alias for `@crwilhit/faros-actions-node@0.1.0`).
`createActionsClient({baseURL: FAROS_ACTIONS_BASE_URL, project: FAROS_PROJECT, tokenFile: FAROS_ACTIONS_TOKEN_FILE})`
then `faros.integration('<alias>').invoke('query_table/v1', {...})`.
Server-side only; the runtime exchanges a projected bootstrap token for a
10-minute workload token whose RBAC is exactly the materialized grants.

### Skills

```
GET  /api/projects/{p}/assistant/skills                  → {skills:[{id,name,description,scope,packageName,enabled,editable,version,digest,contentDigest,resources[]}],catalogDigest,warnings[]}
GET  /api/projects/{p}/assistant/skills/detail?id=
POST /api/projects/{p}/assistant/skills/project          create a project skill package
POST /api/projects/{p}/assistant/skills/project/import
GET|PUT|DELETE /api/projects/{p}/assistant/skills/project/{packageName}    PUT/DELETE need expectedDigest
GET  /api/projects/{p}/assistant/skills/project/{packageName}/export
POST /api/projects/{p}/assistant/skills/activation       {id,enabled}
```

Scopes: bundled (embedded, read-only), provider
(`CatalogEntry.spec.assistantSkills`, qualified `providers/<provider>/<package>`),
project (`.agents/skills/<package>/SKILL.md` with activation state in
`.agents/skills/.faros-catalog.json`). Frontmatter supports `name` (≤ 64 B)
and `description` (≤ 1024 B) only; `context`, `agent`, `model` are rejected.
Limits: 32 KiB per skill, 64 resources, 4 MiB per package, 64 packages
default. Skills are guidance only; they cannot grant tools or permissions.
The portal workbench only browses and toggles; create, edit, import, export,
delete exist on the API alone.

## 4. Templates, scaffolds, AGENTS.md

Templates are infrastructure `Template` CRs. Development-capable ones
declare `spec.development` with `components.<name> {workspacePath, imageInput, devImage, workingDir, startCommand, port, reload}`,
`scaffold {repository, ref}`, and `build.workflowPath`.

| Template | Components | Scaffold |
|---|---|---|
| `application` | `web` → `web/`, `api` → `api/` (+ Postgres, access gate) | `github.com/faroshq/faros-scaffold-application@v0.1.3` |
| `simple-webapp` | `app` → `.` | `github.com/faroshq/faros-scaffold-simple-webapp@v0.1.3` |
| `worker` | one component, no URL | none |
| `universal-coding-sandbox` | scratch | none |

Scaffold fetch is a tarball download (400 files, 8 MiB total, 1 MiB per file,
text only) seeded only into an empty workspace and marked uncommitted so the
reconciler lands it as the first commit. Both shipped scaffolds (v0.1.3)
include a root `AGENTS.md` stating the runtime contract,
`.github/workflows/build.yaml` (smoke test, then one Railpack image per
component pushed as `sha-<commit>` and `latest`, multi-arch), and
`package.json` files with both `dev` and `start` scripts. CI smoke-tests
`/api/health` (application) and `/` — keep them answering.

The same reconciler commits whatever the workspace holds that is not yet in
git whenever the project goes idle — in practice 5–15 s after every assistant
turn that changed files, with the message `Update N files in <dirs>` and a
file list. There is no switch for it and asking the model not to commit does
not stop it.

Reconciler commit behavior:

- Paths are settled (marked committed) only when `code__commit_files`
  returns phase `Succeeded` with a SHA; anything else leaves them dirty.
- When the tool reports the commit `is queued behind a GitHub rate limit` or
  `did not finish within` the wait, the reconciler remembers that
  `RepositoryCommit` by name and re-reads it every 60 s instead of resending;
  newer edits wait. `Succeeded` → settle and announce; `Failed` or gone → a
  fresh commit next pass. The memory is in-process (single replica), so a
  restart costs at most one resend.
- One commit is bounded like the code provider: 500 files, 48 MiB decoded,
  2 MiB per text file, 25 MiB per binary; the rest waits for the next pass.
  Binaries go base64 only if `code__commit_files` advertises `encoding`
  (cached 10 min per cluster); otherwise they stay uncommitted (logged once),
  as do over-limit files, without blocking text.
- Generated messages are capped at 480 characters (subject ≤ 200, ≤ 20
  listed paths, then `- … and N more`), under the 512-character CRD limit.
- Each settled commit is posted to the thread as `project.committed`.

The assistant's `commit_project_files` follows the same limits (48 MiB total,
2 MiB text); when `code__commit_files` does not advertise `encoding` it
commits the text, returns `skippedBinaryPaths`, and fails only when binaries
were the whole change: `only binary files changed (…), and this workspace's Code provider does not accept binary commits yet; they stay uncommitted in the workspace`.

App Studio injects the workspace-root `AGENTS.md` (32 KiB cap) into every
model sample. Project memory is injected too.

## 5. Sandbox model

The Template is the sandbox. A dev instance is the same graph rendered with
`farosMode: development`; declared components swap to a dev image with the
`faros-dev-agent` injected and a per-component workspace volume; undeclared
components (Postgres, access gate) run as in production. Three non-root
containers per component: coordinator (control API, no secrets), runtime
supervisor (app env and secrets), stateless executor (argv only, no token).
Hardened isolation via RuntimeClass (`gvisor`, `kata`) is a platform setting.

Data plane (infra-owned, caller-authenticated):
`/services/providers/infrastructure/dataplane/clusters/{cluster}/instances/{name}[/components/{c}]/{log|sync|restart|env|process|exec|status}`.
Production instances answer 409 on these verbs.

Preview URL is the template's ordinary public route; `authorize-development-preview`
probes DNS, TLS, and the Gateway before reporting `ready`.

## 6. Concurrency and reservations

While an assistant run owns a project, template switch, hydrate, manual sync,
file writes (`PUT`/`DELETE files/content`, `files/upload`) and
delete return 409. Single replica: assistant work does not survive a
provider restart; orphaned turns become `interrupted`.

## 6a. Binary files and transport limits

| Where | Limit |
|---|---|
| Workspace text file | 256 KiB |
| Workspace binary file | 25 MiB |
| Restore / tree replacement | 500 files, 48 MiB |
| Commit, checkout, sync bundle | 500 files, 48 MiB decoded; 25 MiB per binary; 2 MiB per text file on commit |
| App Studio → hub MCP client (`hubmcp`) | response ≤ 96 MiB (`code mcp <method>: response exceeds 100663296 bytes`), call timeout 120 s |
| App Studio → data plane client | response ≤ 96 MiB (`development data plane <verb>: response exceeds … bytes`) |
| Assistant MCP calls | response ≤ 96 MiB (`MCP <method> response exceeds … bytes`) |

Capability gating: App Studio sends base64 to, or asks base64 from, only a
component that advertises it — `code__commit_files` must declare
`files[].encoding`, `code__checkout_repository` must declare
`binaryEncoding` (both read from `tools/list`, cached 10 min per cluster), a
dev agent must list `base64` in `syncEncodings` (data-plane `process` verb). Otherwise binaries
are skipped and stay uncommitted/unsynced; text is never blocked. The
workspace digest encodes a binary as `0xfe`, 8-byte length and its SHA-256.

## 7. Portal features and the routes behind them

| Portal tab | Routes |
|---|---|
| New project wizard | `create-readiness`, `plan`, `projects/stream`, first turn |
| Preview | `authorize-development-preview`, `preview-bridge/sessions` |
| Code (edit, upload, download) | `files`, `files/content` (GET/PUT/DELETE), `files/raw`, `files/upload` |
| Review | `assistant/threads/{t}/reviews` |
| Providers | hub `GET /api/providers` |
| Integrations | `integrations` CRUD |
| Publishing | `promotion`, `releases`, `promote`, `publishing`, `publishing/members`, `publishing/grants` |
| History | `checkpoints`, `repository.commits`, `restore-workspace` |
| Project settings | `PATCH {p}`, `preview`, `preview/grants`, `DELETE {p}?uid=` |
| Skills | `assistant/skills`, `skills/detail`, `skills/activation` |
| Models | `llm-settings*` |
| Chat | threads, turns, events, steer, interrupt, approval, input, attachments, approval-mode |
