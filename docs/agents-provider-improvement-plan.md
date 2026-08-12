# Agents provider — improvement plan (handover)

Status: **Phases 0, 1, 4 and 3.1/3.3 implemented in the backend (2026-07-30);
Phase 2 (portal) implemented in the same change. Remaining: 3.2 (compaction),
3.4 (per-tool grants + tool inventory cache), 3.5 (versioning), 3.6 (retry
backoff), 3.7 (template gallery), 3.8 (deferred set).**
Date: 2026-07-30 · Scope: `providers/agents/` (Go backend + portal micro-frontend)

## What landed (2026-07-30)

Backend — `providers/agents/`:

- **0.1** `wrapTool` now normalizes every tool onto one audited `ExecRich` path
  (`api/toolset.go`), so MCP/edges tools can no longer dodge approval gating or
  the audit log. Regression test: `api/approval_test.go:TestWrapToolGatesExecRich`.
- **0.2/0.3** Audit rows carry `RunID`, full args + result with secret-key
  redaction (`redactArgs`) and rune-safe truncation (`safeTruncate`);
  `store.ToolCall.ArgsDigest` → `Args`/`Result` with an in-place Postgres
  migration.
- **0.4** Trigger filters are enforced (`triggerFilterAllows`: `eventType`,
  `match`, `header.<name>`; unknown keys ignored rather than failing closed).
- **0.7** `writeResourceError` maps validation/permission failures to their own
  status classes instead of blanket 502.
- **1.1–1.4** `GET /api/runs`, `GET /api/runs/{id}` (step trace + pending state
  + delegation children), `POST /api/runs/{id}/cancel` (live-run registry),
  `GET /api/events` (SSE run/inbox push with `id:` + keepalive), async run-now
  (202 + runID) for schedules and triggers, chat SSE reshaped to
  `start`/`delta`/`tool_start`/`tool_end`/`approval_required`/`done`/`error`.
- **1.6** Tool turns persist into the session transcript and replay into later
  turns.
- **3.1** Durable approval pause/resume: `engine.InterruptError` +
  `Checkpoint` + `ResumeTurnWithTools`, `api/resume.go`, resume wired to both
  the portal inbox and channel `/approve`. An approval authorizes exactly one
  call with the exact requested arguments.
- **3.3** Long-term memory notes are injected into every run's context.
- **Phase 4** Deleted: the Run CRD (runs are store-native), `spec.runner`,
  `defaultNotifyConnection` + its bridge. Wired: `spec.autonomy` (enforced at
  toolset assembly), `spec.limits.timeoutSeconds`. `make codegen-agents-provider`
  regenerated 5 schemas.

Portal — `providers/agents/portal/` (**Phase 2 in full**): ported to Lit
(keyed rendering replaces the innerHTML-rebuild model, killing the form-wipe /
focus-loss / rerender-per-token bug class); nav collapsed 7 → 4 (Agents ·
Activity · Connections · Models); the agent editor is now one canonical
config pane beside a live chat playground, with a Runs tab; **Activity + run
trace viewer** added (paged runs, pinned approvals, step timeline, delegation
children, cancel, approve/deny-to-resume); chat rewritten (markdown, correct
SSE parsing, incremental streaming, scroll-pin-only-at-bottom, multiline
composer, Stop→cancel, tool cards, approval cards, transcript rehydration);
`/api/events` subscription with backoff + poll fallback replaces
load-once-and-go-stale; `{data,loading,error}` slices give real empty/error
states; creation wizard; modal focus trap fixed; schedules+triggers unified
into one component; responsive + a11y pass. The Flow canvas was **deleted**
(1,750 lines) rather than ported read-only — the config pane now shows the
same wiring as editable, keyboard-accessible form state. Backend follow-ups
this surfaced (patchable `description` + `limits`, `?since/until` on runs,
`schedule`/`trigger` push events, `?limit=` on messages) were implemented.

Also landed alongside (not part of the original plan): **keyless web search**.
`web_search` became pluggable (`config.provider`: self-hosted `searxng` or
`brave`), the SSRF guard was split by trust origin — a model-supplied
`web_fetch` URL still refuses private ranges, while a user-configured search
endpoint may be private/in-cluster (link-local refused on both) — and
`GET /api/capabilities` reports which providers the tenant's aggregate MCP
endpoint federates, so the portal can offer agent-assisted setup only where it
would actually work. The `searxng` and `browser` infrastructure Templates and
the tenant-Secret token bridge are covered in
[`agent-web-access.md`](./agent-web-access.md).

Verification: `go build ./...` + `go test ./...` green in `providers/agents`;
`make codegen-agents-provider` produces a clean diff; portal `tsc --noEmit`
clean and 34 vitest tests pass; `make build-agents-provider` produces a binary
that boots clean and serves the embedded portal.

Net footprint: 55+ files, roughly −600 lines overall — the new runs API, trace
viewer, durable approvals, and event streaming cost less than the Flow canvas,
`actions.ts`, and six replaced views that came out.

