# App Studio reference

App Studio has no REST facade. Every operation is either a plain kube
read/write of a `Project`, `Session` or `Studio` CR through the hub's kcp
proxy, or a **verb** — a kcp custom subresource on one of those CRs:

```
$AS/projects/<p>/<verb>[/<tail>]        $AS = $HUB/clusters/$CLUSTER/apis/ai.railgrid.ai/v1alpha1  (railgrid env)
$AS/sessions/<s>/<verb>[/<turn>]
$AS/studios/studio/<verb>
```

Every call needs `Authorization: Bearer` (`X-Railgrid-Org` /
`X-Railgrid-Workspace` are addressing only; `fc` from SKILL.md section 0
sends all three). kcp authorizes the **HTTP method** as the RBAC verb on
`<resource>/<verb>`, forwards the call to the provider with your identity
stamped, and the provider checks you can `get` the addressed object before
the handler runs. Consequences: a project or session you cannot see, one
that is being deleted, and an undeclared verb all answer **404 `Not Found`
as plain text**; a wrong method is 405 with `Allow`; a tail on a verb that
takes none is 400. Handler errors are Kubernetes `Status` JSON; lists are
`{"items":[…]}`. The verb names are `spec.export.resources[].verbs` in
`providers/app-studio/manifest.yaml`; `railgrid app` and `railgrid sandbox`
call the same ones.

## 1. What App Studio owns and what it does not

- Owns: `Project`, `Session`, `Studio` CRs (`ai.railgrid.ai/v1alpha1`, all
  cluster-scoped), the workspace file store, the assistant, and the verbs.
- Does not own git (the **code** provider does) or runtime (the
  **infrastructure** provider does). App Studio holds no runtime kubeconfig.
- Has no MCP server. It is an MCP *client* that calls `code__*` tools on
  the workspace aggregate endpoint as the project's identity. CLI:
  `railgrid app`, `railgrid sandbox`, `railgrid commit` ([cli.md](cli.md)).
- Depends on `code` and `infrastructure` being enabled in the workspace.

## 2. CRDs

### Project

`spec`: `displayName` (required, 1–128 chars, CRD-enforced), `description`,
`repository` (`repositoryRef`, `name`, `connectionRef`, `adopted`),
`template.name` (infrastructure Template; empty means no dev environment),
`memory` (`goals[]`, `requirements[]`, `constraints[]`), `sharing.preview.mode`
(`private|public`), `sharing.publishing.mode` (`private|shared|public`),
`environments[]` (`name`, `mode artifact|live`, `promotion manual|auto`,
`autoDeploy`, `bindings[]`).

`bindings[]`: `name`, `provider`, `kind providerResource|providerReference`,
`resourceRef {apiVersion, kind, resource, name}`, `values` (opaque),
`allowedActions[] {name, version, schemaDigest, grantedBy, grantedAt, revoked, revokedBy, revokedAt}`,
`imagePullSecretRef.name`.

`status`: `phase`, `updatedAt`, `observedGeneration`,
`environments[] {name, mode, phase, bindings[] {name, provider, phase, url, previewURL, outputs}}`,
`workspace {sourceRevision, uncommittedPaths[], pendingCommit {name, repositoryRef, workspaceDigest, paths[], requestedAt}, settlement {workspaceDigest, paths[], recordedAt}}`.

Finalizer `ai.railgrid.ai/instances` carries the whole teardown. Annotation
`ai.railgrid.ai/delete-repository: "true"` (set before deleting) also deletes a
non-adopted Repository. Derived names: dev instance `<project>-dev`, prod
instance `<project>-prod` (truncated at 30 chars plus an 8-hex hash when
needed). Environment names `development` and `production`; binding names
`dev` and `prod`.

Plain CR operations (no verb): list `GET $AS/projects`; read the CR
`GET $AS/projects/<p>` (use `view` for the joined state); edit
`spec.displayName` / `spec.description` / `spec.memory` with a merge patch
(`PATCH $AS/projects/<p>` with `Content-Type: application/merge-patch+json`,
or `kubectl patch project <p> --type merge -p '…'`); delete with
`DELETE $AS/projects/<p>` (section 3, "Delete") or `kubectl delete project`.
Sharing is **not** CR metadata: use the `preview` and `publishing` verbs.

### Session

One assistant conversation; `metadata.name` is the thread ID.
`spec.projectRef`, `spec.threadID`, `spec.actorID`; `status.title`,
`phase active|archived`, `activeTurnID`, `activeTurnStatus`, `turnCount`,
`lastActivityAt`, `updatedAt`. Owned by the Project; finalizer
`ai.railgrid.ai/purge` deletes the transcript, so `kubectl delete session <s>`
is the `discard` verb. Threads are per user: another user's session is 404 on
every verb.

### Studio

Singleton `metadata.name: studio`. `spec.search {disabled, size small|medium|large, resourceRef}`
and `spec.browser {…}` provision a shared SearXNG and Playwright browser as
infrastructure instances; `status.search` / `status.browser` report
`Ready|Pending|Disabled`. `spec.llm` is the model registry (section 3, "Models").

**The Studio must exist before any `studios/studio/<verb>`** (the gate reads
it). `railgrid app create` creates it; by hand:

