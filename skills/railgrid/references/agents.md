# Agents provider reference

From a coding agent, use the `agents__*` MCP tools (section 8) through
`railgrid mcp proxy`. Everything else is the kube API of your workspace:
the CRs below with `kubectl` (context `railgrid`) or kube REST at
`$HUB/clusters/$CLUSTER/…` with your bearer, and the provider's **verbs**,
which are kcp custom subresources on the same API:

```
$HUB/clusters/$CLUSTER/apis/agents.railgrid.ai/v1alpha1/{resource}/{name}/{verb}[/{tail}]
```

There is no `/services/providers/agents/api/*` any more (and no `/s2s/*`);
`AG=$HUB/services/providers/agents` only still serves `/mcp`, the browser
OAuth routes and the anonymous inbound webhooks (section 3). A verb carries
only the bearer — no `X-Railgrid-Org`/`-Workspace` headers; the cluster in
the path is the workspace.

## 1. CRDs (`agents.railgrid.ai/v1alpha1`, cluster-scoped)

`kubectl get agents` (short `agt`), `modelcredentials` (`modelcred`), `runs`,
`connections` (`conn`), `schedules` (`sched`), `triggers` (`trig`),
`toolsets` (`ts`). Every kind carries a `Validated` condition: the
reconciler reports cross-object mistakes (unknown `agentRef`, unknown tool
family, a grant without `core`, a channel conflict…) on the object instead
of a 400 at write time, so read `status.conditions` after `kubectl apply`.

### Agent

| Field | Meaning |
|---|---|
| `displayName` (required), `description` | |
| `systemPrompt` (≤ 32 KiB) | Persona injected at the head of every run |
| `backend.type model\|harness` (default `model`) | Where turns execute: in the provider against a chat model, or on a coding harness on an edge (section 5) |
| `backend.model {credentials {chat, background, compaction}, fallbacks[]}` | Purpose → ModelCredential name; `chat` is the fallback for all. `fallbacks` are tried when the chat credential errors before the first streamed token. Replaces the former `models`/`modelFallbacks` |
| `backend.harness {edgeRef {kind, name}, credentialRef, model?, workspace persistent\|ephemeral}` | Section 5. CEL: `type: harness` requires `harness` and rejects `model` |
| `autonomy suggest\|ask\|auto` (default `ask`) | Approval posture |
| `delegates[]` | Agents this one may `delegate` to |
| `tools.interactive` / `tools.background` | `ToolGrant {families[], connections[], toolsets[], requireApproval[]}` |
| `memory {enabled (true), maxNotes}` | |
| `limits {maxToolTurns, timeoutSeconds (3600), maxSpawnsPerRun 10 (cap 20), maxConcurrentSpawns 4 (cap 8)}` | |
| `budget {window day\|month, usdLimit, tokenLimit}` | Breach suspends schedules and background runs; chat stays |
| `channels[] {name, connectionRef, primary}` | Named channel roles |

Status: `phase Ready|Suspended`, `suspendedReason`, `lastRunAt`, `usage
{windowStart, tokens, usd}`, `backend {type, service, harness {name,
version}}` (what a run will execute on, resolved), conditions `Validated`,
`ModelCredentialsReady` and `BackendReady`.

Tool families (`families[]`): `core` (always kept: `memory_save`/`memory_list`,
`schedule_create`/`update`/`delete`/`schedules_list`, `notify`, `ask`, `wait`,
`delegate` when `delegates` is set), `web` (`web_fetch`; `web_search` needs a
`websearch` connection), `github` (tools from a `github` connection), `mcp`
(tools from `mcp` connections as `<connection>__<tool>`), `spawn` (`spawn`
and `join`), `visualization` (`visualize_data`, inline charts in portal chat),
`edges` (the workspace MCP aggregate, interactive runs only, never opt-in),
`files` (accepted by validation, not wired to any tool).

### ModelCredential

The model endpoint an agent uses, as an object; the key stays in a Secret.
Section 2.

### Run