This document is a self-contained handover for an implementing agent. It was produced by a
four-track review (API/domain model, tools wiring/execution, portal UI, feature parity vs the
2026 agent-platform field). Every claim carries a source reference (`file:line`, relative to
`providers/agents/` unless stated otherwise). Line numbers are as of commit `4d9a8444`
(branch `main`, 2026-07-30) — re-grep for the symbols if lines have drifted.

**Explicit constraint from the owner: NO backwards compatibility is required.** Schemas, API
shapes, portal routes and stored spec fields may be broken/renamed/deleted freely.

---

## 0. Orientation — how the provider is put together today

Read these first:

- `docs/agents-provider-architecture.md` — 569-line master design ("server-side multi-tenant
  OpenClaw alternative"). Its own "status" section lists known gaps; this plan supersedes its
  ordering.
- `docs/agents-provider-research.md` — why Eino (not adk-go/Temporal), claude-code as future
  runner, in-provider Postgres cron.
- `docs/agents-multi-channel.md` — named-channel routing design (landed as #478).

Key facts an implementer needs:

| Thing | Where | Notes |
|---|---|---|
| CRDs (Agent, Connection, Run, Schedule, Trigger, Toolset) | `apis/v1alpha1/` | cluster-scoped, status subresource; `Run` CRD exists but is **never instantiated** — runs live only in the store (`client/client.go:44` registers `RunGVR`, zero callers of `.Runs()`) |
| HTTP API + routes | `api/server.go:123-198` | REST DTOs + one SSE endpoint (chat) |
| Single execution path (chat, run-now, background, channel, delegation) | `api/run.go:112` (`executeTask`) | budget check → model+fallbacks → toolset build → stream loop → persist |
| LLM loop | `engine/engine.go:129` (`StreamTurnWithTools`) | Eino, stateless, serial tool calls, max turns = `spec.limits.maxToolTurns` capped 32 default 16 (`api/run.go:139-142`) |
| Toolset assembly | `api/toolset.go:57` (`buildToolset`) | grants by trigger class (`toolset.go:31,100-103`), Toolset CR expansion (`toolset.go:188-217`), MCP dial per run (`toolset.go:124-147`), edges aggregate MCP interactive-only (`toolset.go:150-163`), approval/audit wrapper (`toolset.go:221-246`) |
| Background executor | `api/background.go` | 30s poll over APIExport VW, optimistic status-claim, in-process 4-worker pool (`executor/executor.go`) |
| Channels | `channels/channels.go`, `api/channels_inbound.go`, `api/discord_gateway.go` | telegram/slack/discord in+out, smtp out, slash commands `/new /status /inbox /approve /deny /answer` (`channels_inbound.go:148-235`) |
| Store | `store/store.go` (iface), `store/postgres.go`, in-memory impl | Messages, Runs (with unused `Checkpoint` col), Memory, InboxItems, ToolCalls, Usage, Sessions, TenantRefs |
| Portal | `portal/src/` (~5.8k lines TS) | vanilla custom element `faros-provider-agents`, full-`innerHTML` re-render + manual re-wire (`element.ts:142-160`), hash router (`router.ts`), module-singleton view state, one 610-line namespaced stylesheet |
| Host embedding | repo `portal/src/pages/ProviderFrame.vue:92-176` | loads `/ui/providers/agents/main.js`, sets context property on the element; **no iframe/postMessage** (portal/README.md is stale on this) |
| Build | `make build-agents-provider` (embeds portal), `make build-agents-provider-portal`, `make codegen-agents-provider`, `make agents-db-up/down`, `make run-provider-agents` | portal: `npm run build` / `npm run typecheck` in `portal/` (no tests exist) |

Strengths to **preserve** (do not regress while restructuring):

- Interactive-vs-background tool grants keyed off trigger class — the platform's core trust
  story (`apis/v1alpha1/types_agent.go:237-271`, `api/toolset.go:31`).
- Heartbeats with "OK" output suppression (`api/background.go:451`).
- Channel-native approvals (`/approve` from Telegram) (`api/channels_inbound.go:159-174`).
- Budget hard-stop with USD from the pricing catalog (`api/run.go:43-64,115,179`), delegation
  budget rollup to parent (`api/toolset.go:94-96`).
- MCP `initialize` instructions injected as system messages (`api/toolset.go:170-175`,
  `api/run.go:210-212`); image observations (`engine/engine.go:69-88,343-374`).
- Optimistic-claim schedule dedup across replicas (`api/background.go:294-306`).
- Portal: type-driven connection forms with setup guides (`portal/src/conn-defs.ts`), the
  Models/usage dashboard (`portal/src/views/models.ts`), "families are derived, never
  hand-picked" rule, portalkit host contract (`portal/src/portalkit/`).

---

## Phase 0 — Correctness & security fixes (do first, independent of everything else)

### 0.1 Approval + audit bypass for all MCP tools — **security bug, highest priority**

`wrapTool` wraps only `t.Exec` (`api/toolset.go:223-224`). The engine prefers `ExecRich`
when present (`engine/engine.go:197-199`). Every MCP-discovered tool sets **only** `ExecRich`
(`tools/mcp.go:114`) — and MCP is exactly the dangerous surface (edges/infrastructure/code via
the aggregate endpoint, `api/agents.go:520-525`). Consequences:

- `requireApproval` patterns (e.g. `edges__*`) never gate MCP tools — only built-in core/web.
- MCP tool calls never reach the `AppendToolCall` audit log (`api/toolset.go:238-243`).

Fix: wrap both paths (wrap `ExecRich` when set, else `Exec`), or better, normalize to one
execution interface at the wrapper boundary so future exec variants can't dodge it. Add a unit
test that builds a toolset containing an `ExecRich`-only tool with a matching
`requireApproval` pattern and asserts the call is blocked pending approval AND audited.
`api/toolset_test.go` exists as a home.

### 0.2 Link tool calls to runs

`store.ToolCall.RunID` exists (`store/store.go:151`), Postgres has the `run_id` column
(`store/postgres.go:594`), but `wrapTool` never sets it. Thread the run ID (created at
`api/run.go` before streaming) into the wrapper. This is a hard prerequisite for the trace
viewer (Phase 1.1 / 2.5).

### 0.3 Stop clipping audit args/results to 300 chars — store full payloads

`clipArgs` (`api/toolset.go:307`) clips to 300 bytes (byte-slicing, can split UTF-8 mid-rune —
same bug in `truncate`, `api/background.go:559`). `store.go:146` claims args are
"redacted/hashed" — they are not; they're just clipped. Decide and implement one policy:

- Store full args + full result (or spill >N KB to a blob column/table), AND
- Redact values of keys matching a denylist (`token`, `password`, `secret`, `authorization`)
  before persisting.

Without full results there is nothing for a run-detail view to show (see 1.1).

### 0.4 Trigger filter is declared but unenforced

`TriggerSpec.Filter` documents `eventType`/`labels`/`match`/`path` (`apis/v1alpha1/types_trigger.go:76-80`)
but `webhookTrigger` (`api/background.go:572`) fires on every POST. Either implement filter
evaluation before enqueueing the run, or delete the field (Phase 4 table). Recommendation:
implement `eventType` + `match` (substring on body) minimally; it's load-bearing for
"agents react to events" being safe.

### 0.5 Portal SSE parser bugs

`parseSSE` (`portal/src/api.ts:100-107`) joins multi-line `data:` fields with `.trim()` and no
`\n` separator (spec says join with newline), and frame-splits only on `\n\n` (no CRLF
tolerance). Fix now (tiny), or fold into the Phase 2 chat rewrite — but note the backend may
start emitting multi-line JSON at any point.

### 0.6 Modal: global Enter confirms regardless of focus, no focus trap

`portal/src/portalkit/modal.ts:97-99` — Enter anywhere confirms, including delete confirms.
Add focus trap + scope keydown to the dialog. (portalkit is a synced kit — check whether other
providers consume the same file and sync the fix.)

### 0.7 Smaller correctness items (batch into any nearby PR)

- `resolveInboxItem` maps every store error to 404 (`api/inbox.go:70`).
- `writeResourceError` maps kcp admission 400s to 502 UpstreamError (`api/http.go:86` area) —
  users see "upstream error" for their own validation mistakes.
- `testCredential` returns 200 `{ok:false}` (`api/settings.go:152`) while `testConnection`
  returns 502 on failure — pick one convention (recommend 200 + structured result for both).
- `updateConnection` non-atomic spec-then-secret write can report failure after the spec
  already changed (`api/connections.go:200-226`).
- Approval consumption is keyed by agent+tool name only — approved call may execute with
  different args than requested; inbox item doesn't record RunID even though the field exists
  (`api/toolset.go:270-305`). Minimal fix: bind approval to an args digest + run ID. Full fix
  is Phase 3.1 (durable pause/resume).
- Tenant scope side-channel: background transcripts land under `{org:"unmapped", ws:clusterID}`
  until a user first opens the UI (`api/background.go:462-468`, `api/http.go:144`); add a
  migration/remap on first mapping.

---

## Phase 1 — API prerequisites for a usable UI

The portal cannot get meaningfully better without these. All new; no compat concerns.

### 1.1 Runs API (the single biggest enabler)

Today there is **no endpoint that lists runs or shows a run** — the UI's only window into a
background execution is a 200-char toast (`portal/src/actions.ts:236,279`). All the data
already exists in Postgres (runs table with phase/usage/attempt, tool-call audit rows,
messages). Add:

```
GET  /api/runs?agent=&class=interactive|background&phase=&session=&cursor=&limit=
     → { items: [RunSummary], nextCursor }
     RunSummary: { id, agent, trigger, sessionID, phase, startedAt, finishedAt,
                   inputPreview, usage{inputTokens,outputTokens,usd}, attempt,
                   scheduleRef?, triggerRef?, parentRunID? }

GET  /api/runs/{id}
     → { ...RunSummary, input, output, error?,
         steps: [ { seq, type: "tool"|"message"|"delegation",
                    tool?, args, result, durationMs, at, truncated?: bool } ],
         children?: [RunSummary] }   // delegation lineage via parentRunID

POST /api/runs/{id}/cancel           // see 1.3
```

Implementation notes:
- Steps come from tool-call rows once 0.2/0.3 land; order by seq/timestamp.
- Keep the K8s-shaped CR endpoints as they are, but new store-backed endpoints use plain
  camelCase DTOs — see 1.5 for the envelope decision.
- `usage.go:77` currently pulls 5000 runs and filters in-process — reuse the new paged run
  listing under the rollup, or add SQL aggregation.

### 1.2 Async run-now

`POST /api/schedules/{name}/run` and `/api/triggers/{name}/run` currently execute the whole
agent loop synchronously inside the HTTP request with no timeout (`api/schedules.go:222`,
`api/triggers.go:267`) — proxies will kill long runs. Change both to enqueue on the executor
and return `202 { runID }`; the UI then follows the run via 1.1/1.4. (Drop the sync mode
entirely — no compat needed.)

### 1.3 Run cancellation

No cancel exists; `Aborted` phase is unreachable; a background job can only die via the
executor's global 10-min watchdog (`executor/executor.go:100-106,157-162`). Add a registry of
live run contexts (map runID→cancelFunc, in-process is fine — the executor is in-process),
`POST /api/runs/{id}/cancel`, and set phase `Aborted`. Also honor
`spec.limits.timeoutSeconds` (`apis/v1alpha1/types_agent.go:296` — currently **never read**;
its "3600s default watchdog" doc comment is fiction) by wrapping `executeTask`'s context.

### 1.4 Server-push events for UI freshness

The portal loads everything once per tenant and never refreshes (`portal/src/element.ts:102-119`,
no polling anywhere) — inbox items, schedule nextRun, run phases all go stale. Add one stream:

```
GET /api/events   (SSE)
  event: run      data: {id, agent, phase, usage?}          // on create + phase change
  event: inbox    data: {id, state, agent, tool}            // on create + resolve
  event: schedule data: {name, nextRun, lastRunID}          // on fire
  : keepalive every 15s
```

Include `id:` fields on every event (also fix chat SSE: `api/agents.go:481-513` emits no
`id:`, no keepalive, and the runID only arrives in `done` — emit a `start` event carrying
runID first, so a dropped stream can reconcile via 1.1).

### 1.5 API consistency pass

- **Pagination/filtering**: every CR list passes empty `ListOptions` and returns everything
  (`api/agents.go:50`, `api/schedules.go:27`, …). Messages/sessions hard-code limit 100
  (`api/agents.go:413`). Add `?cursor=&limit=` uniformly.
- **Envelopes**: today CR endpoints speak K8s objects, store endpoints speak plain DTOs.
  Since compat is free: pick plain DTOs everywhere the portal talks to
  (`{items, nextCursor}` lists, `{error:{code,message}}` errors) and keep raw CR shapes only
  on the APIExport (kubectl) surface.
- **PUT-that-is-PATCH**: update handlers are merge-patches under PUT verbs with no
  concurrency control (`api/agents.go:257` etc.). Rename to PATCH; add `If-Match` /
  resourceVersion round-trip so two portal tabs can't silently last-write-win.

### 1.6 Persist tool turns into session history

`executeTask` saves only the user task + final assistant text (`api/run.go:149-174`) — the
next turn cannot see what tools the previous turn ran, which cripples multi-turn agentic use.
Persist tool-call/observation messages (role `tool`) into the transcript, and include them in
`LoadRecentMessages` replay (`api/run.go:213`). Combine with a size guard (see Phase 3.2
compaction) so replay doesn't blow the context window.

---

## Phase 2 — Portal restructure

### 2.0 Rendering foundation: port to Lit

The current model — `render(): string` + full `innerHTML` swap + manual `wire()` re-attachment
(`portal/src/element.ts:142-160`) — is the root cause of an entire class of bugs:

- any async store load wipes half-typed forms (worked around in exactly one place:
  `portal/src/views/agent-wiring.ts:164-170`);
- chat re-renders the whole app **per streamed token** and force-scrolls
  (`portal/src/views/agent-chat.ts:135-137,207-208`);
- no optimistic updates / focus preservation possible;
- three divergent hand-rolled escape helpers (`types.ts:120`, `portalkit/modal.ts:65`,
  `flow.ts:148` — the last one skips `'`).

Port to **Lit** (`lit` npm package, ~5KB): it is custom-element-native so the host contract
(`ProviderFrame.vue` property-set, zero iframe) is untouched, and views are already
`render()`-shaped so porting is mechanical view-by-view. Keyed templates fix form-wipe,
focus loss, and streaming re-render wholesale, and `lit-html` auto-escaping retires the manual
`escapeHTML` discipline. Add `@lit-labs/signals` or keep the existing `AppStore` with a
`host.requestUpdate()` bridge — keep it boring.

Also in this step:
- Type the host context properly: `FarosContext` in `portal/src/types.ts:6-12` is missing
  `subPath`, `orgUUID`, `workspaceUUID` that the host actually passes
  (repo `portal/src/pages/ProviderFrame.vue:162`); tenant currently comes from
  `localStorage['faros:portal:tenant']` (`portal/src/portalkit/tenant.ts`) — make the host
  context the source of truth.
- Kill module-singleton view state (`agent-chat.ts:12-17`, `connections.ts:21-24`,
  `models.ts:66-74`) — move into component state so tenant switches don't need the manual
  reset choreography in `element.ts:102-118`.
- Generate/write TS types for the write API (mutations today are `Record<string,unknown>`
  with hand-typed patch keys scattered across `actions.ts`/`agent-wiring.ts`/`flow-view.ts`).
- Update `portal/README.md` (it still describes a postMessage handshake that doesn't exist).

### 2.1 Navigation: 7 tabs → 4

Current: Agents · Connections · Toolsets · Schedules · Triggers · Models · Inbox (counts in
badges, all stale). Restructure:

| New tab | Absorbs | Rationale |
|---|---|---|
| **Agents** | Agents | unchanged entry point |
| **Activity** | Inbox + (new) Runs | live feed: runs (all agents, filterable by agent/class/phase) with pending approvals pinned on top; badge driven by `/api/events` (1.4). Standalone-stale Inbox dies (`views/inbox.ts` is 41 lines — trivial to absorb) |
| **Connections** | Connections + Toolsets | both are "reusable capability config"; toolsets become a section |
| **Models** | Models | the usage dashboard is good — keep as-is (`views/models.ts`) |

Global schedule/trigger tables die as top-level tabs; schedules/triggers are owned by their
agent (see 2.2). If a cross-agent automation overview is wanted later, it's a filter in
Activity, not a config surface.

### 2.2 One canonical agent editor: config-left / chat-right

Today an agent's model is editable in **three places** (Settings `views/agent-settings.ts`,
Wiring `views/agent-wiring.ts`, Flow model node), channels in two, schedules in three — with
no canonical surface, and stale copy ("Tools & toolsets are wired in the Flow tab",
`agent-settings.ts:40`). The 2026 field-standard pattern is *edit config next to a live
playground* (OpenAI Assistants playground/Agent Builder preview, Copilot Studio test pane,
Dify debug-and-preview, Lindy test runs).

New agent detail = **two panes**:

- **Left — Config** (single scrollable form, absorbs Settings + Wiring):
  sections Persona (displayName, description, systemPrompt), Model (chat + fallbacks;
  purpose-specific models only if/when Phase 3.2 lands), Tools (interactive/background grant
  editor with per-item background opt-in — keep the existing checkbox semantics from
  `agent-wiring.ts`), Channels (multi-row editor + inbound enable/test — keep from
  `agent-wiring.ts`), Schedules, Triggers, Delegates, Budget/Limits. Inline save per section
  (PATCH per 1.5), optimistic with rollback on error.
- **Right — Chat** (the playground; see 2.3). Always live against current config.
- **Third tab: Runs** — this agent's Activity, filtered (see 2.5).

**Flow canvas** (`flow.ts` 976 lines + `flow-view.ts` 776 lines): demote to a **read-only
visualization** tab or delete. Its edit path carries its hardest defects — wires can't be
deleted (hover-highlight only, `flow-view.ts` wire handling), no undo, draft-node validation
via toast (`flow-view.ts:576-579,689`), positions in per-browser localStorage
(`flow.ts:950-975`), zero keyboard/touch access, 66px palette with 9.5px labels
(`style.css:444-451`), zoom widget hard-coded `right:316px` (`style.css:460`). Read-only keeps
the "see the whole wiring at a glance" value at ~20% of the code. Recommendation: read-only.

### 2.3 Chat overhaul (table stakes vs every 2026 product)

Current defects, all in `portal/src/views/agent-chat.ts` (209 lines):

- no markdown — escaped plain text in `pre-wrap` (`agent-chat.ts:182`, `style.css:272`);
- full-app re-render per token + forced scroll (`:135-137`, `:207-208`) — cannot read
  scrollback while streaming;
- tool calls = one collapsed monospace line appearing only when the result lands (`:139`) —
  no pending state, no expand, no duration;
- single-line `<input>` composer; no Stop; no retry; error detection regex-matches message
  text (`:148`); sessions = bare `<select>`, no rename/delete.

Target:

- Markdown rendering (marked/markdown-it + DOMPurify, or lit-friendly equivalent), code
  blocks with copy button.
- Incremental streaming into the last message node only; scroll pinned **only when already
  at bottom**.
- Tool-call cards: pending state on SSE `tool`-start (backend must emit start/end tool
  events — extend `api/agents.go:487-500` accordingly), expandable args/result (full payloads
  once 0.3 lands), duration, per-turn token/cost footer (usage arrives in `done`).
- Multiline composer (textarea, Enter=send, Shift+Enter=newline), Stop button (aborts fetch;
  once 1.3 lands also `POST /api/runs/{id}/cancel`).
- Session management: rename/delete (needs small API additions: `DELETE /api/agents/{name}/sessions/{id}`
  exists only as the `/new` channel command path today — expose it; add rename or drop
  rename and label by first message + date).

### 2.4 Data freshness, errors, empty states

- Subscribe to `/api/events` (1.4); update store slices incrementally. Fallback: 30s poll.
- Store loaders currently swallow errors — every loader except agents catches-and-drops
  (`portal/src/store.ts:66-119`), so a failing API renders as "No schedules yet". Give every
  slice `{data, loading, error}` and render distinct empty/loading/error states.
- Replace the single top note bar (`element.ts:145` — one string for success and failure,
  clickable div not a button) with toasts + inline field errors.
- Run-now buttons: replace the 200-char-toast result (`actions.ts:236,279`) with "queued →
  link to run" once 1.2 lands.

### 2.5 Activity / trace viewer (the LangSmith-class drill-down)

New views on top of 1.1/1.4 — this is the single biggest parity gap with a UI surface
(LangSmith, OpenAI Traces, n8n execution inspector all have it; faros records the data and
shows none of it):

- **Activity list**: paged run table — agent, trigger class icon, input preview, phase chip,
  duration, tokens/USD; filters agent/class/phase/date; pending approvals pinned with
  approve/deny inline (absorbs Inbox).
- **Run detail**: header (agent, trigger, session link, usage, attempt), step timeline —
  each tool call as a card (tool, args, result, duration), delegation children linked via
  `parentRunID`, error detail on failures, "Cancel" while running (1.3), link into the chat
  session transcript where applicable.
- Approvals in run detail and Activity link back to their run (requires 0.7 arg-binding /
  RunID on inbox items).

### 2.6 Agent creation wizard

Creation today is a bare name field; the new agent lands in Chat showing "No model assigned…
open Settings" (`portal/src/views/agents.ts:81` never passes the `modelCredential` param that
`actions.ts:10` already supports). Make creation one modal: name + model credential (required,
from existing creds with a link to Models if none) + optional system-prompt seed + optional
primary channel. Land directly in the config+chat editor.

### 2.7 Deduplication & hygiene (during the port, not after)

- `views/schedules.ts` (164 ln) and `views/triggers.ts` (143 ln) are ~90% identical —
  one parameterized automation component.
- Discord webhook-vs-bot detection ×3 (`connections.ts:39-41`, `agent-wiring.ts:21-23`,
  `flow-view.ts:240-242`); channel-inbound logic duplicated Wiring/Flow; delete-confirm
  duplicated (`agents.ts:74`, `agent-detail.ts:62`); model `<option>` builders duplicated
  (settings/wiring). Centralize.
- `actions.ts` (345 ln) is near-identical try/notify/reload wrappers — replace with one
  `mutate()` helper doing optimistic-update + toast + event-driven refresh.
- Responsive: the only media query targets classes that no longer exist
  (`style.css:298-302`); tables need overflow containers; chat height is hard-coded
  `calc(100vh - 280px)` (`style.css:265`). Do a minimal tablet-width pass.
- Accessibility floor: focusable cards (`agents.ts:57` — click-only `<article>`),
  `aria-label` on icon buttons, `aria-current` on nav, `aria-live` on streaming/toasts,
  fixed modal (0.6).
- Tests: none exist for 5.8k lines. Add vitest + component tests for chat streaming,
  approval flows, and the store; a Playwright smoke against a stubbed API is enough for CI.

---

## Phase 3 — Parity features (backend + UI together)

Ordered by leverage. Field references from the 2026 parity survey are inline.

### 3.1 Durable approval pause/resume — biggest single parity win

Today an approval-gated tool call tells the model "tell the user to approve it and try again"
(`api/toolset.go:304`); after `/approve` the **user must re-prompt**; `inbox.go:44` admits it
("When the tool loop lands, resolving an item resumes the checkpointed run; today it records
the decision"). The machinery is half-built and unused: `store.Run.Checkpoint`
(`store/store.go:91`) is never written, `ClaimRun` (`store/store.go:232`) has zero callers,
phases `PendingApproval`/`Aborted` are never set. The field standard is resume-in-place
(LangGraph `interrupt` + checkpoint resume is the reference implementation; Claude Agent SDK
permission prompts, OpenAI connector approvals, Lindy/Zapier confirm-steps all resume).

Implement: on a gated call, serialize loop state (message history + pending tool call) into
`Checkpoint`, set run phase `PendingApproval`, create the inbox item **with RunID + args
digest**; on approve (portal or `/approve`), `ClaimRun`, rehydrate, execute the approved call
(exact args), continue the loop; on deny, resume with a denial observation. Surfaces: Activity
shows `PendingApproval` runs with resume-on-approve; chat shows an inline approval card;
channel `/approve` resumes and delivers the continuation.

### 3.2 Context management / compaction

History replay is a flat last-40 window (`api/agents.go:30`, `api/run.go:213`); the
`compaction` model purpose exists in the schema (`apis/v1alpha1/types_agent.go:74-77`) and is
never read (only `models["chat"]`, `api/agents.go:534`). Implement summarize-and-truncate on a
token threshold using the compaction model (fallback: chat model), store the summary as a
session artifact, and prepend on replay. Prerequisite for 1.6 not exploding contexts.

### 3.3 Memory that actually feeds the model

`spec.memory` (`types_agent.go:274`) is consumed nowhere; memory is only tool-mediated
(`tools/core.go` memory_save/memory_list) so recall depends on the model remembering to call
the tool. Auto-inject top-N notes into the system context per run (respect `maxNotes`), keep
the tools for writes. (Field: OpenAI/ChatGPT memory, LangGraph long-term memory store, Claude
memory tool + CLAUDE.md, Lindy agent memory.)

### 3.4 Per-tool granularity + cached tool inventory

Grants stop at family/connection — granting `mcp`+connection exposes every discovered tool
(`api/toolset.go:124-147`), and the edges aggregate endpoint is all-or-nothing (and its `edges`
prefix is a misnomer — it federates infrastructure/code/kuery too, `api/agents.go:520-525`).
Add include/exclude tool patterns on `ToolGrant`/`Toolset`; cache each connection's tool list
in the store (refresh on demand + TTL) so (a) the portal can show "what tools does this agent
actually have" in the config editor, (b) runs stop paying the serial re-dial tax
(`toolset.go:129-147`, 60s timeout each, `tools/mcp.go:74`).

### 3.5 Versioning / drafts

CR edits are live immediately. The field has draft→publish everywhere (OpenAI Agent Builder
versioning, Copilot Studio publish/environments, Dify app versions, LangGraph deployment
revisions, Zapier/Lindy draft-vs-live). Minimum viable: keep a `spec` history (store table,
last N revisions + who/when via `identity.user` from `api/http.go:47`), diff view + one-click
rollback in the config editor. Full draft/publish only if demand appears.

### 3.6 Retry & failure policy

`schedule.spec.retry.maxAttempts` and `Run.Attempt` exist with no retry loop — failures only
bump `consecutiveFailures` (disable-at-5). Implement bounded retry with backoff in the
executor for background runs; expose attempt history in run detail.

### 3.7 Onboarding: template gallery

Blank-slate onboarding is a known adoption killer; templates are the primary onboarding for
Lindy/Zapier/Dify/n8n/Copilot Studio. Ship 5–8 curated agent templates (persona + tool grant
+ schedule + channel preset, e.g. "morning brief to Telegram", "GitHub PR watcher",
"infra heartbeat") as static JSON in the provider, surfaced on the Agents empty state and the
create wizard.

### 3.8 Deliberately deferred (recorded so nobody re-litigates)

RAG/knowledge bases (Dify-class), evals/datasets (LangSmith/OpenAI Evals-class; note OpenAI
is sunsetting Agent Builder/Evals after 2026-11-30 — the durable pattern is trace-annotation
→ dataset), claude-code pod runner (`spec.runner`), files workspace (blocked on
infrastructure `agent-workspace` Template per architecture doc), A2A protocol, inbound email,
escalate-to-human handoff. These are real gaps vs the field but below the line for this plan.

---

## Phase 4 — Dead spec surface: wire-or-delete decision table

No compat constraints — delete aggressively; every dead field is a silent no-op lying to
users. Re-run `make codegen-agents-provider` after schema edits (regenerates CRDs +
apiresourceschemas + chart schemas; note the schema copy loop covers
`agents connections schedules triggers runs toolsets`).

| Field | Source | Decision |
|---|---|---|
| `agent.spec.runner` (auto/eino/claude-code) | `types_agent.go:90-97`, never read | **Delete** (re-add with the runner, 3.8) |
| `agent.spec.autonomy` (suggest/ask/auto) | `types_agent.go:99-105`, stored (`api/agents.go:189,318`), never enforced | **Wire** — it's the trust story: `suggest` = all tools require approval, `auto` = none, `ask` = per-grant patterns. Small once 3.1 lands |
| `agent.spec.memory` | `types_agent.go:274`, never read | **Wire** via 3.3 |
| `agent.spec.limits.timeoutSeconds` | `types_agent.go:296`, never read | **Wire** via 1.3 |
| model purposes `background`/`compaction` | `types_agent.go:74-77`, only `chat` read | Keep `compaction` (3.2); wire `background` for heartbeats or delete |
| `schedule.spec.retry.maxAttempts` | never used | **Wire** via 3.6 |
| `trigger.spec.filter` | declared, unenforced (`background.go:572`) | **Wire** via 0.4 |
| trigger sources beyond webhook/github | enum blocks them; `github` is just a webhook | Leave; app-event catalog is 3.8-adjacent |
| `connection.spec.allowedHosts` | no reference outside the type | **Deleted.** Briefly wired as a per-connection SSRF opt-in, then removed: it made users hand-authorize their own configuration. The guard now keys on where the URL came from (model vs config) instead |
| `toolset.status.usedBy`, `connection.status.phase`, `agent.status.*` | never written | Write from the background loop (cheap) or delete; agent budget-suspension status (`types_agent.go:125` promises it) should be **wired** — budget breach currently just errors every run |
| Run CRD | registered (`client/client.go:44`), never created | **Delete the CRD**; runs are store-native and now have an API (1.1). Keeps schema/reality from drifting |
| `defaultNotifyConnection` (deprecated) | `types_agent.go` + `EffectiveChannels` bridge (`types_agent.go:176-233`) | **Delete** field + bridge; migrate existing CRs once (no compat needed) |
| families `files`/`edges` in docs/enum | `types_agent.go:253-254`, `buildToolset` handles only core/web/mcp/github | Remove from docs/enum until real |
| `RunTriggerAPI` constant | never used | Delete or use for 1.2 API-initiated runs |

Also from the tools review, worth folding into Phase 3.4-adjacent work: uniform 12k-char MCP
result clip with no pagination/spillover (`tools/mcp.go:129` reusing `webFetchMaxReturn`,
`tools/web.go:30`); flat-scalar-only builtin param schema (`engine/engine.go:44-50`); no
parallel tool execution; global (not per-tenant) 4-worker pool with drop-on-full Submit
(`executor/executor.go:127-137`); Slack inbound lacks signing-secret verification; inbound
channels accept only the single configured chat with no per-user identity
(`channels_inbound.go:104`); no threading/attachments inbound.

---

## Suggested PR sequencing

1. **PR 1**: 0.1 + 0.2 + 0.3 (+ tests) — security fix + run-linked full audit. Small, urgent.
2. **PR 2**: 1.1 + 1.2 + 1.3 (runs API, async run-now, cancel) + 0.7 error-mapping fixes.
3. **PR 3**: 1.4 (+ chat SSE ids/keepalive/start-event) + 1.5 + 1.6.
4. **PR 4**: 2.0 Lit port of the shell + Agents + Connections/Toolsets + Models (visual
   parity, no new features) + 0.5 + 0.6 + 2.7 hygiene.
5. **PR 5**: 2.1 nav + 2.5 Activity/trace viewer + 2.4 freshness/errors.
6. **PR 6**: 2.2 agent editor (config+chat panes) + 2.3 chat overhaul + 2.6 wizard; Flow →
   read-only.
7. **PR 7**: 3.1 durable approvals. Then 3.2 → 3.7 in order; Phase 4 deletions ride along
   with whichever PR touches each file.

Verification per PR: `make build-agents-provider` (portal typecheck+build + go build),
`go test ./...` in `providers/agents`, `make codegen-agents-provider` clean diff after schema
changes, and the e2e targets (`make e2e-provider`) where applicable. Portal has no test
harness yet — PR 4 should introduce vitest.

---

## External references (parity claims)

- OpenAI AgentKit / Agent Builder (and its announced wind-down after 2026-11-30):
  https://openai.com/index/introducing-agentkit/ ,
  https://mcp.directory/blog/openai-agentkit-deprecation-2026
- LangGraph interrupt/checkpoint/time-travel (reference for 3.1): langchain-ai.github.io/langgraph
  (human-in-the-loop + persistence concepts docs)
- LangSmith trace/eval UX (reference for 2.5): docs.smith.langchain.com
- Claude Agent SDK permission prompts / memory tool / subagents: docs.anthropic.com
- Dify (RAG + debug-and-preview), n8n (execution inspector), Copilot Studio (publish/test
  pane), Zapier Agents (preview mode), Lindy (templates/draft replies): vendor docs; survey
  summaries in the review thread, e.g. https://zapier.com/blog/lindy-vs-zapier/ ,
  https://www.gumloop.com/blog/lindy-ai-alternatives
- Internal: `docs/agents-provider-architecture.md`, `docs/agents-provider-research.md`,
  `docs/agents-multi-channel.md`; recent PRs #478 (multi-channel), #479 (background tool
  opt-in), #464 (models/cost overhaul), #463/#467/#427 (portal rework/Flow), #431/#429/#428
  (tools-as-connections/Toolset CRD).
