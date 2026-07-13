# Agents provider — standalone skeleton design

Status: **Substantially implemented (2026-07-13). Chat with a tool loop
(core/web/github/mcp/edges), named model credentials, autonomous cron/heartbeat
firing, event-trigger webhooks, sub-agent delegation, an approvals inbox
(portal + channel), channels in/out (Telegram/Slack), OAuth connections, token
budgets, and a durable Postgres store are built. Not built: the file workspace
(needs the infrastructure provider), the claude-code runner, resumable
approval-paused runs, and the hardening items in
[Implementation status](#implementation-status). Not yet driven end-to-end
against a running hub — integration bugs expected.**
Author: 2026-07-12 (status updated 2026-07-13)
Related: [`agents-provider-research.md`](./agents-provider-research.md) (the
research this design follows from), [`providers.md`](./providers.md),
[`mcp-architecture.md`](./mcp-architecture.md),
[`app-studio-template-sandboxes.md`](./app-studio-template-sandboxes.md)
(sandbox-runner deprecation the `agent-workspace` Template follows from),
`providers/quickstart/` (skeleton), `providers/app-studio/store/` (store
pattern to mirror, not import).

## Summary

A standalone `agents` provider hosting long-running personal AI agents — a
server-side, multi-tenant OpenClaw alternative. A tenant chats with their
agent from the portal or their own messaging channels (Telegram/Slack), the
agent runs scheduled jobs and heartbeats on its own clock, notifies the user
proactively, uses tools (built-in web/GitHub/files families plus arbitrary
MCP connections), and keeps durable memory, sessions, and a file workspace.

**Hard dependencies: the kedge hub and Postgres. Nothing else.** The provider
must be fully functional on a hub that has no infrastructure provider, no
app-studio, and no connected edges. It does not use the Kubernetes layer to
execute anything — agent runs are in-process LLM turns, and cron scheduling is
an internal Go loop, not CronJobs. Where other providers *are* present, the
agents provider detects them and lights up optional integrations (file
workspace, claude-code runner, edge tools); their absence degrades features,
never core function.

## Implementation status

As of 2026-07-12 the provider (`providers/agents/`) is partially built. The
resource model (all five CRDs + Tier 1 fields) is complete; execution is split
between per-request paths that work today and background/autonomous paths that
are not wired yet. The UI has six tabs: Chat, Schedules, Triggers, Connections,
Inbox, Models.

### Built and usable now

- **Provider skeleton** — boots against a bare hub, registered in both Tiltfiles
  (port 8087), Helm chart, `init` bootstrap, portal micro-frontend.
- **Chat** — streaming (SSE) single-turn conversations on the Eino engine, with
  transcript + resumable run records in the store (in-memory backend; see gaps).
- **Named model credentials** — created once, each its own Secret
  (`kedge-agents-model-<name>`), listed/created/deleted on the Models tab and
  assigned/reassigned per agent. This is what an agent uses to reach its
  provider (OpenAI-compatible today).
- **Schedules / Triggers / Connections CRUD** — full create/list/delete of the
  `AgentSchedule`, `AgentTrigger`, and `Connection` CRs from their tabs.
- **Run now / Fire now** — execute a schedule's or trigger's task
  *synchronously* (as the calling user) to test it before autonomous firing
  exists.
- **Budgets** — per-agent monthly token/USD caps, enforced before every run
  (chat, run-now, fire-now); blocks with a clear message when exceeded.
- **Channel notify (outbound)** — Telegram / Slack / SMTP delivery via the
  `channels` package, with a per-connection **Test** button that sends a real
  message.
- **Approvals inbox surface** — list + approve/deny API and tab (populated once
  the tool loop raises approvals — see gaps).
- **Background executor** — schedules fire **autonomously**. The provider reads
  its APIExportEndpointSlice (via `KEDGE_PROVIDER_KUBECONFIG`) to discover the
  APIExport virtual workspace, polls `AgentSchedule` CRs across all bound tenant
  workspaces (~30s, `AGENTS_SCHEDULER_INTERVAL`), claims each fire with an
  optimistic status update (multi-replica safe), and executes through an
  **interface-based executor** (`executor` package: serializable `Job` +
  `Handler`; in-process worker pool today, deliberately swappable for a durable
  engine like Temporal later). Includes timezone-aware cron, one-shot wakeups,
  quiet heartbeats (notify only when actionable), disable-after-5-failures, and
  per-job watchdog timeouts.
- **Background notify** — output/failure of background runs is delivered to the
  agent's `defaultNotifyConnection` (Telegram/Slack/SMTP), settable per agent in
  the UI.
- **Trigger webhooks (inbound)** — webhook/github triggers get an HMAC-tokenized
  URL (shown in the Triggers tab): external `POST`s fire the agent with the
  event payload, no tenant auth needed.

- **Tool loop** — agents call tools mid-conversation via a real tool-call loop
  in the engine (bind → call → observe → continue, bounded by
  `limits.maxToolTurns`, default 16). Families shipped:
  - `core`: `memory_save/list`, `schedule_create` (cron/wakeup — agents
    schedule *themselves*), `schedules_list`, `notify`, `ask` (posts a question
    to the inbox + channel).
  - `web`: `web_fetch` (SSRF-guarded at dial time — blocks private/loopback,
    defeats DNS rebinding) and `web_search` (Brave-compatible `websearch`
    connection).
  - `mcp`/`github`: any `mcp` connection is dialed via the official MCP Go SDK
    and its tools exposed as `<connection>__<tool>`; a `github` connection with
    a PAT gets the hosted GitHub MCP server's full toolset with zero config.
  Per-trigger policy applies (interactive: all families by default; background:
  core+web only — connection-backed families need explicit grants), every call
  lands in the audit log, `requireApproval` patterns gate tools through the
  inbox (approve once → one call), and tool calls render live in the chat UI.

- **Channel inbound** — chat with an agent *from* Telegram/Slack. Each
  messaging connection gets an HMAC-tokenized inbound webhook (**Inbound**
  button on the Connections tab; Telegram is registered automatically via
  `setWebhook`, Slack shows the URL to paste into Event Subscriptions).
  Messages route to the agent whose *notify* dropdown points at the connection
  (override: connection config `agent`), run with the full interactive
  toolset, and reply in the same chat. Loop protection (bot messages ignored),
  configured-chat-only security, and `/new` + `/status` session commands.

- **Approvals + questions over the channel** — approval requests push to the
  agent's channel; `/inbox` lists pending items, `/approve N` / `/deny N`
  resolve approvals (one approval authorizes one tool call), `/answer N <text>`
  answers agent questions — all from Telegram/Slack.
- **Sub-agent delegation** — the `delegate` tool: agents listed in
  `spec.delegates` can be handed a scoped task; the child runs through the
  same execution path with `parentRunID` lineage, its usage rolls into the
  parent's budget, fan-out is capped at 3 per run, and delegated runs cannot
  delegate further (depth 1).
- **OAuth connections** — `auth: oauth` with GitHub/Google/Slack presets:
  bring your OAuth app (client id/secret), click **Connect**, authorize, and
  the callback stores access+refresh tokens in the connection Secret under
  the same `token` key the tool families read. The background loop refreshes
  tokens ~15min before expiry. State is HMAC-signed (no server-side session).
- **Edges family** — the hub's aggregate MCP endpoint (kube clusters + SSH
  servers, MCPServer "default") exposed as `edges__*` tools, dialed as the
  calling user. Interactive runs only (background runs have no user token).