One execution of an agent. **Runs are CRs now** — list, get and watch them
with `kubectl get runs` (columns Agent, Trigger, Phase, Started). The object
is a projection: `spec {agentRef, trigger, sessionID, parentRunRef,
delivery, idempotencyKey, inputPreview (≤ 2 KiB), repository}` and `status
{phase Pending|Running|PendingApproval|Succeeded|Failed|Aborted, message,
startedAt, finishedAt, deadlineAt, attempt, usage {inputTokens,
outputTokens, usdMicros, usd, durationMS}, transcriptRef {sessionID,
messages, toolCalls}, result (repository runs)}`. The object's name is the
run id. Nobody creates a Run by hand (the `run` verb on the agent does);
deleting one purges its transcript and trace, and deleting an agent
garbage-collects its runs (ownerReference). The transcript, the step trace
and the answer are not on the object: they come from the `trace` verb,
`agents__get_run`, or the `messages` verb (section 4).

### Connection

`spec.type`: `github`, `mcp`, `websearch`, `edges` (marker only), `http`,
`telegram`, `slack`, `smtp`, `discord`. Fields: `displayName`,
`auth secret|oauth`, `oauth {provider github|google|slack, scopes,
authorizeURL, tokenURL}`, `secretRef` (default `railgrid-agents-conn-<name>`,
key `token`; the Secret is labelled `railgrid.ai/owner: agents`), `baseURL`,
`channel` (chat id, channel id, email, webhook URL), `config` map
(`instance:` names an infrastructure instance that backs an `mcp` or
`websearch` connection). Status: `phase`, `message`, `webhookPath`,
`oauthConnected`, `tokenExpiresAt`, conditions.

### Schedule

`agentRef`*, `type cron|wakeup|heartbeat`*, `schedule` (5-field cron),
`timeZone`, `runAt` (wakeup), `task`, `channelRef` (channel role name),
`checklist` (heartbeat), `suspend`, `retry.maxAttempts`. Status: `nextRun`,
`lastRun`, `lastRunID`, `consecutiveFailures`, `disabledReason`.

### Trigger

`agentRef`, `source webhook|github`, `connectionRef`, `filter {eventType,
match, header.<name>}`, `task`, `channelRef`, `suspend`. Status:
`webhookPath` (contains a secret), `lastFired`, `lastRunID`.

### Toolset

`displayName`, `description`, `families[]`, `connections[]`,
`requireApproval[]`; merged into an agent's grant by name. Status `usedBy`.

Sessions, messages, memory, the step trace and the inbox are **not** CRs;
they live in the provider's Postgres behind the verbs in section 3.

Minimal agent:

```yaml
apiVersion: agents.railgrid.ai/v1alpha1
kind: Agent
metadata: { name: researcher }
spec:
  displayName: Researcher
  systemPrompt: You are a careful research assistant. Cite sources.
  autonomy: auto
  backend:
    model: { credentials: { chat: main, background: cheap } }
  tools:
    interactive: { families: [core, web, spawn], connections: [search] }
    background:  { families: [core, web] }
  limits: { maxToolTurns: 12 }
  budget: { window: month, usdLimit: "25" }
  channels:
    - { name: primary, connectionRef: my-telegram, primary: true }
```

Enabling the provider: `agents` requires `infrastructure` **and** `edges`
to be enabled in the workspace first (the hub refuses otherwise).

## 2. Model credentials

A `ModelCredential` is `spec {provider, baseURL, model, secretRef {name},
secretKey (default apiKey)}` plus a Secret in namespace `default` that
**must** carry the label `railgrid.ai/owner: agents` (unlabelled, it saves
fine and is invisible to every unattended run). Providers:

- **Chat endpoints** — `openai-compatible` (default) and `openai`: anything
  speaking Chat Completions + `GET /models` (OpenAI `https://api.openai.com/v1`,
  Anthropic's compat endpoint `https://api.anthropic.com/v1`, OpenRouter
  `https://openrouter.ai/api/v1`, a local gateway). `baseURL` is required.
  Model-backed agents run on these only; the tenant brings the key.
- **Harness identities** — `claude-code` (Secret key `oauthToken`, a
  `claude setup-token` value, or `apiKey`, an Anthropic key) and `codex`
  (Secret key `auth.json`). No endpoint, never probed; used only by
  `backend.harness.credentialRef` (section 5).

Status: conditions `SecretResolved`, `Reachable` (`GET {baseURL}/models`
answered; always True for a harness identity), `Ready` (both), plus
`models[]` (the **chat-capable** ids the endpoint served, catalog-known
first), `lastProbeTime`, `lastProbeError`. Purposes on the agent: `chat`
(strong), `background` (cheap; workers, heartbeats), `compaction`.

