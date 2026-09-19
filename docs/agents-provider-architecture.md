# Agents provider — standalone skeleton design

Status: **Substantially implemented (2026-07-30). Chat with a tool loop
(core/web/github/mcp/edges), named model credentials, autonomous cron/heartbeat
firing, event-trigger webhooks with filters, sub-agent delegation, an approvals
inbox (portal + channel) with **durable pause/resume**, a **runs API with
step-level traces**, server-push events, run cancellation, channels in/out
(Telegram/Slack/Discord), OAuth connections, USD budgets, and a durable
Postgres store are built. Not built: the file workspace (needs the
infrastructure provider), context compaction, evals, and the hardening items in
[Implementation status](#implementation-status). Not yet driven end-to-end
against a running hub — integration bugs expected.**
Author: 2026-07-12 (status updated 2026-07-30)
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

**Hard dependencies: the railgrid hub and Postgres. Nothing else.** The provider
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
are not wired yet. The portal has four tabs: Agents, Activity (runs + approvals),
Connections (incl. toolsets), Models — an agent's schedules, triggers, channels,
and tools are edited inside the agent, next to a live chat playground.

### Built and usable now

- **Provider skeleton** — boots against a bare hub, registered in both Tiltfiles
  (port 8087), Helm chart, `init` bootstrap, portal micro-frontend.
- **Chat** — streaming (SSE) single-turn conversations on the Eino engine, with
  transcript + resumable run records in the store (in-memory backend; see gaps).
- **Named model credentials** — created once, each its own Secret
  (`railgrid-agents-model-<name>`), listed/created/deleted on the Models tab and
  assigned/reassigned per agent. This is what an agent uses to reach its
  provider (OpenAI-compatible today).
- **Schedules / Triggers / Connections CRUD** — full create/list/delete of the
  `AgentSchedule`, `AgentTrigger`, and `Connection` CRs from their tabs.
- **Run now / Fire now** — execute a schedule's or trigger's task as the
  calling user *asynchronously*: the endpoint returns `202 {runID}` and the run
  is followed in Activity (a synchronous variant would hit proxy timeouts on
  long tool loops).
- **Budgets** — per-agent monthly token/USD caps, enforced before every run
  (chat, run-now, fire-now); blocks with a clear message when exceeded.
- **Channel notify (outbound)** — Telegram / Slack / SMTP delivery via the
  `channels` package, with a per-connection **Test** button that sends a real
  message.
- **Approvals inbox** — list + approve/deny API, surfaced in Activity and
  pushed to the agent's channel. Resolving an approval **resumes the paused
  run** (see Durable approvals below).
- **Background executor + reconcilers** — schedules fire **autonomously**. The
  provider reads its APIExportEndpointSlice (via `RAILGRID_PROVIDER_KUBECONFIG`)
  to discover the APIExport virtual workspace. A leader-elected
  multicluster-runtime manager (`controller_manager.go`, one reconciler per CR
  under `controller/`) watches `Schedule`, `Connection` and `Agent` CRs across
  all bound tenant workspaces: a Schedule reconcile decides whether it is due,
  claims the fire with an optimistic status update (multi-replica safe), and
  sleeps on `RequeueAfter` until the next planned time — no periodic list of
  every tenant. Fires execute through an **interface-based executor**
  (`executor` package: serializable `Job` + `Handler`; in-process worker pool
  today, deliberately swappable for a durable engine like Temporal later),
  with a Pending run row recorded before the job is queued. Includes
  timezone-aware cron, one-shot wakeups, quiet heartbeats (notify only when
  actionable), disable-after-5-failures, and per-job watchdog timeouts. The
  only timer left (`AGENTS_SCHEDULER_INTERVAL`, ~30s) re-reads the endpoint
  slice and runs the stranded-run recovery sweep over Postgres.
- **Background notify** — output/failure of background runs is delivered to the
  agent channel named by the schedule/trigger's `channelRef`, else the agent's
  primary channel (`spec.channels[]`).
- **Trigger webhooks (inbound)** — webhook/github triggers get an HMAC-tokenized
  URL (shown in the Triggers tab): external `POST`s fire the agent with the
  event payload, no tenant auth needed.

- **Tool loop** — agents call tools mid-conversation via a real tool-call loop
  in the engine (bind → call → observe → continue, bounded by
  `limits.maxToolTurns`, default 16). Families shipped:
  - `core`: `memory_save/list`, `schedule_create`/`schedule_update`/
    `schedule_delete`/`schedules_list` (cron/wakeup — agents schedule and
    re-schedule *themselves*; update/delete are scoped to the calling agent's
    own schedules), `notify`, `ask` (posts a question to the inbox + channel).
  - `web`: `web_fetch` (SSRF-guarded at dial time — blocks private/loopback,
    defeats DNS rebinding) and `web_search`, backed by a `websearch`
    Connection speaking either a **self-hosted SearXNG instance** (no API key —
    the `searxng` infrastructure Template provisions one per tenant) or the
    Brave API. See [`agent-web-access.md`](./agent-web-access.md).
  - `mcp`/`github`: any `mcp` connection is dialed via the official MCP Go SDK
    and its tools exposed as `<connection>__<tool>`; a `github` connection with
    a PAT gets the hosted GitHub MCP server's full toolset with zero config.
  Per-trigger policy applies (tools are wired explicitly; an agent with nothing
  wired gets only `core`). **Every** tool — including MCP/edges tools, which
  expose only the rich executor — passes through one wrapper that gates
  approvals and writes the audit row, so no tool can dodge either. Calls render
  live in chat (`tool_start`/`tool_end`) and replay in the run trace.

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
  resolve approvals (which **resumes the paused run**), `/answer N <text>`
  answers agent questions — all from Telegram/Slack/Discord.
- **Durable approvals (pause/resume)** — a gated tool call raises an engine
  *interrupt*: the loop stops, the conversation + un-executed calls are
  serialized into the run's `checkpoint`, and the run parks in
  `PendingApproval`. Approving resumes the loop in place and executes that call
  with the **exact arguments the user saw** (one approval = one call, bound to
  its run); denying feeds a refusal observation back so the model can react.
  Resumed runs that hit another gate check-point again.
- **Runs API + traces** — runs are objects: listing them, reading one and
  watching one are kube calls (Pillar 1), narrowed server-side by the
  `agents.railgrid.ai/agent` label. What is not on the object is the `trace`
  verb: `GET …/runs/{id}/trace` returns the step-level trace (each tool call's
  args, result, outcome, duration — secrets redacted), the answer,
  pending-approval state and delegated children. `POST …/runs/{id}/cancel`
  aborts a live run and `GET …/runs/{id}/wait` long-polls one to a settled
  phase, for a caller with no watch to hold.
- **Server-push events** — the `events` verb (SSE) streams one agent's run phase
  changes and inbox activity, so the portal reflects background work without
  polling.
- **Durable background execution** — an unattended run survives a restart: the
  request is a `Run` object, not a job in memory, and the leader claims and
  executes it whenever it comes back. There is no periodic tick anywhere in the
  provider; virtual-workspace endpoints are rediscovered on demand.
- **Long-term memory injection** — saved notes are injected into every run's
  system context (bounded by `spec.memory.maxNotes`), so recall no longer
  depends on the model choosing to call `memory_list`.
- **Trigger filters** — `spec.filter` gates webhook deliveries on `eventType`
  (platform header or body `type`/`action`), `match` (payload substring), and
  `header.<name>`; filtered deliveries are acked, not run.
- **Sub-agent delegation** — the `delegate` tool: agents listed in
  `spec.delegates` can be handed a scoped task; the child runs through the
  same execution path with `parentRunID` lineage, its usage rolls into the
  parent's budget, fan-out is capped at 3 per run, and delegated runs cannot
  delegate further (depth 1).
- **OAuth connections** — `auth: oauth` with GitHub/Google/Slack presets:
  bring your OAuth app (client id/secret), click **Connect**, authorize, and
  the callback stores access+refresh tokens in the connection Secret under
  the same `token` key the tool families read. The Connection reconciler
  refreshes tokens ~15min before expiry (requeued for each expiry, not
  polled). State is HMAC-signed (no server-side session).
- **Edges family** — the hub's aggregate MCP endpoint (kube clusters + SSH
  servers, MCPServer "default") exposed as `edges__*` tools, dialed as the
  calling user. Interactive runs only (background runs have no user token).