### Priority 0 — validate before building more

The whole stack builds, unit-tests pass, the Postgres backend is verified
against a live database, and every route boot-smokes. But the paths that
matter most have **never been driven against a running hub**: chat against a
real LLM, cron firing through the APIExport virtual workspace, the Telegram
round-trip (chat → tool → approval → `/approve` → retry), GitHub-MCP tools,
and the OAuth callback. Several depend on RBAC/VW behavior not verifiable in
isolation — does the provider SA read its own `APIExportEndpointSlice`, do the
`secrets` permission claims flow through the wildcard VW, does the hub forward
anonymous `/webhooks/*` and `/oauth/callback` with headers stripped. **An
end-to-end test session is expected to surface integration bugs, and fixing
those outranks new features.**

### Not yet implemented — functional (designed, no code)

1. **Files family + agent workspace (M9).** Blocked *outside this provider*:
   needs the `agent-workspace` Template in the **infrastructure provider** (PVC
   + file-access pod + Template-declared dataplane subresources — the
   sandbox-runner successor). The agents-side `files` tool family is small once
   that exists. Also blocks item 3.
2. **Resumable approval-paused runs.** An approval-gated tool call ends its
   attempt with "approve and retry"; the design wants the run *paused* at an
   Eino checkpoint and *resumed* on `/approve`. The store's `checkpoint` column
   exists but is unused — this is the wiring to close that gap.