```bash
fc "$AS/studios/studio" >/dev/null 2>&1 || fc -X POST "$AS/studios" -H 'Content-Type: application/json' \
  -d '{"apiVersion":"ai.railgrid.ai/v1alpha1","kind":"Studio","metadata":{"name":"studio"},"spec":{"search":{"size":"small"},"browser":{"size":"small"}}}'
```

## 3. Verb table

### Workspace-wide (Studio verbs)

```
GET  $AS/studios/studio/create-readiness        → {gitConnection:{ready,status ready|provider-missing|connection-missing|validating|failed,connectionRef,message}}
GET  $AS/studios/studio/development-templates   → {templates:[{name,displayName,description,category,components{comp:path},previewAccessModes[],hasScaffold}]}
GET  $AS/studios/studio/import-repositories     → {repositories:[{ref,name,connectionRef,htmlURL}]}   code Repositories no project claims
POST $AS/studios/studio/plan                    {prompt,templateName?} → {displayName,repositoryName,template,components,scaffold{repository,ref},availableTemplates[]}
POST $AS/studios/studio/create-project          CreateProjectRequest → 201 ProjectView
POST $AS/studios/studio/create-project-stream   same, SSE events status{message} / created{ProjectView} / error{message}
POST $AS/studios/studio/discover-models         {provider?,baseURL?,apiKey?,existingModelID?} → {models:[{id,name,compatibility,capabilities[]}],source}
POST $AS/studios/studio/test-model              {existingModelID?,provider?,baseURL?,model,apiKey} → {ok:true}; 422 InvalidConnection, 504 timeout, 502 other
```

### Models

The registry is `Studio.spec.llm`; API keys are Secrets you write yourself as
the caller. There is no settings verb: edit the CR, then test.

- `spec.llm.defaultModel` = a model `id`; `spec.llm.models[] {id, revisionID, archived?, name, provider?, baseURL?, model, secretRef{name}}`
  (≤ 20 active, ≤ 200 revisions); `spec.llm.runtime {maxRetries, retryBackoffMS, streamIdleTimeoutMS}`.
- Secret `railgrid-projects-llm-<id>` in namespace `default`, key `apiKey`,
  **label `railgrid.ai/owner: app-studio`** (without it the provider cannot see
  the Secret and the model never reports configured).
- `status.models[] {id, configured}` and condition `LLMRegistryValid` are the
  truth; read them, not the Secret.
- `provider`: `openai-compatible` (default; `baseURL` default
  `https://api.openai.com/v1`) or `google-ai-studio`
  (`https://generativelanguage.googleapis.com`). `discover-models` and
  `test-model` reuse a stored key via `existingModelID` only for the same
  provider and endpoint (`enter a credential before …` otherwise).

### Projects

```
POST   …/create-project                        CreateProjectRequest → 201 ProjectView      (Studio verb, above)
GET    $AS/projects                             kube list of Project CRs
GET    $AS/projects/<p>/view                    ProjectView (CR + live instance status + commit ledger + sourceRevision + thumbnail)
PATCH  $AS/projects/<p>                         merge patch of spec.displayName / description / memory (kube)
DELETE $AS/projects/<p>                         kube delete; see "Delete"
POST   $AS/projects/<p>/set-repository          {connectionRef,retryRepositoryRef?,projectUID?} → ProjectView; 409 when a repository is already bound to another connection or adopted
POST   $AS/projects/<p>/set-template            {template} → {template,components,project}; 404 unknown template; switching deletes the old dev instance and re-syncs
GET    $AS/projects/<p>/thumbnail               PNG; ETag / If-None-Match → 304; 404 when none captured
GET    $AS/projects/<p>/checkpoints             → {items:[{key template|git|ci|production,label,state done|pending|blocked|error,reason,remediation{kind auto|manual,tool,actionUrl,message}}]}
```

`CreateProjectRequest`: `name?`, `displayName?`, `description?`, `prompt?`,
`templateName?`, `inferDevelopmentTemplate?`, `connectionRef?`,
`repositoryMode? auto|none|create`, `existingRepositoryRef?`. Unknown fields
are 400. `none` cannot carry `connectionRef`/`existingRepositoryRef`; `auto`
uses a validated connection when one exists, else the project starts with no
repository.

Naming: an explicit `name` must already be a DNS label
(`name must be a valid DNS label`) and is used verbatim for the Project
**and** the new code `Repository`. If a `Repository` of that name exists
(often one a deleted project left behind) the create is a 409:
`a code Repository named "<n>" already exists (possibly left by a deleted project); adopt it with existingRepositoryRef or choose another name`.
A taken Project name is not suffixed either (the Project create fails).
Only when `name` is omitted are names derived from `displayName`/the prompt
preflight and suffixed until free.

`ProjectView`: `name`, `uid`, `deleting`, `displayName`, `description`,
`phase`, `template`,
`repository {ref, name, connectionRef, htmlURL, status, message, ready, canRetryCreation, commits[{name, phase, branch, commitSHA, commitURL, message, fileCount, createdAt, completedAt}], commitsError}`
(`ref` is the code `Repository` name every `code__*` call wants; it can differ
from the project name),
`memory`, `sharing`, `environments[]`, `createdAt`, `updatedAt`,
`sourceRevision`, `thumbnail {available, refreshing, commitSHA, revision}`.

