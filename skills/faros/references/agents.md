# Agents provider reference

REST base `https://<hub>/services/providers/agents` (written `$AG`). Headers as for
every provider: bearer, `X-Faros-Org`, `X-Faros-Workspace`. MCP tools appear
on the aggregate as `agents__*`.

## 1. CRDs (`agents.faros.sh/v1alpha1`, cluster-scoped)

### Agent

| Field | Meaning |
|---|---|
| `displayName` (required), `description` | |
| `systemPrompt` (≤ 32 KiB) | Persona injected at the head of every run |
| `models {chat, background, compaction}` | Purpose → model credential name; `chat` is the fallback for all |
| `modelFallbacks[]` | Tried when the chat model errors (before the first streamed token) |
| `autonomy suggest\|ask\|auto` (default `ask`) | Approval posture |
| `delegates[]` | Agents this one may `delegate` to |
| `tools.interactive` / `tools.background` | `ToolGrant {families[], connections[], toolsets[], requireApproval[]}` |
| `memory {enabled (true), maxNotes}` | |
| `limits {maxToolTurns 16 (cap 32), timeoutSeconds 3600, maxSpawnsPerRun 10 (cap 20), maxConcurrentSpawns 4 (cap 8)}` | |
| `budget {window day\|month, usdLimit, tokenLimit}` | Breach suspends schedules and background runs; chat stays |
| `channels[] {name, connectionRef, primary}` | Named channel roles |

Status: `phase Ready|Suspended`, `lastRunAt`, `usage {windowStart, tokens, usd}`, `suspendedReason` — may stay `{}` on current builds even after successful runs; judge by `GET /api/runs?agent=<name>` instead.

Tool families: `core` (always: memory, self-scheduling, notify, ask,
delegate), `web` (`web_fetch`; `web_search` needs a `websearch` connection),
`github` (hosted GitHub MCP toolset from a `github` connection), `mcp`
(tools from `mcp` connections as `<connection>__<tool>`), `edges` (aggregate
MCP, interactive runs only, never opt-in), `spawn` (`spawn` and `join`
worker tools), `files` (declared but not implemented).

### Connection

`spec.type`: `github`, `mcp`, `websearch`, `edges` (marker only), `http`,
`telegram`, `slack`, `smtp`, `discord`. Fields: `displayName`,
`auth secret|oauth`, `oauth {provider github|google|slack, scopes}`,
`secretRef` (default `faros-agents-conn-<name>`, key `token`; Slack signing
secret and Telegram secret under `signing_secret`), `baseURL`, `channel`
(chat id, channel id, email, webhook URL), `config` map (`instance:` names an
infrastructure instance for in-workspace MCP or search backends; `agent:`
overrides the one-inbound-agent rule). Status: `phase`, `webhookPath`,
`oauthConnected`, `tokenExpiresAt`.

### Schedule

`agentRef`*, `type cron|wakeup|heartbeat`*, `schedule` (5-field cron),
`timeZone`, `runAt` (wakeup), `task`, `channelRef` (channel role name),
`checklist` (heartbeat), `suspend`, `retry.maxAttempts`. Status: `nextRun`,
`lastRun`, `lastRunID`, `consecutiveFailures`, `disabledReason`.

### Trigger

`agentRef`, `source webhook|github`, `connectionRef`, `filter {eventType, match, header.<name>}`,
`task`, `channelRef`, `suspend`. Status: `webhookPath` (contains a secret),
`lastFired`, `lastRunID`.

### Toolset

`displayName`, `description`, `families[]`, `connections[]`,
`requireApproval[]`; merged into an agent's grant by name.

Runs, sessions, messages, memory, and the inbox are **not** CRDs; they live
in the provider's Postgres and are reachable only through REST or MCP.

Minimal agent:

```yaml
apiVersion: agents.faros.sh/v1alpha1
kind: Agent
metadata: { name: researcher }
spec:
  displayName: Researcher
  systemPrompt: You are a careful research assistant. Cite sources.
  autonomy: auto
  models: { chat: main, background: cheap }
  tools:
    interactive: { families: [core, web, spawn], connections: [search] }
    background:  { families: [core, web] }
  limits: { maxToolTurns: 12 }
  budget: { window: month, usdLimit: "25" }
  channels:
    - { name: primary, connectionRef: my-telegram, primary: true }
```

## 2. Model credentials

Only OpenAI-compatible endpoints are implemented (`provider` is
`openai-compatible`, `openai`, or empty). The tenant supplies the key. Each
credential is a Secret `faros-agents-model-<name>` in namespace `default`
with keys `provider`, `baseURL`, `model`, `apiKey`. Presets: OpenAI
`https://api.openai.com/v1`, Anthropic `https://api.anthropic.com/v1`,
OpenRouter `https://openrouter.ai/api/v1`. Purposes: `chat` (strong),
`background` (cheap; workers, heartbeats), `compaction`.

