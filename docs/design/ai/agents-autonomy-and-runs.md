---
{"schema":1,"id":"design.ai.agents-autonomy-and-runs","title":"Agents autonomy and run transparency","kind":"journey","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The Agents provider ships autonomy policy controls, separate interactive/background grants, and a live run detail with approval, step, source, failure, and child-run evidence."},"appliesTo":["agents","provider-portals","assistant"],"owner":"agents","canonicalSource":[{"path":"docs/design/ai/agents-autonomy-and-runs.md#agents-autonomy-and-run-transparency","role":"design"},{"path":"providers/agents/portal/src/views/AgentConfig.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/RunDetail.vue","role":"implementation"},{"path":"providers/agents/portal/src/types.ts","role":"implementation"},{"path":"providers/agents/portal/src/test/config.test.ts","role":"reference"},{"path":"providers/agents/portal/src/test/activity.test.ts","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-design-docs","status":"passing","evidence":"The design knowledge-base metadata, IDs, sources, and links validate."},{"kind":"test","ref":"providers/agents/portal/src/test/config.test.ts","status":"passing","evidence":"make build-agents-provider-portal passed all 350 portal tests, typecheck, and Vite build on 2026-09-09."},{"kind":"test","ref":"providers/agents/portal/src/test/activity.test.ts","status":"passing","evidence":"make build-agents-provider-portal passed all 350 portal tests, typecheck, and Vite build on 2026-09-09."},{"kind":"browser","ref":"Agents Activity and run-detail rendered verification","status":"passing","evidence":"2026-09-09: production bundle with fixture API data in Chromium, 1440px and 390px, html.dark and html.light; 12 screenshots, approval interaction, child ResourceTable, and no page overflow. Evidence: /var/tmp/codex-build/agents-activity-proof/verification.json. No live-backend verification."}]},"relatedDocuments":[{"id":"design.ai.app-studio-conversation","relation":"see-also"},{"id":"design.ai.evidence-and-status","relation":"see-also"},{"id":"design.patterns.navigation-and-feedback","relation":"see-also"}]}
---

# Agents autonomy and run transparency

Agents lets a person configure an agent that may act interactively or in the
background. The design contract makes authority visible at configuration time
and makes each run inspectable after it starts. A run state is not inferred
from a spinner alone.

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