```bash
kubectl create secret generic railgrid-agents-model-openai -n default --from-literal=apiKey=sk-…
kubectl label secret railgrid-agents-model-openai -n default railgrid.ai/owner=agents
kubectl apply -f - <<'EOF'
apiVersion: agents.railgrid.ai/v1alpha1
kind: ModelCredential
metadata: { name: openai }
spec: { provider: openai-compatible, baseURL: https://api.openai.com/v1, model: gpt-4o, secretRef: { name: railgrid-agents-model-openai } }
EOF
kubectl get modelcredentials          # PROVIDER MODEL READY
```

Verbs on the credential (section 3): `test` (a real chat round-trip,
optional body `{"model":"<id>"}` to probe another id) and `discover`
(re-read `/models`, refresh status). The curated price/context-window
catalog is no longer a route; it ships with the portal bundle
(`model-catalog.json`).

## 3. Verbs (kcp custom subresources)

`V=$HUB/clusters/$CLUSTER/apis/agents.railgrid.ai/v1alpha1`; every call is
`curl -H "Authorization: Bearer $TOKEN"` (kube REST, no tenant headers;
`kubectl get --raw`/`create --raw` with the `railgrid` context works for the
same paths without the `/clusters/…` prefix). kcp authorizes the HTTP method
as the RBAC verb on `{resource}/{verb}` and the provider then checks you can
`get` the object; a workspace member passes, a ServiceAccount needs RBAC on
`agents.railgrid.ai` `agents/run` (and `get` on `agents`) — the old
`agents/delegate` hook is gone.

```
POST   $V/agents/{a}/chat                 {message, sessionID? ("default")}  SSE: start {runID,sessionID}, run_started, assistant_message {content, phase commentary|final}, delta {text}, tool_start {id,name,args}, tool_end {name,result,durationMS,error}, approval_required {runID,inboxID,kind approval|question,tool,args,question}, done {runID,content,finalContent,status,usage} | error. Interactive: edge tools appear as edges__<provider>__<tool> (e.g. edges__edges__pods_list)
POST   $V/agents/{a}/run                  {task, sessionId?, idempotencyKey?, wait? (≤120), callback {url, secret}?, repository?} → 202 {runId, phase, reused?} or 200 {runId, phase, reused?, run}
GET    $V/agents/{a}/sessions             → {items}
DELETE $V/agents/{a}/session/{sessionID}  erase one transcript
GET    $V/agents/{a}/messages?session=&limit=(≤500)&cursor=   → {items, nextCursor}
GET    $V/agents/{a}/usage?days=30        rollups (cap 90): {windowDays, total, byAgent, byModel, series}
GET    $V/agents/{a}/inbox?state=pending  → {items} (this agent's approvals and questions)
POST   $V/agents/{a}/inbox-resolve/{id}   {decision approve|deny|answer, response?} → the item; resumes the paused run
GET    $V/agents/{a}/events               SSE, this agent only: event: run {id,agent,trigger,phase,…} | inbox {id,state,agent,runID}
POST   $V/modelcredentials/{c}/test       {model?} → {ok, latencyMS, error, models[]}
POST   $V/modelcredentials/{c}/discover   → chat-capable model ids; refreshes status.models
GET    $V/runs/{id}/trace                 {…summary, input, output, sources[], pending {inboxID, kind, tool, args}, steps[{tool,args,result,outcome,error,durationMS}], children[], harness {attemptID, sessionID}?, repository?, result?}
GET    $V/runs/{id}/wait?timeoutSeconds=  long-poll (default 60, cap 300) → the same shape
POST   $V/runs/{id}/cancel                → 202 {id, cancelling}; 409 when already settled
POST   $V/runs/{id}/artifact              {name: git-result.json|git-result.bundle} → {name, digest, size, mediaType, data (base64)}
POST   $V/connections/{c}/test            real outbound send → {status: sent}
POST   $V/connections/{c}/enable-inbound  {publicBaseURL} → {webhookPath, webhookURL, registered, note}  (telegram, slack)
POST   $V/connections/{c}/authorize       {publicBaseURL} → {authorizeURL}  (OAuth; finish in a browser)
POST   $V/schedules/{s}/run               → 202 {runID}
POST   $V/triggers/{t}/run                → 202 {runID}
```