- **Own MCP surface** — the provider serves `/mcp` (streamable HTTP,
  stateless, per-request identity), which the hub aggregate federates as
  `agents__*` tools. It covers the **whole configuration surface** — whatever
  the portal's settings screens can do, an MCP client can do:

  | Resource | Tools |
  | --- | --- |
  | Agents | `list_agents`, `get_agent`, `create_agent`, `update_agent`, `delete_agent` |
  | Schedules | `list_schedules`, `create_schedule`, `update_schedule`, `delete_schedule`, `run_schedule` |
  | Triggers | `list_triggers`, `create_trigger`, `update_trigger`, `delete_trigger`, `run_trigger` |
  | Toolsets | `list_toolsets`, `create_toolset`, `update_toolset`, `delete_toolset` |
  | Connections | `list_connections`, `create_connection`, `update_connection`, `delete_connection`, `test_connection` |
  | Model credentials | `list_model_credentials`, `save_model_credential`, `delete_model_credential`, `test_model_credential` |
  | Discovery | `list_tool_families` |

  Every mutating tool delegates to the same `apply*` helper as the REST
  handler (`applyAgentUpdate`, `applyScheduleCreate`/`Update`,
  `applyTriggerCreate`/`Update`, `applyToolsetCreate`/`Update`,
  `applyConnectionCreate`/`Update`, `applyCredentialUpsert`), so a change made
  over MCP is indistinguishable from the same change made in the portal:
  absent fields are untouched, list fields replace wholesale. Secrets are
  write-only on every tool — tokens and API keys can be set or rotated, never
  read back. Two things deliberately stay portal-only: the **OAuth Connect
  flow** (it needs a browser) and **resolving approval requests** (an agent
  must not be able to approve its own gated tool call). This is how an agent —
  or any MCP client on the aggregate — configures the whole system without the
  portal.

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
   that exists. Also blocks the claude-code runner.