3. **claude-code runner.** Only the in-process `eino` runner exists; the
   pod-backed `claude-code` runner needs the workspace PVC (item 1) plus a
   `Runner` interface extraction.
4. **Context compaction.** No `/compact` and no automatic summarize-and-truncate
   — long-lived channel sessions will overflow the model window; `/new` is the
   only relief today.
5. **AgentSkill.** In the schema, no behavior; the cross-tenant skill catalog
   (the ClawHub analog) lands after the resource does.
6. **Native Gemini/Vertex.** `llm.BuildModel` implements only the
   OpenAI-compatible path (covers OpenAI, Anthropic-compat, OpenRouter).

### Not yet implemented — hardening (works, rough edges)

7. **USD budgets don't self-trip.** Runs don't compute dollar cost (no pricing
   table), so only *token* caps enforce; a `usdLimit` never fires from real
   usage.
8. **At-rest encryption.** Message/memory content is plaintext in Postgres (the
   columns exist; the app-studio-style key wiring isn't ported).
9. **Autonomy + policy have no UI editor.** `suggest/ask/auto`, per-trigger
   `requireApproval` lists, and `delegates` are API-only; only the
   interactive/background split is enforced from `autonomy`.
10. **Runs & audit have no UI.** The store records every run and tool call, but
    there is no runs-history / audit tab to inspect background activity.
11. **Retry backoff.** Failed schedules count failures and disable at 5; the
    designed 30s/60s/5m escalating retry isn't implemented.
12. **Slack signing-secret verification** (URL token is the only webhook auth
    today), **trigger filters/idempotency** (payloads pass verbatim, duplicate
    deliveries double-fire), **inbound email**, and **multi-chat routing** (one
    connection = one configured chat).

### Milestone mapping

Built: M1 (skeleton), M2 (chat + store + **Postgres**), M3 (schedules + **cron
firing**), M4 (connections + **tool loop**: core/web/github/mcp/edges), M5
(inbox + **delegation** + channel approvals), M6 (channels + **inbound chat**),
M7 (triggers + **webhooks** + **OAuth**), M8 (**token budgets**). Not built:
M9 (workspace/files), the claude-code runner, and the hardening items above.
The [Milestones](#milestones) section lists the full plan.

## Design rules

1. **Own everything.** Own Postgres schema, own tenant credential Secrets,
   own memory store, own scheduler, own tool implementations. No imports from
   `providers/app-studio` or `providers/infrastructure` (mirror their
   patterns; do not link their modules).
2. **No k8s execution path in core.** The default runner executes the agent
   loop inside the provider process. Compute- and storage-backed capabilities
   (claude-code runner, `agent-workspace` filesystem) are optional plugins
   that only register when the infrastructure provider is installed.
3. **Optional capabilities are discovered, not assumed.** At startup (and
   periodically) the provider probes the hub catalog for `infrastructure`
   (workspace + compute runner) and for tenant `MCPServer` resources (edge
   tools). Absent → those tool families and runners simply don't register.
4. **Everything an agent can do, the API can do.** Chat, run-on-demand,
   schedules, connections, memory, files, notifications — all are REST +
   APIExport resources first; the portal, channels, and the agent's own
   self-management tools sit on top.
5. **Trigger-scoped trust.** What an agent may do depends on who is watching.
   Interactive chat can unlock risky tools behind approvals; scheduled,
   heartbeat, and wakeup runs default to read-only + notify-first. This is
   the primary prompt-injection mitigation: unattended runs read untrusted
   web/email content, so they don't get write-capable tools by default.

## APIExport resources (`agents.kedge.faros.sh`)

| Kind | Purpose |
|---|---|
| `Agent` | The persistent assistant: persona/system prompt, model profile refs (per purpose: `chat`, `background`, `compaction`), memory policy, tool grants (connection refs + built-in family toggles) with per-trigger policy, runner preference, limits (max tool turns, per-run timeout), **budget** (monthly token/cost cap, action on breach: suspend + notify), default notification connection, **`autonomy`** (`suggest`/`ask`/`auto`), and **`delegates`** (agent names this agent may spawn as sub-agents) |
| `Connection` | A named credential to an external system: `type` (`github`, `mcp`, `websearch`, `http`, `telegram`, `slack`, `smtp`), **`auth`** (`secret` default, or `oauth`), `secretRef` to a tenant-workspace Secret, non-secret config (base URL, allowed hosts, channel/chat IDs). For `auth: oauth`, an `oauth` block (provider, scopes) and a provider-run callback mint + refresh the token into the Secret. Connections turn tool families and channels on per agent |
| `AgentSchedule` | Time-based firing. `type: cron \| wakeup \| heartbeat`; cron spec (5-field) + **`timeZone`** (IANA name, like `CronJob.spec.timeZone`; default UTC) + task prompt (cron) or standing checklist ref (heartbeat) + `agentRef` + retry policy + `suspend`. Status: `nextRun`, `lastRun`, `consecutiveFailures`, `disabledReason` |
| `AgentTrigger` | Event-based firing — the non-time half of automation. `spec.source` (`webhook`, `channel`, `email`, `github`, `connection`) + `connectionRef` + `filter` (source-specific match: header/signature, message regex, event type, label) + `task` + `agentRef` + `suspend`. Webhook sources get a hub-routed inbound endpoint; connection sources subscribe to a Connection's event stream. Status: `lastFired`, `consecutiveFailures`, `disabledReason` |
| `AgentRun` | One execution: trigger (`chat`, `schedule`, `heartbeat`, `wakeup`, `event`, `api`, `channel`, `delegation`), input, phase, usage/cost, **`parentRunID`** (set for sub-agent runs — delegation lineage), pointer to transcript + checkpoint in the store |
| `AgentSkill` *(post-v1)* | Markdown instructions + required connection types + tool grants, attachable to agents. Later: shareable across tenants via the catalog — the ClawHub analog, which a single-user OpenClaw cannot do |

Tenant-facing permission claim: `secrets` (tenant-scoped), under this
provider's own names: `kedge-agents-llm` (model profiles — see Runner) and
`kedge-agents-conn-<name>` (one per Connection).

### Autonomy and the approvals inbox

`Agent.spec.autonomy` sets the default posture — `suggest` (draft only, never
act), `ask` (act after approval), `auto` (act freely within tool policy) — and
the per-trigger `requireApproval` lists refine it per tool. When a run needs
sign-off it parks in `PendingApproval` (its checkpoint already persisted) and
writes an **inbox item** to the store rather than blocking. The inbox is a
single cross-agent queue of pending approvals and agent questions, surfaced at
`/api/inbox` in the portal and pushed to the agent's default channel; an
approve/deny (portal button or channel reply) resumes the checkpointed run.
This unifies what would otherwise be scattered per-run approval prompts and is
what makes unattended agents safe to grant real tools.

### Sub-agent delegation

An agent runs a flat loop by default. `Agent.spec.delegates` lists other agent
names it may spawn; a `delegate` tool in the `core` family starts a child
`AgentRun` (trigger `delegation`, `parentRunID` set) against the named agent
with a scoped task, streams its result back, and counts its usage against the
parent's budget. Eino's ADK/DeepAgent provides the sub-agent primitive; the
provider adds the run lineage and budget rollup. Depth and fan-out are bounded
by provider limits to keep a delegation tree from runaway spend.

## Storage (own, Postgres)

Mirror the app-studio `store` shape (interface + `postgres.go` + `memory.go`
dev backend + optional at-rest encryption), tables scoped by
org/workspace/agent:

- `agents_messages` — chat transcript per session, cursor pagination.
- `agents_runs` — durable runs: phase, trigger, usage/cost, and an **opaque
  JSONB checkpoint** (Eino interrupt/resume state). Claim semantics
  (`ClaimRun`) so exactly one replica resumes an interrupted run.
- `agents_memories` — long-term memory: small titled markdown notes
  (OpenClaw-style), written/read by the agent through its `memory` tools,
  injected by recency/relevance. Plain rows + keyword search in v1.
- `agents_schedules` — scheduler working set (mirrors `AgentSchedule` specs,
  owns `next_fire_at` computed in the schedule's `timeZone`, claim + failure
  bookkeeping).
- `agents_triggers` — event-trigger working set (mirrors `AgentTrigger` specs,
  dedup/idempotency keys for delivered events, failure bookkeeping).
- `agents_inbox` — pending approvals and agent questions across all agents:
  run ref, kind (approval/question), payload, state (pending/approved/denied/
  answered), so the portal and channels render one queue.
- `agents_oauth` — OAuth state for `auth: oauth` Connections: encrypted refresh
  tokens, expiry, the short-lived `state` nonce for the callback handshake.
- `agents_tool_calls` — **audit log**: every tool invocation (agent, run,
  trigger, tool, arguments digest, outcome, duration). Multi-tenant table
  stakes, and the debugging surface for unattended runs.
- `agents_usage` — per-agent rolling usage for budget enforcement (tokens,
  cost, window).

The APIExport resources are the source of truth for *spec*; Postgres owns
*state* (transcripts, checkpoints, fire times, usage). A thin reconciler
keeps `AgentSchedule.status` updated from the store.

## Scheduler (in-process, no k8s)

The repo's first Go scheduler, deliberately boring:

- Ticker loop (~15s). Due rows claimed with
  `UPDATE agents_schedules SET claimed_by=$pod, claimed_at=now() WHERE
  next_fire_at <= now() AND claimed_by IS NULL ... RETURNING` — safe with
  multiple replicas, no leader-election component; a stale-claim sweeper
  releases claims older than the watchdog.
- `next_fire_at` computed in the schedule's IANA `timeZone` ("every morning
  at 8" means the user's 8am, DST included), stored as UTC.
- OpenClaw-grade reliability: up to 3 retries at 30s/60s/5m for transient
  errors; extended backoff (to 60m) for consecutively failing recurring
  schedules; immediate disable with `disabledReason` on permanent errors
  (revoked credentials, deleted agent); 60-minute default watchdog per run.
- Each fire creates an `AgentRun` and hands it to the runner. Concurrency per
  schedule is `Forbid`: a schedule whose previous run is still active skips
  the tick and records it.

Three schedule types, one table:

- **cron** — "do X at time T": task prompt, full run, output delivered via
  `notify` or run history.
- **wakeup** — one-shot ("check again in 2h"), created by the agent itself
  through its scheduling tools; a row with `next_fire_at` and no cron
  expression.
- **heartbeat** — the OpenClaw pattern that makes the agent feel autonomous
  rather than a cron wrapper: a periodic pulse (default ~30m) where the
  agent reviews a **standing checklist** (user-editable markdown: "anything
  in my inbox? PRs waiting on me?") using the cheap `background` model
  profile, with **output suppressed unless actionable** — it either does
  nothing quietly or escalates via `notify`. Read-only tool policy by
  default (rule 5).

## Runner, model profiles, budgets

```go
type Runner interface {
    // Start or resume a run; streams events (tokens, tool calls,
    // interrupts) and persists checkpoints via the store.
    Run(ctx context.Context, run *RunHandle) error
    Capabilities() RunnerCapabilities
}
```

- **`eino` (default, always available).** In-process loop:
  `adk.ChatModelAgent` + tools node, bounded tool turns, checkpoint
  interrupt/resume persisted to `agents_runs`.
- **`claude-code` (optional plugin).** Registers only when the
  `infrastructure` provider is detected. Provisions/attaches the agent's
  `agent-workspace` (below) and runs Claude Code headless
  (`claude -p --resume`, `stream-json`) in a pod with the workspace PVC
  mounted — session JSONL and files live on the same volume. For long
  autonomous tasks; Anthropic credentials required.
- `Agent.spec.runner: auto | eino | claude-code` — `auto` picks `eino`
  unless the task is marked long-running and `claude-code` is available.

**Model profiles.** `kedge-agents-llm` holds a small list of named profiles
(provider, baseURL, model, key) instead of one entry. Agents map purposes to
profiles: `chat` (strong), `background` (cheap — heartbeats, wakeups,
summarization), `compaction`. BYO OpenAI-compatible or Gemini, per tenant,
provider-agnostic.

**Budgets.** Every run records usage into `agents_usage`; each turn checks
the agent's rolling window against `spec.budget`. On breach: suspend
schedules + heartbeats, refuse new background runs, notify the user, keep
interactive chat available with an explicit warning (the user can raise the
cap in the portal). An always-on agent spends money while you sleep; the
hard stop is not optional.

## Agent workspace (files) — infrastructure-backed

Agents doing real work need a filesystem: downloaded files, drafts, reports,
scratch state. This is **not** built into the core provider (rule 2) — it is
the first consumer of a new minimal infrastructure Template, and the natural
successor to the deprecated `sandbox-runner`:

- **`agent-workspace` Template** (lands in
  `providers/infrastructure/install/templates/agent-workspace.yaml`): a
  minimal persistence unit — a **PVC** plus a small file-access pod (no dev
  server, no ingress, no URL). The Template declares dataplane subresources
  (`read`, `write`, `list`, `stat`, `archive`) following the Template-declared
  data-plane contract from the app-studio runtime decoupling work.
- The agents provider provisions one instance per agent on demand (via the
  infrastructure API as the calling user) and reaches files through
  `{hub}/services/providers/infrastructure/dataplane/clusters/{cluster}/
  agentworkspaces/{name}/{verb}` — never a direct kube client.
- Exposed to the agent as the `files` tool family (`file_read`, `file_write`,
  `file_list`, `file_delete`), and to the user in the portal (browse +
  download).
- The same PVC is the claude-code runner's volume: one workspace per agent,
  shared by both runners, so a task started in-process and continued by
  claude-code sees the same files.
- **When infrastructure is absent**: the `files` family doesn't register;
  agents still have memory notes. No core feature breaks.

## Tool families (built-in, in-process Go)

Registered per-agent from its grants; every family is optional and
independently testable. In-process registry (similar in spirit to
`providers/mcp/aggregate.RegisterToolFamily`) — the core tools do not require
the hub MCP endpoint.

| Family | Backing | Notes |
|---|---|---|
| `core` | store | `memory_write/list/read`, `schedule_create/list/cancel`, `wakeup`, `trigger_create/list/cancel` (register an event automation), `sessions_list/history`, **`notify`** (deliver a message to the agent's default channel connection), **`delegate`** (spawn a sub-agent run against a name in `spec.delegates`), **`ask`** (post a question to the approvals inbox and await the user) |
| `web` | Go stdlib + readability extraction | `web_fetch` (SSRF-guarded: DNS pinning, deny private ranges, per-connection allowlist), `web_search` via a `websearch` Connection. No headless browser in v1; `chromedp` family is a later opt-in |
| `github` | `github` Connection | Remote GitHub MCP endpoint with the tenant's PAT, or a bundled `github-mcp-server` binary (Go/static) over stdio. Pre-wired instead of hand-configured MCP |
| `mcp` | `mcp` Connection | Arbitrary remote MCP server (URL + auth header from the Secret), client via the official `modelcontextprotocol/go-sdk`. The extensibility escape hatch |
| `files` | infrastructure `agent-workspace` | Optional (see above) |
| `edges` | hub MCP virtual endpoint | Optional: aggregate kube/SSH tools when the tenant has `MCPServer` resources |

**Per-trigger tool policy** (rule 5): `Agent.spec.tools` grants each family
per trigger class — `interactive` (chat/channel: full grants, risky tools
behind approval) vs `background` (schedule/heartbeat/wakeup: read-only +
`notify` by default; write-capable tools only if the user explicitly opts a
schedule in). Every call lands in `agents_tool_calls`.

## Surfaces: portal, channels, notifications

**Portal — agent-first** (like app-studio's project model): the left sidebar
lists agents (the starting point) plus a footer of workspace-shared resources.
Selecting an agent opens *its* Chat / Schedules / Triggers / Channels /
Settings — schedules and triggers are filtered and created for that agent (no
agent picker), Channels sets the agent's notify/inbound connection, and
Settings edits display name, model credential, system prompt, autonomy, monthly
budget, and delegates. The shared footer holds **Models** (credentials),
**Connections** (secrets — created once, referenced by agents), and the
cross-agent **Inbox**. This keeps per-agent config inside the agent and shared
secrets outside it, mirroring app-studio. (Vite + `kedge.ready`/`kedge.context`
handshake; streaming chat with tool-call rows + approval prompts.)

**OAuth connections.** For `auth: oauth` Connections the portal starts the flow
at `/api/connections/{name}/oauth/authorize` (redirect to the provider, e.g.
GitHub App / Google / Slack). The provider's callback
`/services/providers/agents/oauth/callback` exchanges the code, stores the
refresh token in the connection Secret + `agents_oauth`, and refreshes before
expiry so tool calls always get a live token. This replaces pasted PATs for the
integrations that require OAuth.

**Event triggers.** `AgentTrigger` webhook sources expose a hub-routed inbound
endpoint (`/services/providers/agents/triggers/{trigger-id}`, signature/secret
verified); a delivered event that passes `filter` starts an `event`-triggered
run with the payload as input. `github`/`connection` sources subscribe through
the relevant Connection instead of a raw webhook. Idempotency keys in
`agents_triggers` drop duplicate deliveries.

**Channels — the feature that makes it an assistant instead of a portal
tab.** OpenClaw's core value is living where you already chat; v1 ships
**Telegram and Slack** as Connection types (bot token in the Secret,
chat/channel ID in config):

- *Inbound*: the provider exposes one webhook endpoint per channel
  connection through the hub proxy
  (`/services/providers/agents/webhooks/{connection-id}`, secret-path +
  signature verification per platform). An inbound message resolves
  connection → agent → session and starts a `channel`-triggered run; replies
  stream back to the same chat. Session commands work from the channel:
  `/new` (fresh session), `/compact` (summarize + truncate via the
  `compaction` profile), `/status`.
- *Outbound*: the `notify` tool and budget/schedule alerts deliver to the
  same connection. Long outputs get summarized for the channel with a link
  to the full run in the portal.
- *Approvals over channels*: when a run hits a tool gated by approval, the
  approval request goes out on the channel ("agent wants `github: merge PR
  #42` — approve?") and the reply resumes the checkpointed run — the
  interrupt/resume machinery already required for chat approvals, pointed at
  a different surface.
- `smtp` Connection covers outbound email notifications; inbound email is
  post-v1.

**Context compaction** is automatic as sessions approach the model's window
(summarize with the `compaction` profile, keep memory notes + recent turns),
and on demand via `/compact`. Long-lived chats are the norm here, not the
exception.

## Repository skeleton

```
providers/agents/
  main.go            # init/serve subcommands (quickstart pattern)
  init_cmd.go        # provider-sdk/install bootstrap: schemas, APIExport,
                     # endpoint slice, bind grant, CatalogEntry
  heartbeat.go
  provider.yaml      # admin Provider record
  manifest.yaml      # CatalogEntry (dev loopback URL, own port)
  apis/v1alpha1/     # Agent, Connection, AgentSchedule, AgentTrigger,
                     # AgentRun (+AgentSkill)
  api/               # REST handlers: chat SSE, agents, runs, schedules,
                     # triggers, inbox, connections, oauth callback, budgets,
                     # files proxy
  channels/          # telegram/, slack/: webhook verify, inbound routing,
                     # outbound delivery, approval round-trips
  triggers/          # event sources (webhook/channel/email/github/connection),
                     # filter eval, idempotency
  oauth/             # authorize + callback flows, token refresh per connection
  inbox/             # cross-agent approvals + questions queue
  engine/            # eino loop: model profiles, callbacks, events,
                     # checkpoints, compaction, sub-agent delegation
  runner/            # Runner interface; eino/, claudecode/ (optional)
  scheduler/         # cron/wakeup/heartbeat loop, tz handling, claims,
                     # backoff, watchdog
  store/             # store.go, postgres.go, memory.go, encryption.go
  tools/             # core/, web/, github/, mcpconn/, files/, edges/
  client/            # tenant dynamic client (cluster-ID addressed)
  portal/            # Vite micro-frontend
  install/schemas/   # APIResourceSchemas
  deploy/chart/
  Dockerfile
  go.mod             # own module; no app-studio/infrastructure imports
```

Plus one deliverable **in the infrastructure provider**:
`install/templates/agent-workspace.yaml` (PVC + file-access pod + dataplane
subresource declarations).

## Milestones

The Tier 1 resource model (autonomy, delegation, `AgentTrigger`, OAuth) is
baked into the schema from milestone 1 so later work doesn't retrofit it; the
*behavior* for each lands in the milestone noted below. Tier 2 (RAG, tracing,
quiet hours, egress controls) is staged as milestones 9–10. Status markers below
reflect the 2026-07-12 state (see [Implementation status](#implementation-status)):
✅ done · ◑ partial (per-request built, autonomous/background not) · ⬜ not started.

1. ✅ **Skeleton** — quickstart-derived scaffold, APIExport with all five
   resources (`Agent`, `Connection`, `AgentSchedule`, `AgentTrigger`,
   `AgentRun`) including the Tier 1 fields, portal shell, heartbeat, chart.
   Boots against a bare hub.
2. ◑ **Chat + store** — eino runner, SSE chat, messages/runs in the store.
   **Done** except: Postgres backend (in-memory only), tool approvals in chat
   (needs the tool loop), at-rest encryption. Model creds became *named
   credentials* (own Secret each), not the single `kedge-agents-llm`.
3. ✅ **Scheduler** — CRUD + tab + Run now, and **autonomous firing** via the
   background executor: timezone-aware cron/wakeup/heartbeat, optimistic status
   claims, watchdog timeout, disable-after-5-failures. (Exponential retry
   backoff between failures is simplified to fail-and-count.)
4. ✅ **Tools + policy** — the tool loop executes `core`/`web`/`github`/`mcp`
   families with per-trigger policy defaults, `requireApproval` gating through
   the inbox, and audit logging; tool calls render live in chat. (`autonomy`
   field not yet enforced beyond the interactive/background split.)
5. ◑ **Delegation + inbox** — inbox API + tab done and now *populated* (the
   `ask` tool and approval-gated tools post items). **Not done:** sub-agent
   delegation (`delegate`, `parentRunID` lineage, budget rollup) and
   pause/resume on approval.
6. ✅ **Channels + heartbeats** — outbound notify (Telegram/Slack/SMTP), Test
   send, background-run delivery, quiet heartbeats, **inbound chat from
   Telegram/Slack** (webhook + routing + replies), and `/new`/`/status`
   session commands. (Approvals *over the channel* and `/compact` remain.)
7. ◑ **Event triggers + OAuth** — CRUD + tab + **Fire now** + **inbound
   HMAC-tokenized webhooks** (external POST → run, URL shown in the UI) done.
   **Not done:** filters/idempotency, channel/connection event subscriptions,
   and the entire OAuth flow (authorize + callback + refresh, `agents_oauth`).
8. ✅ **Budgets** — per-agent token/USD caps enforced before every run, with a
   clear over-budget message. (Compaction and usage *alerts* not built.)
9. ⬜ **Workspace + GitHub** — `agent-workspace` Template in infrastructure,
   `files` family over the dataplane, portal file browser; `github` family
   (bundled stdio binary + remote-endpoint mode).
10. ⬜ **Optional integrations** — `edges` family behind MCPServer detection;
    `claude-code` runner sharing the workspace PVC; `AgentSkill` resource.
11. **Tier 2 — knowledge + observability** — document/URL ingestion with
    chunked retrieval (RAG) beyond memory notes; per-run trace view (steps,
    tool I/O, tokens, latency).
12. **Tier 2 — safety + notifications** — egress/exfiltration controls
    (outbound-with-data approval, egress allowlists, secret redaction in tool
    args/logs); quiet hours + notification digest batching.

## Out of scope (v1)

Voice, canvas/device nodes and local-device control (OpenClaw's local-first
identity — a server-side multi-tenant platform shouldn't compete there),
headless browser (chromedp), inbound email.

## Tier 3 backlog (post-v1, deliberately deferred)

Agent presets/gallery (one-click "PR reviewer", "inbox triage" and onboarding);
team/shared agents (multi-user access to one agent, org-scoped); cost/usage
dashboards + chargeback beyond the per-agent budget hard-stop; data export +
delete (GDPR: export transcripts, forget-me); manual dry-run of a schedule
before enabling; cross-tenant skill catalog (the `AgentSkill` sharing story,
once the resource exists).