Still hub-proxied under `$AG=$HUB/services/providers/agents` (not verbs):
`/mcp`, `GET /oauth/providers`, `/oauth/callback`, `/healthz`, and the
anonymous inbound `POST /webhooks/triggers/{cluster}/{name}/{token}` and
`POST /webhooks/channels/{cluster}/{name}/{token}` (`status.webhookPath`
appended to the hub URL).

Removed outright: `/api/*` CRUD (use kubectl), `/api/whoami`,
`/api/capabilities` (read `MCPServer.status.federatedProviders` or
`agents__list_tool_families .providers`), `/api/catalog`, `/api/runs` list
(`kubectl get runs`), `/s2s/*`. The flat create/update bodies
(`modelCredential`, `interactiveFamilies`, …) survive only on the MCP tools.

## 4. Runs and invocation semantics

- `POST …/agents/{a}/run` and `agents__run_agent` are **API runs** (trigger
  `api`): background tool grant, no `edges__*` tools, no channel
  notification, detached from the request. `wait` is capped at 120 s;
  `PendingApproval` counts as settled for waiters. The run executes as the
  **agent** (the provider through its export), never as the caller, so
  starting a run lends it nothing of yours.
- Poll with `kubectl get run <id> -w` (phase, usage, timings), read the
  answer from `…/runs/{id}/trace` `.output` (or `.run.output` on a `run`
  that waited, or `agents__get_run`).
- `idempotencyKey` is unique per agent: a retry with the same key returns
  the existing run (`200`, `reused: true`) instead of starting another. Only
  the verb has it; `agents__run_agent` does not.
- Callback: `POST` to `callback.url` on settle, signed `X-Railgrid-Signature`
  (HMAC-SHA256 with `callback.secret`), 3 attempts, payload `{runId, agent,
  phase, output, sources, message, usage, finishedAt}`. Best-effort; polling
  is the reliable path.
- `cancel` is durable: a run executing elsewhere, queued, or parked on a
  harness is stopped when it next checks; the response says `cancelling`.
- `web_fetch` is anonymous. Its first line is `HTTP <code> <final URL>`,
  plus `(redirected from <URL>)` after a redirect. A private or restricted
  railgrid app answers with the access gate's redirect to
  `/auth/apps/authorize`; `web_fetch` stops there and returns `This is a
  private railgrid app: its access gate redirects to sign-in (…), and
  web_fetch cannot sign in. No content was fetched.` Agents can't mint app
  tokens, so give an agent public endpoints only or pass the data in `task`.
  `maxChars` raises the returned text from 12 000 up to 64 KiB.
- Chat (`chat`, the portal) and channel messages are **interactive** runs:
  interactive grant plus the aggregate MCP as `edges__<provider>__<tool>`
  (three segments), acting as the calling user. Sessions are independent
  threads; several can run at once.
- Schedules/triggers fired by `run` or by their own clock are background
  runs that deliver to the `channelRef` (or primary) channel.

## 5. Harness-backed agents (Claude Code / Codex on an edge)

An agent can run its turns on a coding harness installed on one of the
workspace's host edges instead of the in-process model loop:

```yaml
spec:
  backend:
    type: harness
    harness:
      edgeRef: { kind: LinuxServer, name: build-01 }   # LinuxServer | MacOSServer, never a cluster
      credentialRef: claude-main   # ModelCredential with provider claude-code | codex
      model: sonnet                # optional, passed to the harness
      workspace: persistent        # persistent (one directory across turns) | ephemeral
      githubConnectionRef: gh-main # optional: a github Connection whose token the harness runs with
```

- Which harness answers is derived from the credential's provider
  (`claude-code` → Claude Code, `codex` → Codex); the credential is sent
  with each turn and never stored on the machine.
- The machine offers harnesses through `spec.harness.mode auto|none|explicit`
  on the `LinuxServer`/`MacOSServer` (`auto` is the default: everything
  installed). `kubectl get linuxserver build-01 -o jsonpath='{.status.harnesses}'`
  shows `{name: claude|codex, detected, enabled, ready, version}`; the edges
  provider publishes each runner as a `Service` named `<edge>-<selector>`
  (`build-01-claude`, `build-01-codex`, type `runner`) whose
  `status.harness.name` is the advertised name (`claude-code`, `codex`).
- `status.backend` on the Agent and the `BackendReady` condition
  (`UnknownEdgeRef`, `HarnessServiceMissing`, `HarnessNotReady`,
  `UnsupportedHarnessCredential`, `BackendUnknown`) say whether a run will
  dispatch; `ModelCredentialsReady` is `NotApplicable`.
