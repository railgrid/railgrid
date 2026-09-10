---
{"schema":1,"id":"design.ai.agents-autonomy-and-runs","title":"Agents autonomy and run transparency","kind":"journey","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The Agents provider ships autonomy policy controls, separate interactive/background grants, a persistent chat surface with retained Config, Tools & toolsets, Schedules & triggers, and Runs workbench panels, and run evidence for approvals, steps, sources, failures, and child runs."},"appliesTo":["agents","provider-portals","assistant"],"owner":"agents","canonicalSource":[{"path":"docs/design/ai/agents-autonomy-and-runs.md#agents-autonomy-and-run-transparency","role":"design"},{"path":"providers/agents/portal/src/App.vue","role":"implementation"},{"path":"providers/agents/portal/src/router.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentDetail.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentChat.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/ChatMessage.vue","role":"implementation"},{"path":"providers/agents/api/run.go","role":"implementation"},{"path":"providers/agents/api/run_progress.go","role":"implementation"},{"path":"provider-sdk/agentkit-vue/conversation.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITimestamp.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentConfig.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/Activity.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/RunDetail.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentWorkbench.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/AgentWorkbenchHeader.vue","role":"implementation"},{"path":"providers/agents/portal/src/types.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationHeader.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationIdentity.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceBackLink.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationLayout.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkspace.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchTab.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIMessage.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActivityDisclosure.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActionRow.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIInterrupt.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITranscript.vue","role":"implementation"},{"path":"providers/agents/portal/src/test/agent-detail.test.ts","role":"reference"},{"path":"providers/agents/portal/src/test/element-router.test.ts","role":"reference"},{"path":"providers/agents/portal/src/test/activity.test.ts","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-design-docs","status":"passing","evidence":"Current design knowledge-base validation passed."},{"kind":"test","ref":"Agents detail/config/authority tests","status":"passing","evidence":"50 focused Agents detail/config tests and 7 authority tests passed; Agents portal typecheck passed."},{"kind":"test","ref":"Agents API and engine projection tests","status":"passing","evidence":"Final API and engine suites passed, including tool-limit, checkpoint, canceled-tool, cancellation, and approval regressions."},{"kind":"test","ref":"Agents portal final tests and build","status":"passing","evidence":"399 tests across 29 files, typecheck, and Vite build passed after the shared formatter extraction."},{"kind":"test","ref":"App Studio final tests and build","status":"passing","evidence":"The 67-file portal suite, typecheck, two final formatter/projection focused suites, Vite build, and budgets passed; formatter output remained unchanged."},{"kind":"browser","ref":"Built mocked Agents and App Studio fixture matrix","status":"passing","evidence":"Final built bundles passed desktop and mobile light/dark fixtures: the Agents six-case rail/titlebar matrix (932px with a 216px host sidebar, 1551px desktop, and 390px mobile), Agents live/draft/reload, and the final App Studio timestamp/disclosure check. Rail/titlebar geometry, collapse/reopen, mobile open/Escape, and overflow checks passed; see /tmp/adoption-rail-header-browser.log. This is mocked fixture evidence separate from served runtime and Tilt; it does not establish a live backend model call."},{"kind":"browser","ref":"Served Agents and App Studio runtime with Tilt health","status":"passing","evidence":"Both Tilt health endpoints were healthy, and served Agents main.js plus App Studio page-element bytes exactly matched the final builds. Evidence: /tmp/adoption-final-runtime-evidence.json. This served-runtime check did not make a model call."},{"kind":"test","ref":"Agents production build","status":"passing","evidence":"Final Agents portal typecheck and Vite build passed with 399 tests across 29 files; no live model call is implied."},{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Current PortalKit parity passed for the canonical and distributed asset sets."},{"kind":"command","ref":"make verify-ui-conformance","status":"passing","evidence":"Final UI conformance covered 420 files with zero violations; shared sync and design-doc gates also passed."},{"kind":"browser","ref":"Historical Agents rendered verification (CSS v10/v11)","status":"passing","evidence":"Earlier fixture evidence covered pre-convergence Agents chat and run surfaces, including v10/v11-era shared CSS behavior at desktop/mobile sizes and light/dark themes. It is historical and does not verify the current AIWorkspace or CSS/runtime version 13."},{"kind":"browser","ref":"Historical Agents header alignment rendered verification (CSS/runtime v14)","status":"passing","evidence":"Historical actual Agents production bundle fixtures passed at the 1129px target with a simulated 216px host sidebar and at mobile 390x844, in light and dark themes (8 cases across Agents and App Studio). The header used the UI14 host stylesheet (index-CpQ6oqXU.css), 56px chrome, a 32px identity tile, 13px title, 11px context, centered controls, no horizontal overflow, and provider list navigation. Evidence: /tmp/header-alignment/shared-header-agents-final.log, /tmp/header-alignment/shared-header-studio-final-repair.log, /tmp/header-alignment/shared-header-studio-final-mobile.log. Parent visually reviewed four screenshots. No live backend, Tilt, or actual-host browser claim is made."},{"kind":"browser","ref":"Historical Agents AIWorkbenchTab rendered verification (CSS/runtime v14)","status":"passing","evidence":"Historical built Agents fixture passed at the 1129px target with a simulated 216px host sidebar and at mobile 390x844 in light and dark themes (8/8 cases across Agents and App Studio). The shared UI14 tab frame measured a 32px outer wrapper and 30px control with a 14px icon at 1.75 stroke; state colors matched and there was no horizontal overflow. Agents keyboard Config to Runs passed with no browser or request errors. Logs: /tmp/header-alignment/shared-tabs-agents-final.log and /tmp/header-alignment/shared-tabs-studio-final.log. Screenshot: *-tab-agents-target-light.png; parent reviewed it. Mock backend and simulated host; no live Tilt claim is made."}]},"relatedDocuments":[{"id":"design.ai.app-studio-conversation","relation":"see-also"},{"id":"design.ai.evidence-and-status","relation":"see-also"},{"id":"design.components.ai-conversation","relation":"see-also"},{"id":"design.patterns.navigation-and-feedback","relation":"see-also"}]}
---

# Agents autonomy and run transparency

Agents lets a person configure an agent that may act interactively or in the
background. The design contract makes authority visible at configuration time
and makes each run inspectable after it starts. A run state is not inferred
from a spinner alone.

Chat is the primary interactive surface and remains mounted while Config,
Tools & toolsets, Schedules & triggers, and Runs open as collapsible workbench
panels beside it. The panels mount lazily
when first visited and retain their state while the agent instance remains
open. Config remains the authority for persona, model, autonomy, budget, limits,
and channels. Tools & toolsets owns tool grants; Schedules & triggers owns
automation controls. Runs remains the inspection surface for individual runs.
Session lifecycle currently exposes create, select, and delete. Shared
conversation presentation may be reused for the transcript and activity
details, but Agents retains its provider-owned message projection, approval
validation, run lifecycle, and channel behavior.

Agent Chat places the shared 56px `AIConversationHeader` above
`AIConversationLayout`, so the titlebar spans the conversation rail and
transcript. The rail's `Threads` header starts immediately below the titlebar.
Rail collapse and reopen remain available on desktop; mobile rail open and
Escape-to-close behavior remain available. Agent Chat uses the shared
conversation layout, rail, transcript, and `AIConversationIdentity`; Run Detail uses the shared transcript,
message, activity, action, and interrupt surfaces. Agents keeps transcript
scroll and focus roots, approval authority, run polling, and provider
navigation in its own views. The agent detail enables `ResourcePage`'s `fill`
layout for Chat and all workbench tabs. When an agent exists and the Agents store has
a loaded snapshot, it sets `showHeader` to `false` and supplies the agent
identity and actions through AgentChat's leading, heading, and actions slots.
The leading slot places an icon-only `ResourceBackLink` after the rail toggles;
it is named **Back to agents**, points to the `#/agents` list, and is disabled
while deletion is busy. The heading slot uses `AIConversationIdentity` with the
active thread as the primary title and the 13px agent `h1` as secondary context
on two lines; the actions slot carries the status badge and PanelRight
workbench toggle. The identity and actions therefore
share the existing conversation header; there is no separate full-width
resource toolbar or 18px gap. The workbench tabs align with that
conversation header, with
no separate Workbench title or subtitle. When the agent is loading, initially
failing, or not found without a loaded snapshot, `ResourcePage` retains its own
header. Once a loaded snapshot exists, including a stale refresh state, the
AgentChat header remains while ResourcePage's stale/error notice remains
authoritative. Chat's conversation controls remain inside the conversation.

Deletion is supplied by `AgentDetail` through AgentConfig's final
`destructive-actions` slot rather than the conversation header. The existing
confirmation, authority, busy, error, success, and navigation handling remains
provider-owned; this contract does not claim a live delete verification.

`AgentWorkbenchHeader` uses `AIWorkbenchTabs` for closeable, draggable tabs and
its New tab action. `AIWorkbenchLauncher` offers search, existing tabs, and
suggested tools. Config, Tools & toolsets, Schedules & triggers, and Runs are
singleton tool tabs: selecting an open tool focuses it rather than duplicating
its form. Closing the active tab selects a neighbor; closing the last tab opens
the launcher. Closed panes stay mounted so reopening retains unsaved inputs.
Tab order and the open set are retained while the agent workspace is mounted.
Agents owns its tab state and supports drag reordering plus Alt+Shift+Left/Right
on a focused tab; the shared components own presentation and event forwarding.

On desktop, an initial `/agents/:name/chat` opens the retained workbench on
Config, including its form, with a New tab launcher available. At widths of 999px or less, Chat starts with the
workbench closed unless an explicit workbench route requests a panel.
Explicit hide remains authoritative for Chat, tab selection restores the last
selected pane, and ResourcePage read states remain visible.

Host sidebar navigation back to the provider root restores the Agents list.
The host republishes context for hash-only Vue Router navigation; Agents reads
the committed URL without rewriting host history. Native back/forward and
standalone hash navigation remain supported.

The agent-scoped route `/agents/:name/runs/:runID` embeds Run Detail inside the
Runs workbench. Its nested resource surface uses an `h2` Run heading and a
back link to that agent's Runs tab. The standalone `/activity` and
`/activity/:runID` routes retain the top-level Activity and run-detail
surfaces.

Agents uses the shared execution-details frame for tool arguments, output,
status, and duration. Readable activity labels retain exact tool identifiers in
the disclosure; generic results do not imply shell execution metadata. The
composer reserves a bottom action row for send/stop, matching App Studio, while
keyboard guidance remains in its accessible description and editor tooltip.

## Shipped contract

### Autonomy names the interruption policy

The configuration surface explains the consequence of each autonomy mode:

- **Suggest** waits for approval for every tool call. It is the safest and the
  most interruptive setting.
- **Ask** waits only for tools matched by a grant's approval patterns.
- **Auto** runs granted tools without asking and is intended only for tools the
  operator trusts unattended.

Autonomy is enforced server-side on every run. A blocked action is represented
as **PendingApproval** in Activity and can be resolved from the run detail;
the UI must not imply that a pending action already ran.

### Background authority is narrower

Interactive chat and background schedules, triggers, or heartbeats have
separate tool grants. Linking a tool or toolset for chat does not silently grant
it to background work. A background checkbox is an explicit opt-in and is
disabled until the interactive grant exists; removing the interactive grant
also removes its background grant.

Background runs have no human watching them, so the background surface should
be deliberately smaller. Explain the distinction beside the grant controls,
and preserve it in any summary or review surface. Built-in capabilities and
fan-out are also grants: a worker cannot be described as having web access or
research fan-out unless the corresponding family/connection is enabled for that
run class.

### A run is a trace, not a terminal label

Run detail exposes the agent, trigger, run class, session, start time, duration,
usage, and parent relationship. While a run is Pending, Running, or
PendingApproval, the detail refreshes and its elapsed time moves; a settled run
stops live polling. **Cancel** is available only while live.

An approval pause shows the tool and disclosed arguments with **Approve &
resume** and **Deny**. A missing or malformed disclosure is a reason to stop
and report, not a reason to reconstruct or guess the operation.

Activity retains PortalKit's queryable `ResourceTable` for filters, cursor
pagination, read states, and keyboard row navigation. The primary cell groups
the input preview with the agent; phase, trigger/class, duration, usage, and
creation time remain separate scan columns. Pending inbox actions retain their
argument disclosure and approval/denial controls above the table.

Run detail groups agent, trigger/class, elapsed duration, and usage in a compact
summary. Approval and failure states precede the input/output and expandable
tool trace. Session, start time, attempt, and parent navigation occupy a
secondary details column that stacks below the trace in narrow containers.
Child runs continue to use the shared simple `ResourceTable`. Output uses
document styling, and sources remain available alongside partial failed output.

Run conversation details are collapsible and preserve approvals. A
`PendingApproval` child remains in flight until it is resolved or the run
settles. An unknown step outcome is not success; preserve the provider's
state and expose enough evidence for the operator to distinguish waiting,
running, failed, canceled, and completed work.

The trace then makes the following evidence available when present:

- output and attributed source links;
- ordered tool steps with outcome, duration, arguments, result, and error;
- failed-run error plus any partial output that was produced before failure;
- child runs linked by `parentRunID`, distinguishing spawned workers from
  delegated runs;
- separate counts for running, queued, awaiting-approval, completed, and
  failed child runs, with an updating hint while work remains in flight.

Empty fan-out is meaningful: if spawning was granted but no child exists, say
that the agent answered directly or that a spawn attempt produced no worker,
rather than leaving an ambiguous empty panel.

### Server-owned transcript and timing

Persisted Agents messages receive their `createdAt` timestamp at the server
persistence boundary. The chat stream also exposes the server-owned run start
timestamp. The projection keeps turn phases explicit: a complete model response
that leads to tool calls is `commentary`, each tool callback is a `tool` row, and
the last complete model response is `final`. A failed model stream remains a
terminal marker with its partial output; an incomplete or otherwise unclassified
response is not promoted to a final answer. Missing or unclassified evidence
therefore remains visible for inspection.

Run worked duration accumulates the measured model-response and tool-callback
durations. The active clock begins when the durable `Running` record is written;
queue/setup time, approval waits, and recovery waits are excluded. A recovery
checkpoint carries the accumulated worked duration so a resumed run continues
from observed work without counting the idle pause.

Agents maps the resulting `durationMS` through the shared
`formatAIWorkedDuration` helper and passes the rounded label to `AITurnProgress`.
The shared `AITimestamp` presents each valid server-created message timestamp as
semantic time with relative and full values. AgentKit owns this presentation;
Agents owns the server projection, lifecycle phase, and timing evidence.

The rendered fixture evidence for this contract came from built bundles with
mocked APIs at desktop and mobile sizes in light and dark themes, including the
Agents live, draft, and reload cases. The final Agents six-case rail/titlebar
matrix covered the 932px target with a 216px host sidebar, 1551px desktop, and
390px mobile; titlebar and rail geometry, collapse/reopen, mobile open/Escape,
and overflow checks passed (`/tmp/adoption-rail-header-browser.log`). This is
separate from the real served runtime checks: both Tilt health endpoints were healthy, and the served Agents
`main.js` and App Studio page-element bytes exactly matched the final builds
(evidence: `/tmp/adoption-final-runtime-evidence.json`). These checks do not
include a live backend model call. Agents API and engine suites passed, including
the added tool-limit, checkpoint, and canceled-tool regressions plus focused
cancellation and approval coverage. The final Agents portal result was 399 tests
across 29 files with typecheck and Vite build passing; App Studio's 67-file suite,
typecheck, two final formatter/projection focused suites, Vite build, and budgets also
passed. Shared sync, design-doc, and UI-conformance gates passed, with 420 files
and zero conformance violations.

## Retrieval guidance

When designing an Agents configuration or activity surface, pair this contract
with [evidence and status](evidence-and-status.md). Preserve the distinction
between interactive and background authority, between a pending approval and a
completed step, and between a failed run and its partial output. Use the exact
run class and phase vocabulary already carried by the API.

## Explicit exclusions and gaps

- Agents has no generic confidence or uncertainty vocabulary. Concrete phase,
  outcome, source, and error evidence is the available contract.
- There is no generic undo or reversal affordance for arbitrary tool calls. A
  cancellation request, denial, or failed run is not a reversal of effects that
  already occurred.
- Sources on a run are useful attribution, but Agents has no generic
  generated-artifact provenance contract. Do not claim that a source list proves
  lineage from model output to a generated or deployed artifact.
- The current run-detail error alert renders a run message as an implementation
  behavior under sanitization review. It is not a recipe for exposing raw
  backend errors in new surfaces.