```
GET    /api/credentials                      keys redacted
POST   /api/credentials                      {name, provider?, baseURL?, model, apiKey}
DELETE /api/credentials/{name}
POST   /api/credentials/{name}/test          → {ok, latencyMS, error, models[]}
GET    /api/catalog                          curated pricing and context windows
GET    /api/usage?days=30                    rollups by agent, model, day (max 90)
```

## 3. REST routes

```
GET    /healthz  /api/whoami  /api/capabilities   (capabilities: which providers the aggregate federates)
GET|POST /api/agents ; GET|PUT|DELETE /api/agents/{name}
GET    /api/agents/{name}/sessions ; DELETE /api/agents/{name}/sessions/{session}
GET    /api/agents/{name}/messages?session=&limit=&cursor=
POST   /api/agents/{name}/chat                {message, sessionID?}  SSE: start, tool_start, tool_end, delta, done | approval_required | error
POST   /api/agents/{name}/runs                {task, sessionId?, idempotencyKey?, wait? (≤120), callback {url, secret}?} → 202 {runId, phase} or 200 {runId, phase, run}
GET    /api/runs?agent=&phase=&trigger=&class=&session=&parent=&since=&until=&cursor=&limit=(≤200)
GET    /api/runs/{id}                         {…summary, input, output, sources[], pending {inboxID, tool, args}, steps[{tool,args,result,outcome,error,durationMS}], children[]}
GET    /api/runs/{id}/wait?timeoutSeconds=    long-poll (default 60, cap 300)
POST   /api/runs/{id}/cancel                  → 202
GET    /api/events                            SSE: run phase changes and inbox activity
GET|POST /api/schedules ; PUT|DELETE /api/schedules/{name} (no GET by name: 405) ; POST /api/schedules/{name}/run → 202 {runID}
GET|POST /api/triggers ; GET|PUT|DELETE /api/triggers/{name} ; POST /api/triggers/{name}/run
GET|POST /api/toolsets ; GET|PUT|DELETE /api/toolsets/{name}
GET|POST /api/connections ; GET|PUT|DELETE /api/connections/{name}
POST   /api/connections/{name}/test           real outbound send
POST   /api/connections/{name}/enable-inbound {publicBaseURL} → {webhookPath, webhookURL, registered, note}
GET    /api/oauth/providers ; POST /api/connections/{name}/oauth/authorize {publicBaseURL} → {authorizeURL}
GET    /api/inbox?state=pending ; POST /api/inbox/{id}/resolve {decision approve|deny|answer, response?}
POST   /s2s/clusters/{cluster}/agents/{name}/runs ; GET /s2s/clusters/{cluster}/runs/{id}[/wait]     ServiceAccount callers
POST   /webhooks/triggers/{cluster}/{name}/{token} ; POST /webhooks/channels/{cluster}/{name}/{token}   anonymous inbound
```

Create and update bodies use flat fields: `name`, `displayName`,
`description`, `systemPrompt`, `autonomy`, `modelCredential`,
`modelFallbacks`, `budgetTokens`, `budgetUSD`, `delegates`, `channels`,
`interactiveFamilies`, `backgroundFamilies`, `interactiveToolsets`,
`backgroundToolsets`, `interactiveConnections`, `backgroundConnections`.
`maxToolTurns` and `timeoutSeconds` are accepted **only by `PUT`**
(and `agents__update_agent`); on `POST /api/agents` they are dropped
silently (`spec.limits` stays `{}`) — create, then `PUT` them. Only fields
you send change; list fields replace wholesale.

Schedules: `POST /api/schedules` takes
`{name, agentRef, type cron|wakeup|heartbeat, schedule?, timeZone?, runAt?, task?, checklist?, suspend?, channelRef?}`
(e.g. `{"name":"digest-hourly","agentRef":"digest","type":"cron","schedule":"0 * * * *","timeZone":"UTC","task":"…"}`);
`POST …/schedules/{name}/run` → 202 `{"runID":…}`, then
`GET /api/runs/{runID}/wait`. There is **no** `GET /api/schedules/{name}`
(405): read one schedule from the list, or `kubectl get schedules.agents.faros.sh <name>`.

## 4. Invocation semantics

- `POST /runs` and `agents__run_agent` are **API runs**: background tool
  grant, no `edges__*` tools, no channel notification, detached from the HTTP
  request. `wait` is capped at 120 s; `PendingApproval` counts as settled
  for waiters.