2. **Context compaction.** No `/compact` and no automatic summarize-and-truncate
   — long-lived channel sessions will overflow the model window; `/new` is the
   only relief today. The `compaction` model purpose exists for this and is
   still unread.
3. **claude-code runner.** Only the in-process Eino loop exists. `spec.runner`
   was **removed from the schema** (it was a silent no-op); re-add it with the
   runner, which needs the workspace PVC (item 1) plus a `Runner` interface
   extraction.
4. **Evals / datasets.** No analog to LangSmith datasets or OpenAI trace
   grading. The run trace (steps + args + results) is now recorded, which is
   the substrate an annotation→dataset flow would build on.
5. **AgentSkill.** In the schema, no behavior; the cross-tenant skill catalog
   (the ClawHub analog) lands after the resource does.
6. **Native Gemini/Vertex.** `llm.BuildModel` implements only the
   OpenAI-compatible path (covers OpenAI, Anthropic-compat, OpenRouter).
7. **Agent config versioning/drafts.** CR edits are live immediately; the field
   standard (Agent Builder versions, Copilot Studio publish, Dify app versions)
   is draft→publish with rollback.

### Not yet implemented — hardening (works, rough edges)

8. **At-rest encryption.** Message/memory content is plaintext in Postgres (the
   columns exist; the app-studio-style key wiring isn't ported).
9. **Retry backoff.** Failed schedules count failures and disable at 5; the
   designed 30s/60s/5m escalating retry isn't implemented
   (`schedule.spec.retry.maxAttempts` is still unread).
10. **Inbound hardening leftovers.** Slack signature verification, Telegram
    secret tokens, per-event de-duplication, and payload quarantine are built
    (see [`agents-multi-channel.md`](./agents-multi-channel.md#inbound-verification-de-duplication-and-quarantine)).
    Still open: the dedup set is per process (multi-replica durability via
    the store's run idempotency index), **trigger webhook idempotency**
    (`AgentTrigger` deliveries have no platform id to key on), **inbound
    email**, and **multi-chat routing** (one connection = one configured chat,
    with no per-user identity inside it).
11. **Per-tool grant granularity.** Grants stop at family/connection: granting
    `mcp` + a connection exposes every tool that server discovers, and the
    aggregate railgrid endpoint is all-or-nothing for interactive runs. There is
    also no cached tool inventory, so every run re-dials each MCP connection
    serially.
12. **Executor durability + fairness.** The in-process pool is 4 workers
    globally (not per-tenant) and has no persistence across restarts. A full
    queue no longer drops jobs — `Submit` waits on the caller's context and
    returns `ErrQueueFull` (503 + `Retry-After` on webhooks) — but the queue
    itself is still in memory.

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
4. **Objects are kcp; the backend serves verbs.** `Agent`, `Schedule`,
   `Connection`, `Toolset`, `Trigger` and the tenant's credential Secrets are
   bound APIs in the tenant's own workspace, so every reader and writer —
   the portal, `kubectl`, another provider, the agent's own self-management
   MCP tools — goes to kcp for them. The provider's own HTTP surface carries
   only what kcp cannot answer: a chat turn, a run and its cancellation, a
   connection test, an OAuth authorize, a webhook delivery. This is the
   provider contract's Pillar 2/3 split, and it replaced an earlier rule
   ("all are REST first") that had the provider relaying CRUD the hub could
   not authorize per resource.

   The consequence is that **there are two writers and no gatekeeper between
   them.** Anything that must be true of a stored object regardless of who
   wrote it is a reconciler's `Validated` condition, not a check in a
   request handler; a handler-side check is a convenience for whoever is
   typing, and is mirrored in the portal for the same reason.
5. **Trigger-scoped trust.** What an agent may do depends on who is watching.
   Interactive chat can unlock risky tools behind approvals; scheduled,
   heartbeat, and wakeup runs default to read-only + notify-first. This is
   the primary prompt-injection mitigation: unattended runs read untrusted
   web/email content, so they don't get write-capable tools by default.

## APIExport resources (`agents.railgrid.ai`)

| Kind | Purpose |
|---|---|
| `Agent` | The persistent assistant: persona/system prompt, model profile refs (per purpose: `chat`, `background`, `compaction`), memory policy, tool grants (connection refs + toolset refs + built-in families) with per-trigger policy, limits (max tool turns, per-run timeout), **budget** (rolling token/USD cap), **`channels`** (named messaging bindings, one primary), **`autonomy`** (`suggest`/`ask`/`auto` — enforced at toolset assembly), and **`delegates`** (agent names this agent may spawn as sub-agents) |
| `Connection` | A named credential to an external system: `type` (`github`, `mcp`, `websearch`, `http`, `telegram`, `slack`, `smtp`), **`auth`** (`secret` default, or `oauth`), `secretRef` to a tenant-workspace Secret, non-secret config (base URL, allowed hosts, channel/chat IDs). For `auth: oauth`, an `oauth` block (provider, scopes) and a provider-run callback mint + refresh the token into the Secret. Connections turn tool families and channels on per agent |
| `AgentSchedule` | Time-based firing. `type: cron \| wakeup \| heartbeat`; cron spec (5-field) + **`timeZone`** (IANA name, like `CronJob.spec.timeZone`; default UTC) + task prompt (cron) or standing checklist ref (heartbeat) + `agentRef` + retry policy + `suspend`. Status: `nextRun`, `lastRun`, `consecutiveFailures`, `disabledReason` |
| `AgentTrigger` | Event-based firing — the non-time half of automation. `spec.source` (`webhook`, `channel`, `email`, `github`, `connection`) + `connectionRef` + `filter` (source-specific match: header/signature, message regex, event type, label) + `task` + `agentRef` + `suspend`. Webhook sources get a hub-routed inbound endpoint; connection sources subscribe to a Connection's event stream. Status: `lastFired`, `consecutiveFailures`, `disabledReason` |
| `Toolset` | A shareable bundle of tool grants (families, connections, approval rules) many agents can link, so wiring is written once |
| `AgentSkill` *(post-v1)* | Markdown instructions + required connection types + tool grants, attachable to agents. Later: shareable across tenants via the catalog — the ClawHub analog, which a single-user OpenClaw cannot do |

| `Run` | One execution of an Agent: the identity the platform authorizes, lists, watches and garbage-collects it by. `spec` (agentRef, trigger, sessionID, parentRunRef, sourceName, idempotencyKey, a bounded inputPreview) is written once and never edited; `status` (phase, message, owner, startedAt/finishedAt/deadlineAt, transcriptRef, usage) is the provider's alone. The object's NAME is the run id |

**A Run is a projection, not the record.** The transcript, the step-level tool
trace and the resume checkpoint stay in Postgres, keyed by the object's name —
high churn, unbounded, and of no interest to an API server. What is on the
object is what a tenant needs to ASK about a run without the provider relaying
it: which agent, what started it, what phase, when, and what it cost. That
split is the projection carve-out.

Nobody writes a Run's spec but the provider: a Run is the record of something
that happened, so "create a Run to start a run" would be a second way to start
work, racing the `run` verb that already exists. Deleting one is ordinary and
meaningful — it is how a tenant discards a run — and the finalizer turns that
into a purge of the rows behind it. An ownerReference to the Agent means
deleting an agent garbage-collects its runs, and each one's finalizer purges
its own rows on the way out.

**Inbox items stay Postgres-only,** under the same carve-out, and deliberately:
an inbox item is a pause in a run, it has the lifetime of that run, and giving
it a CR of its own would mean a second object whose deletion semantics have to
be kept in step with the run's for no gain in what a tenant can authorize.
They are addressed as verbs on the agent that raised them
(`agents/{n}/inbox`, `agents/{n}/inbox-resolve/{id}`), which is the object a
tenant can actually grant approval rights over.

Tenant-facing permission claim: `secrets` (tenant-scoped), and nothing else.
Three names, all under this provider's own prefixes:

| Secret | Written by | Why the provider touches it |
|---|---|---|
| `railgrid-agents-model-<name>` | the tenant (portal, kube client) | read, to call the model on the tenant's behalf |
| `railgrid-agents-conn-<name>` | **the provider** | the OAuth callback stores access + refresh tokens; the Connection reconciler generates the Telegram `secret_token` / Slack signing secret that make an inbound webhook verifiable |
| `railgrid-agents-llm` | the tenant | the legacy single-credential Secret, read for workspaces that predate per-name credentials |

There are no `serviceaccounts` / `clusterroles` / `clusterrolebindings` claims:
an agent's unattended identity is **minted by the hub**, scoped to the exact
Instances that agent references, and TTL'd — the provider never writes RBAC
into a tenant's workspace. There are no `tokenreviews` / `subjectaccessreviews`
claims either: service callers use the same data-plane verbs and the same two
gates as everyone else, so the provider runs no reviews of its own.

**Per-agent identity.** An interactive run acts as the human driving it. An
unattended one asks the hub for an identity of its own
(`provider-sdk/identityclient`), with rules built from what the agent actually
references:

- `get` on the **named** Instances its Connections and Toolsets point at, and
  `create` on those instances' declared `{resource}/{verb}` subresources —
  which is how the data plane expresses "may exec/proxy this one";
- `get` on the workspace's APIBinding for the instance API group and `use` on
  its default `MCPServer`: the two objects the provider reads on the agent's
  behalf to find out *where* to send a call. An agent wired to nothing still
  gets these, because resolving an endpoint is not access to anything.

What that replaced is worth stating, because it was the sharpest edge in the
provider: a ServiceAccount the provider wrote into the tenant's workspace with
its own claimed credentials, holding a ClusterRole that granted `get`+`list` on
*every* resource in `infrastructure.railgrid.ai`, with a token that never
expired and rules that were create-if-absent so a grant never shrank. That
token, read out of the workspace, reached any instance there — including a
browser instance holding live logins the agent was never wired to. The rules
are now re-stated on every refresh, so removing a Connection removes the access
it carried, and the Agent's purge finalizer revokes the identity outright
rather than waiting out a TTL.

### Autonomy and the approvals inbox

`Agent.spec.autonomy` sets the default posture — `suggest` (draft only, never
act), `ask` (act after approval), `auto` (act freely within tool policy) — and
the per-trigger `requireApproval` lists refine it per tool. Autonomy is applied
when the toolset is assembled: `suggest` rewrites the approval list to `*`,
`auto` clears it, `ask` keeps the grant's own patterns. A small exempt set
(`notify`, `ask`, `memory_*`, `wait`, `schedule_list`) is never gated — under
`suggest` an agent must still be able to reach the user.

When a gated call comes up, the tool wrapper raises an **interrupt** rather
than executing: the engine unwinds the loop, the api layer serializes the
conversation and the un-executed calls into the run's `checkpoint`, parks the
run in `PendingApproval`, and writes an **inbox item** carrying the run ID and
the exact requested arguments. The inbox is a single cross-agent queue of
pending approvals and agent questions, surfaced per agent by the `inbox` verb,
merged in Activity, and pushed to the agent's primary channel.

Resolving it (portal button or channel `/approve`) **resumes the checkpointed
run in place**: the run is claimed (so a double-approve can't run it twice),
the loop rehydrates from the checkpoint, and the approved call executes with
the arguments the user actually saw — an approval authorizes exactly one call
and cannot be replayed onto different arguments. A denial feeds a refusal
observation back so the model can adapt instead of failing. Runs resumed into
another gate check-point again. This is what makes unattended agents safe to
grant real tools.

### Sub-agent delegation

An agent runs a flat loop by default. `Agent.spec.delegates` lists other agent
names it may spawn; a `delegate` tool in the `core` family starts a child
run (trigger `delegation`, `parentRunID` set) against the named agent
with a scoped task, streams its result back, and counts its usage against the
parent's budget. Eino's ADK/DeepAgent provides the sub-agent primitive; the
provider adds the run lineage and budget rollup. Depth and fan-out are bounded
by provider limits to keep a delegation tree from runaway spend.

## The route surface

Everything a tenant can ask this provider to DO is one shape:

```
/services/providers/agents/dataplane/clusters/{clusterID}/{resource}/{name}/{verb}[/{tail}]
```

`provider-sdk/serve` assembles the server from the closed list of Pillar 2
route classes, so the layout is not this provider's to invent:

| Path | Class | What |
|---|---|---|
| `/healthz`, `/readyz` | (c) | liveness; virtual-workspace readiness |
| `/mcp`, `/mcp/sse` | (b) | the MCP projection the hub's aggregate federates |
| `/dataplane/…` | (a) | every tenant verb, gated as the caller |
| `/oauth/callback`, `/oauth/providers` | (d) | the browser OAuth popup flow |
| `/webhooks/triggers/…`, `/webhooks/channels/…` | (g) | signed inbound hooks |
| everything else | — | the portal bundle |

There is no `/api/*` and no `/s2s/*`. `serve.New` refuses to register the
first; the second was deleted rather than moved, and why is the interesting
part.

### The verbs

| Resource | Verb | Method | Notes |
|---|---|---|---|
| `agents` | `chat` | POST | one assistant turn, streamed (SSE) |
| `agents` | `run` | POST | start an unattended run |
| `agents` | `sessions` | GET | list chat sessions |
| `agents` | `session` | DELETE | `…/session/{sessionID}` — erase one transcript |
| `agents` | `messages` | GET | one session's transcript |
| `agents` | `usage` | GET | this agent's cost/token/latency rollups |
| `agents` | `inbox` | GET | this agent's pending approvals and questions |
| `agents` | `inbox-resolve` | POST | `…/inbox-resolve/{itemID}` |
| `agents` | `events` | GET | this agent's activity, streamed (SSE) |
| `agents` | `model-test` | POST | probe a model credential with a real request |
| `agents` | `model-discover` | POST | list the ids the credential's endpoint serves |
| `runs` | `trace` | GET | the run's step trace and answer (Postgres) |
| `runs` | `wait` | GET | block until the run settles |
| `runs` | `cancel` | POST | ask a run in flight to stop |
| `connections` | `test` | POST | send a test message |
| `connections` | `enable-inbound` | POST | register the inbound webhook |
| `connections` | `authorize` | POST | start the OAuth flow |
| `schedules` | `run` | POST | fire now |
| `triggers` | `run` | POST | fire now |

Every one of them is declared in `spec.dataPlane.verbs` on the CatalogEntry, in
`manifest.yaml` and the chart's copy. A Go test compares the two files
textually and both against the route table, because a verb that is served but
not declared cannot be granted to a workload identity, and one that is declared
but not served mints a capability whose calls 404.

### Why the service-to-service route is gone

`POST /s2s/clusters/{c}/agents/{n}/runs` existed because the rest of the
provider assumed a human: the hub authenticates the caller, resolves their
workspace and injects `X-Railgrid-*` headers, and that chain runs on a User CR
and a Membership. A caller with neither had no way in, so the provider grew its
own authentication (TokenReview against the token's home cluster), its own
authorization (SubjectAccessReview on an invented `agents/delegate`
subresource), a cluster→workspace map in Postgres to find the tenant, and a
review client built from the provider's own kubeconfig.

None of that is needed once the route carries the cluster in the PATH. A
ServiceAccount presenting its own bearer passes the same two gates a human
does — `get` on the agent, `create` on `agents/run` — evaluated by the tenant's
own RBAC in the tenant's own workspace. So the bespoke route, the
`agents/delegate` SAR, the `agents_tenants` lookup and the review client were
all deleted, and the `tokenreviews` / `subjectaccessreviews` permission claims
exist only until §8 PR 6 finishes retiring the last user of them.

Two things follow that are worth stating:

- **Who may start a run is not whose identity it runs with.** A run started
  through `agents/{n}/run` executes as the AGENT, through the APIExport virtual
  workspace, exactly as a scheduled run does — so authorizing a caller to start
  a run never lends the agent that caller's reach. Only `chat`, which is
  interactive and holds a stream open for a person, runs as the caller.
- **The org/workspace scope still has to come from somewhere.** It is read from
  kcp as the caller, as before; when that read is refused — a service identity
  minted for one verb has no business also holding a read on the workspace's
  `LogicalCluster` — the provider falls back to the cluster→workspace mapping
  it recorded the last time someone who could read it came through.

### What was dropped rather than moved

- **`/api/whoami`** — a debugging echo of the headers. The headers it echoed
  are no longer what addresses a request.
- **`/api/capabilities`** — "which providers can an agent here reach?". The
  provider used to answer it by dialling the hub's aggregate MCP endpoint and
  splitting tool names. The hub's own reconciler already computes it and writes
  it to `MCPServer.status.federatedProviders`, so the portal reads the object
  (Pillar 1) and the MCP discovery tool reads it through the same cached path.
- **`/api/catalog`** — the curated model catalog: compiled-in prices and
  context windows with no tenant content in them. It ships as a bundle asset
  (`portal/public/model-catalog.json`, regenerated by
  `go run ./internal/gencatalog`), and a Go test fails if it drifts from
  `llm.Catalog()` — the only way a user gets quoted one price and billed at
  another.
- **The hardcoded aggregate-MCP path.** `edgesEndpoint` composed
  `<hub>/services/mcpserver/<cluster>/apis/railgrid.ai/v1alpha1/mcpservers/default/mcp`
  — the hub's routing shape restated in a provider that has no business knowing
  it. It reads `MCPServer.status.URL` now.
- **The hardcoded infrastructure data-plane path.** `tools/tools.go` built
  `/services/providers/infrastructure/dataplane/clusters/…` by hand, which
  hardcodes both another provider's name and the grammar. It now derives a
  candidate provider name from the Instance API group (its first label — the
  convention the hub follows when naming a provider's APIBinding), GETs that
  one named binding in the tenant's workspace, checks it really exports the
  group, and builds the URL with `dataplane.ProviderPath`. The candidate is a
  guess; the binding is the answer, and a mismatch is refused rather than
  turned into a URL for the wrong provider. A named `get` rather than a list on
  purpose: it is a grant an agent's own scoped identity can hold, so an
  unattended run resolves this for itself.

## Storage (own, Postgres)

Mirror the app-studio `store` shape (interface + `postgres.go` + `memory.go`
dev backend), tables scoped by org/workspace/agent. Content is stored in
plaintext; at-rest encryption is the database's responsibility today
(application-level encryption is planned, see item 8 above):

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
*state* (transcripts, checkpoints, usage, run cancellation). Fire times are
the one piece of state that lives on the CR (`Schedule.status.nextRun` /
`lastRun` / `observedGeneration`), because that is what the reconciler claims
against. Runs are Postgres rows and deliberately **not** a CR.

## Scheduler (multicluster-runtime reconcilers)

Reconcilers, not a ticker. `controller_manager.go` builds a
multicluster-runtime manager over the provider's APIExport virtual workspace
(`provider-sdk/apiexportprovider`, one endpoint per kcp shard), gated by a
Lease in the provider workspace (`provider-sdk/leaderelection`) so a scaled
deployment keeps every CR single-writer. Readiness (`/readyz`) reports whether
the virtual workspace is reachable *and* being watched.

- `controller/schedule` — one reconcile per Schedule event or requeue.
  `internal/schedulepolicy` decides from spec + status whether it is due; a
  spec edit (generation bump) drops the stale `nextRun` and re-arms from the
  new cron/timezone/runAt. A due schedule is claimed by updating `status`
  against the resourceVersion read (a conflict means another replica or a
  newer event won — drop out, the watch re-delivers), then submitted to the
  executor, which records a Pending run row before queueing. The reconciler
  then returns `RequeueAfter` until the next planned fire (clamped to
  [1s, 1h]). A restart or leadership change re-derives the same answer from
  the same CR state, so a fire missed while nobody was watching fires on the
  first reconcile after, and a claimed fire is never claimed twice.
- `controller/connection` — the per-tick Connection housekeeping, now
  event-driven: Slack/Telegram inbound verification material (park a Slack
  connection without a signing secret in `Error`; generate a Telegram
  secret_token and re-register the webhook), OAuth refresh requeued at
  `expiry − 15m`, and the Discord gateway desired state (the socket map stays
  in-process; the reconciler adds and removes sessions). Credential Secrets
  are watched too, so an OAuth callback or a pasted secret re-triggers at
  once. Status `Ready`/`Error` for these concerns is written only here.
- `controller/agent` — stamps `phase: Ready` on agents that have none.
- `controller/run` — the durable queue for unattended work, plus each run's
  deadline and the purge of its store rows on delete.

  **The object is the queue.** A schedule fire, a trigger webhook or an inbound
  channel message writes a Pending `Run` and returns; nothing is enqueued in
  memory. This reconciler claims one by writing `status.owner` through the
  status subresource, and optimistic concurrency settles the race — a loser
  sees a conflict and drops the run rather than executing it twice. Two things
  the in-process channel could not do follow directly: a restart loses nothing
  (the watch re-delivers every Pending Run, so unclaimed work is picked up by
  definition), and a saturated pool no longer refuses an inbound delivery with
  503 + Retry-After, because a write that returns is the end of the producer's
  responsibility.

  Because only the leader reconciles, an owner that is not this process is a
  previous leader: a run still unstarted under one past `ClaimGrace` is
  re-claimed, and one left *executing* is handed to the recovery policy in
  `api/recover.go` — resume from its checkpoint, or close it honestly and tell
  whoever was waiting. `status.attempt` caps both, so a run that kills whatever
  picks it up is closed instead of taking the provider down on every restart.

  A run a person is watching is never claimed here: it executes on the replica
  that served their request and carries no unattended delivery kind. That is
  the one distinction keeping a chat turn from being charged twice. The deadline is `status.startedAt` plus the agent's
  `spec.limits.timeoutSeconds` (default 30m, capped at 2h), written on the
  object when the run starts and then requeued to wake at exactly that instant
  — the sanctioned computed-deadline `RequeueAfter`, not a poll standing in for
  a watch. It reports the timeout rather than killing the run: the executor
  holds the run's context and is what stops it, and the two meet at the durable
  cancel flag the engine reads between tool rounds. The limit is read once, when
  the run starts, so editing an agent cannot shorten work already in flight.
- Timezone-aware cron (`timeZone`, DST included), one-shot wakeups, quiet
  heartbeats; immediate disable with `disabledReason` on permanent errors (bad
  cron, missing runAt) and after 5 consecutive failed runs; per-job watchdog
  timeout in the executor.
- Cancellation is durable: `POST …/runs/{id}/cancel` sets
  `cancel_requested` on the run row, which the engine checks between tool
  rounds on whichever replica is executing, a queued job checks before
  starting, and the recovery sweep honours instead of resuming.

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

**Model profiles.** `railgrid-agents-llm` holds a small list of named profiles
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
| `web` | Go stdlib + readability extraction | `web_fetch` (SSRF-guarded: the URL comes from the model, so private ranges are refused), `web_search` via a `websearch` Connection (`config.provider`: `searxng` self-hosted, or `brave`) whose baseURL is user-authored configuration and so may be private/in-cluster. Link-local is refused on both paths — that range carries cloud instance-metadata. A real browser is not in this family: the `browser` infrastructure Template runs Playwright MCP and is wired as an ordinary `mcp` Connection |
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
secrets outside it, mirroring app-studio. (Vite + `railgrid.ready`/`railgrid.context`
handshake; streaming chat with tool-call rows + approval prompts.)

**How the portal reads and writes.** The micro-frontend has two data paths,
and `portal/src/api.ts` is the single entry point to both. Objects go to kcp
through portalkit's kube client (`portal/src/resources.ts`,
`createKubeClient({ fetch: providerFetch(ctx), cluster: ctx.tenant })`):
creates are a plain `create` so a duplicate name is still a 409, edits are JSON
merge patches — which replace list and map fields wholesale, matching what the
Go patch helpers did — and Secrets are server-side applied. Verbs go to
`/services/providers/agents/dataplane/clusters/{cluster}/…`, addressed by the
same `ctx.tenant` cluster ID the kube half uses, so both halves move together
on a workspace switch. The read shapes did not change in the move: the deleted
CRUD handlers returned the raw CRs, so the portal's `Agent`, `Schedule`,
`Connection`, `Toolset` and `Trigger` types were already the Kubernetes
objects.

Because the grammar addresses an object, the feeds that have no object of their
own — inbox, usage, the event stream — are per-agent verbs, and `api.ts` fans
out over the agents the caller can see and merges. That is the honest shape: a
caller sees exactly the agents they may see. Activity is no longer among them —
a run IS an object, so the feed is a kube list with a label selector and the
provider serves no route for it at all.

Two consequences worth stating plainly. A model credential's Secret now reaches
the browser on a list, exactly as `kubectl get secret` would for the same user
in the same workspace — the key is the user's own and the provider no longer
stands between them; `listCredentials` projects it away immediately and no view
ever sees it. And a `Trigger`'s `status.webhookPath` is minted by the Trigger
reconciler rather than by whoever created the object, because the token is an
HMAC the provider keys and the browser must never hold.

**Sidebar sub-nav.** `CatalogEntry.spec.ui.children` declares Agents, Activity
and Connections. The portal composes each as `/providers/agents/<builtinRoute>`
and pushes the trailing segment back as `railgridContext.subPath`, which
`portal/src/router.ts` (`routeForSubPath`) maps onto this element's own hash
route. Schedules and triggers are deliberately not children: they are edited on
an agent's Automation tab, not as a workspace-level collection, so there is no
route to point a sidebar entry at.

**OAuth connections.** For `auth: oauth` Connections the portal starts the flow
with the `authorize` verb on the Connection (redirect to the provider, e.g.
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
  provider.yaml      # admin Provider record
  manifest.yaml      # CatalogEntry (dev loopback URL, own port)
  apis/v1alpha1/     # Agent, Connection, AgentSchedule, AgentTrigger,
                     # AgentRun (+AgentSkill)
  api/               # dataplane.go is the whole route table; the rest are the
                     # verb handlers it dispatches to (NOT object CRUD — see
                     # design rule 4): chat SSE, run, runs, sessions, messages,
                     # usage, inbox, events, model-test/discover on an agent;
                     # test/enable-inbound/authorize on a connection; run on a
                     # schedule or trigger. Plus the OAuth callback, the signed
                     # webhooks, and the MCP tools, which are the one place the
                     # apply*Create/apply*Update builders still run
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
   credentials* (own Secret each), not the single `railgrid-agents-llm`.
3. ✅ **Scheduler** — CRUD + tab + Run now, and **autonomous firing** via the
   Schedule reconciler + background executor: timezone-aware
   cron/wakeup/heartbeat, optimistic status claims, watchdog timeout,
   disable-after-5-failures, durable cancel. (Exponential retry backoff between
   failures is simplified to fail-and-count.)
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