- `githubConnectionRef` names a `github` Connection (PAT or OAuth) in the
  workspace. Its token is read per turn and exported into the harness child as
  `GH_TOKEN` and `GITHUB_TOKEN`, so `gh pr review` and git over HTTPS
  authenticate as that connection — the way a reviewer agent posts to a pull
  request. It travels like the harness credential (dispatch data, in the
  runner's memory, never on the machine's disk, sent again on every resume).
  Claude Code only: Codex runs with its network disabled. A fine-grained PAT
  scoped to the repositories to review with pull-request write is the right
  token. `BackendReady` reports `UnknownConnectionRef` when the connection is
  missing and `InvalidSpec` when it is not a github one.
- The harness brings its own tools: `spec.tools`, `delegates`,
  `limits.maxToolTurns`/`maxSpawns*`, `backend.model`, and `autonomy`
  `suggest`/`auto` are rejected as `Validated=False` `MeaninglessForHarness`.
  A permission prompt the machine's mode does not pre-approve parks the run
  in `PendingApproval` as an inbox item; `inbox-resolve` (approve/deny/answer)
  resumes the same turn. `chat` works and keeps one harness session per
  conversation; `trace` carries `harness {attemptID, sessionID}`.
- **Repository runs**: `run` with `repository {repositoryID, baseCommit (40
  hex), cloneSource {remoteURL, username?, token?}, commitMessage?,
  requiredCapabilities?, requiredToolchains?, requiredEnvironment?,
  verification {names, commands}?, approvedInput?, maxDurationSeconds?}` (no
  `sessionId`; refused on a model-backed agent) runs in a fresh clone of that
  commit and exports a Git result: `status.result {baseCommit, commit, tree,
  noChanges, resultDigest, bundleDigest, bundleSize}`, bytes via the
  `artifact` verb (`git-result.json`, `git-result.bundle`).

Details: `docs/edge-harness.md`, `docs/local-runner.md`.

## 6. Deep research

Grant `spawn` plus `web` (portal preset "Research fan-out"). Tools:
`spawn {task, instructions?, tools?, maxToolTurns?}` → `{taskId}` and
`join {taskIds?, timeoutSeconds? (default 300, cap 900)}`. Workers are child
runs of the same agent with trigger `spawn`, fresh context, the
`background` credential, families intersected with the parent's grant,
never `edges`; results clipped to 8 KiB; sources parsed from a trailing
`Sources:` block. Limits: 4 concurrent (cap 8), 10 per run (cap 20), depth
2, worker turns 8 (cap 16). The run tree is `…/runs/{id}/trace .children`,
`agents__get_run .children`, or `kubectl get runs` filtered on
`spec.parentRunRef`.

## 7. Channels

| Channel | Direction | Fields | Inbound verification |
|---|---|---|---|
| Telegram | in + out | `secret` bot token, `channel` chat id | Auto-registered via `setWebhook` with a generated secret token; nothing to paste |
| Slack bot | in + out | `secret` `xoxb-…` (chat:write), `channel` `C…`, `signingSecret` | Signature over raw body; `enable-inbound` refuses without the signing secret; paste the returned `webhookURL` into Event Subscriptions |
| Slack incoming webhook | out | `channel` = webhook URL | |
| Discord bot | in + out | `secret` bot token, `channel` home channel, MESSAGE CONTENT intent | Gateway WebSocket, no webhook |
| Discord webhook | out | `channel` = webhook URL | |
| SMTP | out | `secret` password, `channel` recipient, `config {host, port, from, username}` | |

One connection can be the inbound channel of one agent; a second binding is
reported as `Validated=False` `ChannelConflict`. Session commands from a
channel: `/new`, `/status`, `/inbox`, `/approve N`, `/deny N`,
`/answer N <text>`. Trigger payloads are quarantined as untrusted; channel
messages are the user's own turn. Duplicate deliveries are acknowledged
without a second run.

## 8. MCP tools (`agents__*`)

Runs: `run_agent {agent, task, sessionId?, wait?≤120}` → `{runId, phase,
output?, sources?, message?, status}` (side-effecting; no `idempotencyKey` —
use the `run` verb when a retry must not start a second run),
`get_run {runId, wait?≤300}` → `{…, output, sources, steps[{tool,outcome,error,durationMS}], children[], usage}`,
`list_runs {agent?, phase?, trigger?, session?, parent?, limit? (20, max 100)}`.