- Chat (`/chat`, the portal) and channel messages are **interactive** runs:
  interactive grant plus the aggregate MCP as `edges__<provider>__<tool>`
  (three segments), acting as the calling user.
- Callback: signed with `X-Faros-Signature` (HMAC-SHA256 with `callback.secret`),
  3 attempts, payload `{runId, agent, phase, output, sources, usage, finishedAt}`.
- S2S: the caller's ServiceAccount needs `create` (and `get`) on
  `agents.faros.sh` `agents/delegate` for that agent name; 503 means the
  provider has no virtual-workspace connection. Never verified end to end
  against live kcp per the design doc.

## 5. Deep research

Grant `spawn` plus `web` (portal checkbox "Research fan-out"). Tools:
`spawn {task, instructions?, tools?, maxToolTurns?}` and
`join {taskIds?, timeoutSeconds?}`. Workers are child runs of the same agent
with trigger `spawn`, fresh context, `background` model, families
intersected with the parent's grant, never `edges`; results clipped to 8 KiB;
sources parsed from a trailing `Sources:` block. Limits: 4 concurrent
(cap 8), 10 per run (cap 20), depth 2, worker turns 8 (cap 16), join 300 s
(cap 900). The run tree from `GET /api/runs/{id}` is the research trace.

## 6. Channels

| Channel | Direction | Fields | Inbound verification |
|---|---|---|---|
| Telegram | in + out | `secret` bot token, `channel` chat id | Auto-registered via `setWebhook`; nothing to paste |
| Slack bot | in + out | `secret` `xoxb-…` (chat:write), `channel` `C…`, `signingSecret` | Signature over raw body; `enable-inbound` refuses without the signing secret; paste the returned URL into Event Subscriptions |
| Slack incoming webhook | out | `channel` = webhook URL | |
| Discord bot | in + out | `secret` bot token, `channel` home channel, MESSAGE CONTENT intent | Gateway WebSocket, no webhook |
| Discord webhook | out | `channel` = webhook URL | |
| SMTP | out | `secret` password, `channel` recipient, `config {host, port, from, username}` | |

One connection can be the inbound channel of one agent (409 otherwise).
Session commands from a channel: `/new`, `/status`, `/inbox`, `/approve N`,
`/deny N`, `/answer N <text>`. Trigger payloads are quarantined as untrusted;
channel messages are the user's own turn.

## 7. MCP tools (`agents__*`)

Runs: `run_agent {agent, task, sessionId?, wait?≤120}`,
`get_run {runId, wait?≤300}`, `list_runs {agent?, phase?, trigger?, session?, parent?, limit?}`.

Agents: `list_agents`, `get_agent {name}`, `create_agent {name, displayName?, description?, systemPrompt?, autonomy?, modelCredential?, modelFallbacks?, budgetTokens?, budgetUSD?, channels?}`,
`update_agent {name, …pointer fields…}`, `delete_agent {name}`.

Credentials: `list_model_credentials`, `save_model_credential {name, model, provider?, baseURL?, apiKey?}`,
`delete_model_credential`, `test_model_credential`.

Connections: `list_connections`, `create_connection {name, type, displayName?, baseURL?, channel?, config?, secret?, signingSecret?}`,
`update_connection`, `delete_connection`, `test_connection`.

Toolsets: `list_toolsets`, `create_toolset {name, displayName?, description?, families?, connections?, requireApproval?}`, `update_toolset`, `delete_toolset`.

Schedules: `list_schedules`, `create_schedule {name, agentRef, type, schedule?, timeZone?, runAt?, task?, checklist?, suspend?, channelRef?}`, `update_schedule`, `delete_schedule`, `run_schedule`.

Triggers: `list_triggers`, `create_trigger {name, agentRef, source, connectionRef?, filter?, task?, suspend?, channelRef?}`, `update_trigger`, `delete_trigger`, `run_trigger`.

Discovery: `list_tool_families`.

Deliberately absent: OAuth connect (browser only) and inbox resolution (a
human approves). Secrets are write-only. If an MCP tool answers "open the
agents UI once, then retry", the provider has not yet recorded the
cluster → org/workspace mapping for background execution; load the Agents
portal page once.

## 8. Relationships

- App Studio delegates research to agents through `agents__run_agent` and
  friends when the workspace has at least one agent and the provider is
  federated; transport timeout is stretched to `wait + 30 s`.
- Interactive agent runs get every federated provider's tools, including
  `edges__edges__pods_exec` on connected clusters, as the calling user.
- Infrastructure is optional: with it, `searxng` and `browser` instances back
  `web_search` and a Playwright MCP; background runs reach the data plane
  with a per-agent ServiceAccount `faros-agent-<agent>` (read-only on
  `infrastructure.faros.sh`).