**`phase` is not the readiness gate for committing.** A newly created project
returns `phase: Ready` while its `Repository` is still being reconciled. That
window reports `repository.status: Provisioning` (sometimes with the message
`Creating repository "<name>".`, sometimes with no message at all). If it
lasts more than ~2 min and the `Repository` CR has no `status` at all, the
code provider is not reconciling — `railgrid app status` says so and
`GET $HUB/api/providers` shows `code` not ready
([troubleshooting.md](troubleshooting.md)). `RepositoryMissing` means the CR
existed and is gone. A `code__commit_files` issued in that window fails with
`repository "<name>" not found`, which reads like a wrong `repositoryRef`.

Gate on `repository.ready == true` (and `repository.htmlURL` being populated)
before the first commit:

```bash
until fc "$AS/projects/$P/view" | jq -e '.repository.ready == true' >/dev/null; do sleep 5; done
```

`kubectl get repositories.code.railgrid.ai` confirms from the other side.

**Delete.** A kube delete of the Project CR; the finalizer tears down the
dev/prod instances, the repository claim, sessions, attachments, identity and
workspace tree. Pass the UID as a precondition so a same-name replacement
cannot be deleted by a stale action; names can be reused after the async
delete.

```bash
UID=$(fc "$AS/projects/$P" | jq -r .metadata.uid)
# optional: also delete the Repository App Studio created (and its GitHub repo). Adopted repos are never deleted.
fc -X PATCH "$AS/projects/$P" -H 'Content-Type: application/merge-patch+json' -d '{"metadata":{"annotations":{"ai.railgrid.ai/delete-repository":"true"}}}'
fc -X DELETE "$AS/projects/$P" -H 'Content-Type: application/json' -d '{"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":"'$UID'"}}'
```

Repositories survive by default (the claim label is released). A UID
mismatch is a kube 409. While a run is active the finalizer waits for it
(`an assistant turn is still finishing`).

### Files and workspace

The file path is the **`?path=` query parameter** on every file verb (a path
in the tail is 400). Paths are relative to the repository root.

```
GET    $AS/projects/<p>/files                        → {files:[{path,size,…}],truncated?,limit?}   flat sorted tree
GET    $AS/projects/<p>/files-content?path=<p>       → {path,content,version,binary,truncated,size}   all fields always present; 404 "file not found"
PUT    $AS/projects/<p>/files-content?path=<p>       body = raw file bytes
DELETE $AS/projects/<p>/files-content?path=<p>       optional If-Match
GET    $AS/projects/<p>/files-raw?path=<p>[&download=1]   raw bytes
POST   $AS/projects/<p>/files-upload                 multipart
POST   $AS/projects/<p>/hydrate-workspace            {ref?} → {repositoryRef,ref,commitSHA,written[],sourceRevision,skipped[]}   git → workspace via code__checkout_repository; 400 / 502 / 503 "project workspace store is not configured"
POST   $AS/projects/<p>/restore-workspace            {commitSHA:"<full sha>",expectedSourceRevision:<ProjectView.sourceRevision, number or numeric string>} → {commitSHA,written[],deleted[],sourceRevision,skipped[]?}   409 when the revision moved; restoring an older commit deletes files and the reconciler commits the deletions; a restored commit without a workflow builds nothing (`build.status: none`)
POST   $AS/projects/<p>/scaffold                     → {template,scaffold{repository,ref},seeded}   re-seed template starter files into an EMPTY workspace; 400 no template, 422 NoScaffold
```

`files-content` GET: text beyond 256 KiB is `truncated` with no `version`; a
binary file (NUL byte or invalid UTF-8) has `content: ""`, `binary: true` and
a whole-file `version` (`sha256:<hex>`).

`files-raw`: `ETag` is the quoted version,
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

- `PUT files-content`: body is the whole file (≤ 25 MiB binary, 256 KiB text;
  413 `file exceeds the 26214400-byte binary limit` or
  `text|binary file "<p>" is too large: N > M bytes`). No precondition =
  upsert. `If-None-Match: *` = create only (any other value 400
  `If-None-Match supports only * (create only)`). `If-Match: <version>`
  (quotes and `W/` stripped) = replace only that version; `If-Match: *` =
  must exist (412 `file does not exist`). Both headers → 400. Stale or
  existing target → 412. Answers 201 created / 200 replaced with
  `{path,size,version,binary}`.
- `DELETE files-content`: `If-Match` optional (absent or `*` = current
  version); 204; 404 `file not found`; stale → 412.