Agents: `list_agents`, `get_agent {name}` (full settings incl. `backend` and
`tools`), `create_agent {name, displayName?, description?, systemPrompt?,
autonomy?, modelCredential?, modelFallbacks?, budgetTokens?, budgetUSD?,
channels?, maxToolTurns?, timeoutSeconds?}`, `update_agent {name, …the same
fields…, delegates?, interactiveFamilies?, backgroundFamilies?,
interactiveToolsets?, backgroundToolsets?, interactiveConnections?,
backgroundConnections?}`, `delete_agent {name}` (**destructive**: also purges
conversations, memory and run history). **`create_agent` takes no tool
grants**: create, then `update_agent` with the families, toolsets and
connections. Only fields you pass change; list fields replace the stored
list (`core` is always re-added), so read with `get_agent` before appending.
`modelCredential` sets `backend.model.credentials.chat`; `budgetUSD`/
`budgetTokens` set a monthly budget. A harness backend is written with
kubectl, not these tools.

Credentials: `list_model_credentials` (`{name, provider, baseURL, model,
ready, models[]}`, keys never returned), `save_model_credential {name, model,
provider? (openai-compatible), baseURL? (https://api.openai.com/v1),
apiKey?}` (writes the labelled Secret under key `apiKey`; omit `apiKey` to
keep the stored key), `test_model_credential {name}` (`GET /models` →
`{ok, latencyMS, error, models[]}`), `delete_model_credential {name}`
(**destructive**, removes the Secret too).

Connections: `list_connections`, `create_connection {name, type,
displayName?, baseURL?, channel?, config?, secret?, signingSecret?}`,
`update_connection {name, displayName?, baseURL?, channel?, config?,
secret?, signingSecret?}`, `delete_connection {name}` (**destructive**),
`test_connection {name}` (sends a real message).

Toolsets: `list_toolsets`, `create_toolset {name, displayName?, description?,
families?, connections?, requireApproval?}`, `update_toolset`,
`delete_toolset {name}` (**destructive**; linking agents silently lose the grant).

Schedules: `list_schedules`, `create_schedule {name, agentRef, type,
schedule?, timeZone?, runAt?, task?, checklist?, suspend?, channelRef?}`,
`update_schedule {name, schedule?, timeZone?, runAt?, task?, checklist?,
suspend?, channelRef?}`, `delete_schedule {name}` (**destructive**),
`run_schedule {name}` → `{runID, status}` (output goes to the channel).

Triggers: `list_triggers` (rows include the secret-bearing `webhookPath`),
`create_trigger {name, agentRef, source webhook|github, connectionRef?,
filter?, task?, suspend?, channelRef?}`, `update_trigger {name, task?,
source?, connectionRef?, filter?, suspend?, channelRef?}`,
`delete_trigger {name}` (**destructive**), `run_trigger {name}` → `{runID, status}`.

Discovery: `list_tool_families` → `{families[{name, description}],
providers[] (federated through the aggregate), note}`.

Deliberately absent: OAuth connect (browser only), inbox resolution (a human
approves; use the portal or the `inbox-resolve` verb), run cancel, usage,
sessions/messages, and `discover` (verbs only). Secrets are write-only. If a
run tool answers "this workspace is not mapped yet — open the agents UI
once, then retry", load the Agents portal page once so the provider records
the cluster → org/workspace mapping.

## 9. Relationships

- Interactive runs get every federated provider's tools through the
  workspace MCP aggregate, including `edges__edges__pods_exec` on connected
  clusters, as the calling user; the provider list is
  `MCPServer.status.federatedProviders`.
- Other MCP consumers (App Studio, another agent holding `edges`) delegate
  through `agents__run_agent`/`get_run`; stretch the client timeout past
  `wait`.
- Infrastructure backs `searxng` (`web_search`) and `browser` (Playwright
  MCP) instances named by a connection's `config.instance`; the agent reaches
  them as the provider through `instances/proxy`. The only per-agent
  identity is minted by the hub (TTL'd, scoped to the instances the agent's
  connections name); the provider writes no ServiceAccounts or RBAC into
  your workspace.
- Edges supplies the harness hosts (section 5); both `edges` and
  `infrastructure` must be enabled before `agents` can be.