- `POST files-upload`: multipart, one or more `file` parts (the part
  filename is the path, `\` → `/`), optional `dir` (target directory, "" =
  root), optional `overwrite=true|false`. ≤ 100 files and 48 MiB of file
  bytes per request (413 `upload exceeds the 50331648-byte request limit`),
  25 MiB per binary. Every target is preflighted before anything is written:
  duplicate path 400, existing target without overwrite 409
  `file "<p>" already exists; upload with overwrite=true to replace it`.
  → 200 `{files:[{path,size,version,binary}]}`.

```bash
fc -X PUT "$AS/projects/$P/files-content?path=web/public/assets/jeep.glb" -H 'If-None-Match: *' --data-binary @jeep.glb
fc "$AS/projects/$P/files-content?path=web/public/assets/jeep.glb" | jq '{binary,size,version}'
```

Other paths that change files: assistant tools (`create_file`,
`replace_file`, `edit_file`, `delete_file`, `move_file`, `import_attachment`,
`download_file`), hydrate, restore, scaffold. Hydrate writes files but does
**not** mark them for re-commit and does **not** delete workspace files the
ref no longer has (a commit that removed `index.html` leaves it in the
workspace and the sandbox; `DELETE files-content` it yourself); scaffold and
restore do mark files. Restore does not fail when the checkout skipped
paths; it returns them in `skipped` and is meant to keep their workspace
copies. Ambiguous: the code passes the checkout's skip entries verbatim, and
those carry reason suffixes (`<path> (binary)`, ` (file too large)`), so they
may not match the workspace path and the file may be deleted anyway. Check
`deleted[]` after a restore of a commit with large or binary files.

### Dev sandbox

```
POST $AS/projects/<p>/sync-development                   workspace → dev instance, per component
POST $AS/projects/<p>/restart-development[?component=]   → {<component>: agent reply}; 400 unknown component, 404 missing instance
GET  $AS/projects/<p>/development-logs[?component=]      streamed (default: first declared component)
GET  $AS/projects/<p>/development-status                 raw instance status
POST $AS/projects/<p>/authorize-development-preview      → {target,ready,previewURL,message,reason,desiredAccess,observedAccess,accessConverged}
POST $AS/projects/<p>/preview-bridge-sessions            {generation:<uuid>,protocolVersion:1,portalInstanceID:<uuid>} + Origin header → 201 {status available,sessionID,generation,capability,previewOrigin,portalOrigin,expiresAt} | 200 {status:"unsupported"}; 409 preview not ready
DELETE $AS/projects/<p>/preview-bridge-sessions/<session>   204
GET|POST|DELETE $AS/projects/<p>/preview                 → {mode,url,converged,supported,grants[]}; POST {mode public|restricted} (aliases members|private; empty keeps the mode); DELETE = private again and drops every preview grant (200 even without a dev environment); converges in ~20–30 s
GET|POST $AS/projects/<p>/preview-grants ; POST …/preview-grants/<grant> (revoke)    same bodies and rules as publishing-grants
```

No start or stop verb: the sandbox exists while `spec.template` is set. App
Studio has no exec verb of its own; the assistant uses `exec_command`, and
you use the infrastructure provider's `instances/<name>/exec` subresource on
`<project>-dev` directly ([infrastructure.md](infrastructure.md) section 8;
`railgrid sandbox exec`, MCP `infrastructure__dev_exec`).
`sync-development` returns `{target: {…, ResourceName, Components}, result: {<component>: {phase, changed, deleted, restarted, sourceRevision, sourceDigest, skipped?: [{path, reason}]}}}`
— `reason` is `binary-unsupported` (the agent lacks base64 sync), `too-large`
(over the per-file limit) or `sync-limit` (over the sync's total size or file
count); `skipped` is present only when something was skipped. Runtime
mutations such as `npm install` inside the sandbox are not synced back.
`sync-development` answers 422 `component "app" has no package.json in the
workspace root; the Node.js (node) development sandbox needs one — commit a
package.json whose "dev" or "start" script launches the server on $PORT, or
skip the sandbox and promote` when a component's workspace has no
`package.json` (an empty `worker` project before its first commit, or an
adopted Go/Python tree); 409 when the sandbox refuses the revision, 404 for a
missing instance, 502 only for transport failures. Hydrate, logs, restart and
the `process` verb still work without a `package.json`.

Mixing syncs: the dev agent stamps a revision on plain syncs too
(`railgrid sandbox sync` uses Unix-seconds revisions), which can put the
agent's applied revision ahead of App Studio's FileStore revision. On a 409
containing `older than the applied revision` or `already applied with a
different digest`, App Studio reads the applied revision from the component's
`process` status, renumbers to applied+1 and retries once; the offset is kept
per component (in memory) and also applied to exec and thumbnail checks. So a
CLI sync does not wedge App Studio's sync, but App Studio's next sync
replaces whatever the CLI pushed.

Binaries reach a component only if its agent's status (infrastructure
`process` verb) lists `base64` in `syncEncodings` (cached 10 min); otherwise
they are skipped with a log line and text still syncs. Per sync: 500 files,
48 MiB decoded, 25 MiB per binary; binaries past the bounds are dropped
(logged), never the whole sync.

### Assistant

Session (thread) → Turn → Item. Listing and starting conversations are
Project verbs; everything acting on one is a Session verb.

```
GET  $AS/projects/<p>/sessions                    ?includeArchived&limit&cursor → {items:[{id,title,status,actorID,createdAt,updatedAt}],nextCursor}
POST $AS/projects/<p>/create-session              {id?,title?} → 201 thread (id defaults to thread-<uuid>)
POST $AS/projects/<p>/adopt-session/<thread>      → {thread,session}   recreates a missing Session CR for an existing thread
POST $AS/sessions/<s>/edit                        {title?,archived?} → thread
POST $AS/sessions/<s>/discard                     → 204 (transcript purged); same as kubectl delete session
GET  $AS/sessions/<s>/items                       ?limit (turns, default 20, max 50) &beforeSequence → {items,nextCursor?}
GET  $AS/sessions/<s>/events                      SSE; replays from sequence 1 unless Last-Event-ID (or ?afterSequence=); ends after the active turn's terminal event; closing does not cancel
POST $AS/sessions/<s>/turn                        {content,clientUserMessageID,modelID?,collaborationMode default|plan (case-insensitive),skills?[],contextResources?[],contentParts?[]} → 202 {thread,turn,continuationOfTurnID?}
POST $AS/sessions/<s>/review                      {"target":{"type":"current_workspace","instructions"?},clientUserMessageID,modelID?,skills?,contextResources?,contentParts?} → 202   read-only Review turn (`current_workspace` is the only target type; instructions ≤ 8 KiB); the review arrives as one `agentMessage` after `inspect` tool calls, `turn.mode` is `review`
GET  $AS/sessions/<s>/active-turn                 204 when idle, 200 turn otherwise; check it before `PUT files-content`, which is 409 during a turn
GET  $AS/sessions/<s>/turn-status/<turn>          {turn,effectiveSettings?}
POST $AS/sessions/<s>/steer/<turn>                {content,clientUserMessageID} → 202 turn; 404 unless the turn is yours and in progress
POST $AS/sessions/<s>/interrupt/<turn>            {clientRequestID} → 202 {turnID,status}
POST $AS/sessions/<s>/continue/<turn>             {content?,clientUserMessageID,skills?,contextResources?,contentParts?} → 202; only after an interrupted turn (409 otherwise)
POST $AS/sessions/<s>/approval/<turn>             {requestID,decision allow|deny,editedArguments?} → {runID,requestID,status,…}
POST $AS/sessions/<s>/input/<turn>                {requestID,answer|answers} → same
GET|PATCH $AS/projects/<p>/approval-mode          {mode on_request|always_ask|never} → {mode,updatedAt}   per user and project
GET|POST  $AS/projects/<p>/attachments            POST multipart field `file` (+clientAttachmentID, draft) → 201 {id,filename,contentType,kind,sizeBytes,sha256,createdAt,draft?,expiresAt?}
GET|DELETE $AS/projects/<p>/attachments/<a>       GET = the bytes (Content-Disposition attachment); DELETE 204, 403 another caller's, 409 bound to a turn
```

```bash
fc -X POST "$AS/projects/$P/create-session" -H 'Content-Type: application/json' -d '{"id":"t1","title":"cart"}'
fc -X POST "$AS/sessions/t1/turn" -H 'Content-Type: application/json' \
  -d '{"content":"Add a cart page backed by /api/cart.","clientUserMessageID":"m1","collaborationMode":"default"}'
fc -N "$AS/sessions/t1/events" > events.log      # ends after turn.completed
```

A turn on an archived session is 409 `assistant thread is archived`; a
second turn while one is active is 409. `collaborationMode` is trimmed and
lowercased before use, so `Default`/`PLAN` work; empty = `default`. On the
`turn` verb `review` is 400 (use the `review` verb), anything else 400
`collaborationMode must be default or plan`. Unknown body fields are 400 on
every assistant verb.

Attachments: any file type is accepted. `kind` is `image`
(PNG/JPEG/WebP ≤ 8 MiB, magic bytes checked), `text` (`.txt`/`.md` UTF-8
≤ 1 MiB), else `file` — including larger images and text, JSON, models,
fonts (≤ 25 MiB; type from the declared type or extension, else
`application/octet-stream`). Over-size → 413. Per turn: 8 attachments,
50 MiB total, current-turn images ≤ 20 MiB. Unbound drafts: 128 MiB and 64
per project. `file` attachments reach the model as metadata only (filename,
ID, type, size, and a hint to call `import_attachment`); their bytes are
never sent.

Binding an attachment to a turn: send `contentParts` —
`[{"type":"text","text":"Place it at web/public/logo.png"},{"type":"attachment","attachment":{id,filename,contentType,sizeBytes,sha256,createdAt}}]`
(`web/public/` on `application`, `public/` on `simple-webapp`)
with exactly those six fields copied from the upload receipt (`kind`,
`draft`, `expiresAt` are rejected: `unknown attachment receipt field`). When
`contentParts` is present the top-level `content` is ignored, so the
instruction must be a `text` part. An attachment binds to one turn (a second
turn with the same receipt is 500 `bind project attachments: attachment
already exists`; later turns may still `import_attachment` it by ID), and a
failed POST consumes its `clientUserMessageID` (409 `client request ID was
already used for different input`) — use a fresh one. The upload's `id`
equals `clientAttachmentID` when you pass one.

Thread event `project.committed`: appended to the turn of the
project's latest assistant run when the reconciler settles a commit;
payload `{commitSHA, commitURL?, branch?, repositoryRef, files[]}` (files
include deletions). Projects without a session get none.

Event stream shape: each event is `id: <sequence>`, `event: <type>`,
`data: {threadID, turnID?, sequence, type, itemID?, payload:{thread|turn|item}, createdAt}`,
with `: keepalive` comments every 15 s. `type` ∈ `thread.created`,
`thread.updated`, `turn.started`, `turn.continued`, `item.started`,
`item.delta`, `item.completed`, `plan`, `approval.requested`,
`approval.resolved`, `input.requested`, `input.resolved`, `turn.completed`,
`turn.failed`, `turn.interrupted`, `project.committed`. An `item.completed`
carries `.payload.item.type` ∈ `userMessage`, `agentMessage` (`.content`),
`plan`, `dynamicToolCall` (`.data = {kind inspect|edit|…, title, target, status
succeeded|failed, severity}`); a terminal turn event carries
`.payload.turn.status`; a failed tool item carries only
`diagnostic {message, category, referenceID}` (no tool name), and a `plan`
mode reply is an ordinary `agentMessage`. To list what the assistant did:

```bash
sed -n 's/^data: //p' events.log | jq -r 'select(.type=="item.completed") | .payload.item | select(.type=="dynamicToolCall") | .data | [.status,.kind,.title,(.target|tostring)] | @tsv'
```

`turn.completed` with `status: completed` is reported even when tool items
inside the turn failed. The turn object (in `turn.completed` /
`turn.failed` / `turn.interrupted` at `.payload.turn`, and in
`turn-status/<turn>`) carries the step outcome, each field omitted when zero:

| Field | Counts |
|---|---|
| `failedItems` | tool items that ended failed |
| `recoveredItems` | failures a linked retry repaired (not in `failedItems`) |
| `rejectedItems` | steps whose approval was denied (their items read `failed`; not in `failedItems`) |
| `failures` | up to 5 × `{itemID, title, category, message, referenceID}` |

`turn-status` computes it from stored items, so it works for older turns too.

Approval: the preference is per user and project (`approval-mode`) and each
turn reports it as `approvalMode`.

| Mode | Reads, plans, questions | File edits | Runtime effects (`exec_command`, restart, env, `rebuild_project`, `agents__run_agent`) | `promote_project`, `infrastructure__provision` | Commits |
|---|---|---|---|---|---|
| `on_request` (default) | allow | allow | allow | ask | ask |
| `always_ask` | allow | ask | ask | ask | ask |
| `never` | allow | **deny** | **deny** | **deny** | **deny** |

Under the default, a turn told to deploy pauses before `promote_project`
until you answer. `never` is fail-closed, not "never prompt". An ask pauses
the run and emits `approval.requested` (`.payload.requestID`,
`.payload.interrupt`); answer with
`POST $AS/sessions/<s>/approval/<turn> {"requestID":…,"decision":"allow"|"deny"}`,
and `approval.resolved` follows. `input.requested` / `input/<turn>` works the
same way for `ask_follow_up`.

Turn statuses: `in_progress`, `completed`, `failed`, `interrupted`. A provider
restart interrupts the active turn; resume with `continue/<turn>` or read
`items` plus the event stream. Spend guards: 200 iterations per turn,
2,000,000 rollout tokens, an org monthly USD cap. Exhaustion fails the turn
with `iteration_limited`, `budget_limited`, or `org_spend_cap_exceeded`.

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
usable `RAILGRID_HUB_PUBLIC_URL` fails with
`private preview inspection is unavailable: the app-studio deployment has no usable RAILGRID_HUB_PUBLIC_URL (chart value hub.publicURL, an absolute HTTPS origin such as https://hub.example.com); ask the platform operator to set it`
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
GET  $AS/projects/<p>/promotion   → {template,instance,productionSchema,immutableProductionInputs[],productionValues,requestedRolloutRevision,observedRolloutRevision,promotable,
                                     build:{status built|incomplete|none|unsupported,commitSHA,components[{name,imageInput,built,image,digest,tag}],missing[],run{…}},
                                     production:{phase,url}}
GET  $AS/projects/<p>/releases    → {items:[{name,phase,branch,commitSHA,commitURL,message,createdAt,completedAt,releaseID,deployable,live,missing[],components[]}]}   ≤ 100, succeeded commits only
POST $AS/projects/<p>/promote     {values?,commitSHA?,releaseID?} → {environment,instance,rolloutRevision,commitSHA,releaseID,components[]}; 400 validation, 502 otherwise
```

The promote response does not embed the project; re-read `view` for project
state. `railgrid app promote <p> --hostname-prefix <x>` sends
`{"values":{"expose":{"hostnamePrefix":"<x>"}}}`.

Build resolution: the newest successful `RepositoryCommit` CR for the
project's repository gives `commitSHA`; for every launchable component the
code provider's `Package` CRs must contain a version tagged exactly
`sha-<commitSHA>`; its digest is what gets deployed. Commits made outside
railgrid produce no `RepositoryCommit` and are therefore invisible here.
Workflow file: the template's `spec.development.build.workflowPath`
(`.github/workflows/build.yaml` for shipped scaffolds). When the template
declares none, App Studio tries `railgrid-app-studio-build.yml`, then
`build.yaml` if that call errors.

Promote writes the `production` environment binding: instance
`<project>-prod`, `railgridMode: production`, user values merged, image inputs
set to digests, fresh `railgridRedeployRevision`, a `dockerconfigjson` pull
Secret `<instance>-registry` minted from the code Connection. Platform
owned and always overriding: `name`, `railgridMode`, image inputs,
`railgridRedeployRevision`, `railgridCluster`, `credentialsSecretName`, `access`
(managed by publishing). `expose.hostnamePrefix` is a first-deploy input.

### Publishing

```
GET|POST|DELETE $AS/projects/<p>/publishing     GET → {published, publication:{name,uid,mode,host,url,ready,phase,error,target{apiVersion,kind,resource,name,uid}}, grants[]} — `published:true, mode:public|restricted` only while published (restricted = shared policy or an active grant), otherwise `published:false, mode:"private"` (a fresh promote and an unpublished app both read private); POST {mode public|restricted} (aliases members|private; empty keeps the mode); DELETE = private and drop grants → 200 same shape
GET   $AS/projects/<p>/publishing-members        → {items:[{user,rbacIdentity,…}]}   workspace/org members
GET|POST $AS/projects/<p>/publishing-grants      → {items:[{name,uid,user,publication,revoked,phase}]}; POST {user,invite?}   `user` is the stable platform User name (`user-xxxxx`, from members), not an email — 400 `user must be the stable platform User name; set invite to share with a new email`; `invite: true` + an email pre-provisions a pending User; 400 `grants require private access; the app is currently public — switch it to invite-only first`
POST  $AS/projects/<p>/publishing-grants/<grant> revoke (allowed in any mode) → the remaining list; 409 when the grant belongs to another app
```

```bash
fc -X POST "$AS/projects/$P/publishing" -H 'Content-Type: application/json' -d '{"mode":"public"}'
fc -X POST "$AS/projects/$P/publishing-grants" -H 'Content-Type: application/json' -d '{"user":"user-abc12"}'
```

`restricted` writes `access: private` plus the Project policy `shared`
(invite-only). The gate enforces `restricted` and an unpublished `private`
app identically: workspace admins and grant holders get in, nobody else, and
no machine caller without an app token ([infrastructure.md](infrastructure.md)
section 5, "Machine callers"). After `public`, anonymous requests can still
get the 302 for 10–20 s; `railgrid app publish` re-reads `publishing` for up
to 15 s until `publication.ready`.

Mechanics: the prod instance's `spec.access` flips in place; the
infrastructure access gate enforces it; invitations are a per-app
ClusterRole `railgrid-app-access.<instance>` (rules: `get` on
`instances/access` for that name, and kcp `access` on nonResourceURL `/`)
plus one ClusterRoleBinding per member, subject `railgrid:<email>`
(`kubectl get clusterrolebindings -l railgrid.ai/app-access` lists them).

### Integrations (provider actions)

```
GET    $AS/projects/<p>/integrations                     → {items:[{environment,alias,provider,kind,resourceRef,allowedActions[],phase}],available:[…],discovery:{state,issues[]}}
POST   $AS/projects/<p>/integrations                     {environment?,alias,provider,kind:"providerReference",resourceRef{apiVersion,kind,resource,name},allowedActions[{name,version,schemaDigest}] (alias: actions[]),consentAccepted?} → 201
PATCH  $AS/projects/<p>/integrations/<alias>             {allowedActions[],consentAccepted?} → 200; 403 when you may not call an action you grant
DELETE $AS/projects/<p>/integrations/<alias>             204
POST   $AS/projects/<p>/integration-actions/<alias>[/<action>]   {action,actionVersion|version,input} → {requestID,provider,action,actionVersion,resourceRef,result|error{code,message,retryable}}; 403 not granted / revoked, 409 schema drift
```

Alias regex `^[A-Za-z_][A-Za-z0-9_-]{0,62}$`; default environment
`development`. Grant creation re-reads the caller-scoped `GET $HUB/api/providers`
catalog and requires exact provider, action, version, bound resource, and
`schemaDigest`; deprecated actions are refused; `consentAccepted` is required
when the catalog says so. Invoke re-verifies the digest against the live
catalog (409 on drift), then calls the serving provider's own
`<resource>/<name>/<action>` subresource on the hub as the **Project's
identity** (not yours) with a two-minute budget and a 4 MiB response cap;
`Idempotency-Key`, `X-Request-ID`, `X-Railgrid-Action-Deadline-Ms` are
forwarded. Only shipped action today: Databricks `query_table/v1` (sync,
read-only, `columns` ≤ 64, `limit` 1..100).

In-app SDK: `@railgrid/actions-node` (alias for `@crwilhit/railgrid-actions-node@0.1.0`).
`createActionsClient({baseURL: RAILGRID_ACTIONS_BASE_URL, project: RAILGRID_PROJECT, tokenFile: RAILGRID_ACTIONS_TOKEN_FILE})`
then `railgrid.integration('<alias>').invoke('query_table/v1', {...})`.
Server-side only; the runtime exchanges a projected bootstrap token for a
10-minute workload token whose RBAC is exactly the materialized grants.

### Skills

```
GET  $AS/projects/<p>/skills                     → {skills:[{id,name,description,scope,packageName,enabled,editable,version,digest,contentDigest,resources[]}],catalogDigest,warnings[]}
GET  $AS/projects/<p>/skill-detail?id=<qualified id>
POST $AS/projects/<p>/skills-create              {packageName,name,description,instructions,resources?[{path,content}]} → 201 detail
POST $AS/projects/<p>/skills-import              same body, or {format,files[]} from an export
POST $AS/projects/<p>/skills-activation          {id,enabled} → detail; 403 unless a system or editable project skill
GET  $AS/projects/<p>/skill-export/<packageName>   → {format:"railgrid.skill.v1",packageName,digest,files[],filename,content,package}
GET|PUT|DELETE $AS/projects/<p>/skill/<packageName>   PUT needs expectedDigest in the body, DELETE as ?expectedDigest=; stale → 409
```

Scopes: bundled (embedded, read-only), provider
(`CatalogEntry.spec.hub.assistantSkills`, qualified `providers/<provider>/<package>`),
project (`.agents/skills/<package>/SKILL.md` with activation state in
`.agents/skills/.railgrid-catalog.json`). Frontmatter supports `name` (≤ 64 B)
and `description` (≤ 1024 B) only; `context`, `agent`, `model` are rejected.
Limits: 32 KiB per skill, 64 resources of ≤ 64 KiB, 4 MiB per package,
5 MiB per request. Skills are guidance only; they cannot grant tools or
permissions. The portal workbench only browses and toggles; create, edit,
import, export, delete exist on the verbs alone.

## 4. Templates, scaffolds, AGENTS.md

Templates are infrastructure `Template` CRs. Development-capable ones
declare `spec.development` with `components.<name> {workspacePath, imageInput, devImage, workingDir, startCommand, port, reload}`,
`scaffold {repository, ref}`, and `build.workflowPath`.

| Template | Components | Scaffold |
|---|---|---|
| `application` | `web` → `web/`, `api` → `api/` (+ Postgres, access gate) | `github.com/railgrid/scaffold-application@v0.1.4` |
| `simple-webapp` | `app` → `.` | `github.com/railgrid/scaffold-simple-webapp@v0.1.4` |
| `worker` | one component, no URL | none |
| `universal-coding-sandbox` | scratch | none |

Scaffold fetch is a tarball download (400 files, 8 MiB total, 1 MiB per file,
text only) seeded only into an empty workspace and marked uncommitted so the
reconciler lands it as the first commit. Both shipped scaffolds (v0.1.4)
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

- Paths are settled (marked committed) only when the commit
  returns phase `Succeeded` with a SHA; anything else leaves them dirty.
  `status.workspace.pendingCommit` / `settlement` on the Project show it.
- When the commit `is queued behind a GitHub rate limit` or
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
- Each settled commit is posted to the session as `project.committed`.

The assistant's `commit_project_files` follows the same limits (48 MiB total,
2 MiB text); when `code__commit_files` does not advertise `encoding` it
commits the text, returns `skippedBinaryPaths`, and fails only when binaries
were the whole change: `only binary files changed (…), and this workspace's Code provider does not accept binary commits yet; they stay uncommitted in the workspace`.

App Studio injects the workspace-root `AGENTS.md` (32 KiB cap) into every
model sample. Project memory is injected too.

## 5. Sandbox model

The Template is the sandbox. A dev instance is the same graph rendered with
`railgridMode: development`; declared components swap to a dev image with the
`railgrid-dev-agent` injected and a per-component workspace volume; undeclared
components (Postgres, access gate) run as in production. Three non-root
containers per component: coordinator (control API, no secrets), runtime
supervisor (app env and secrets), stateless executor (argv only, no token).
Hardened isolation via RuntimeClass (`gvisor`, `kata`) is a platform setting.

Data plane (infra-owned, caller-authenticated): the infrastructure
provider's `instances/<name>/{log|sync|restart|env|process|exec|workspace|proxy}`
subresources on `$HUB/clusters/$CLUSTER/apis/infrastructure.railgrid.ai/v1alpha1`
([infrastructure.md](infrastructure.md)). App Studio's dev verbs call them as
the provider. Production instances answer 409 on these verbs.

Preview URL is the template's ordinary public route; `authorize-development-preview`
probes DNS, TLS, and the Gateway before reporting `ready`.

## 6. Concurrency and reservations

While an assistant run owns a project, template switch, hydrate, restore,
manual sync and file writes (`PUT`/`DELETE files-content`, `files-upload`)
return 409; a Project delete waits in the finalizer. Single replica for
assistant work: it does not survive a provider restart; orphaned turns
become `interrupted` (`continue/<turn>` resumes).

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
dev agent must list `base64` in `syncEncodings` (infrastructure `process`
verb). Otherwise binaries are skipped and stay uncommitted/unsynced; text is
never blocked. The workspace digest encodes a binary as `0xfe`, 8-byte length
and its SHA-256.

## 7. Portal features and the verbs behind them

| Portal tab | Verbs |
|---|---|
| New project wizard | `create-readiness`, `plan`, `create-project-stream`, first turn |
| Preview | `authorize-development-preview`, `preview-bridge-sessions` |
| Code (edit, upload, download) | `files`, `files-content` (GET/PUT/DELETE), `files-raw`, `files-upload` |
| Review | `sessions/<s>/review` |
| Providers | hub `GET /api/providers` |
| Integrations | `integrations`, `integration-actions` |
| Publishing | `promotion`, `releases`, `promote`, `publishing`, `publishing-members`, `publishing-grants` |
| History | `checkpoints`, `view` → `repository.commits`, `restore-workspace` |
| Project settings | kube merge patch of the Project, `preview`, `preview-grants`, kube delete |
| Skills | `skills`, `skill-detail`, `skills-activation` |
| Models | kube patch of `Studio.spec.llm` + Secrets, `discover-models`, `test-model` |
| Chat | `sessions`, `create-session`, `turn`, `events`, `steer`, `interrupt`, `continue`, `approval`, `input`, `attachments`, `approval-mode` |
