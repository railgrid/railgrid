---
{"schema":1,"id":"design.ai.evidence-and-status","title":"AI evidence and status truthfulness","kind":"policy","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"App Studio ships bounded progress/action disclosure, pause-aware worked duration, preview evidence states, and exact-commit release/promotion states; generic confidence and artifact provenance remain unshipped."},"appliesTo":["app-studio"],"owner":"design-system","canonicalSource":[{"path":"docs/design/ai/evidence-and-status.md#ai-evidence-and-status-truthfulness","role":"design"},{"path":"providers/app-studio/portal/src/assistantActionFeed.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/assistantTrace.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/assistantProgress.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/previewState.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/promotionState.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/ReleasePipeline.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/previewState.test.mjs","role":"reference"},{"path":"providers/app-studio/portal/src/promotionState.test.mjs","role":"reference"},{"path":"providers/app-studio/portal/src/assistantActionFeed.test.mjs","role":"reference"},{"path":"providers/app-studio/portal/src/assistantProgress.test.mjs","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-design-docs","status":"passing","evidence":"The design knowledge-base metadata, IDs, sources, and links validate."},{"kind":"test","ref":"providers/app-studio/portal/src/previewState.test.mjs","status":"not-run","evidence":"Portal dependencies are not installed in this worktree."},{"kind":"test","ref":"providers/app-studio/portal/src/promotionState.test.mjs","status":"not-run","evidence":"Portal dependencies are not installed in this worktree."},{"kind":"test","ref":"providers/app-studio/portal/src/assistantActionFeed.test.mjs","status":"not-run","evidence":"Portal dependencies are not installed in this worktree."},{"kind":"test","ref":"providers/app-studio/portal/src/assistantProgress.test.mjs","status":"not-run","evidence":"Portal dependencies are not installed in this worktree."}]},"relatedDocuments":[{"id":"design.ai.app-studio-conversation","relation":"see-also"},{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also"},{"id":"design.patterns.resource-reads","relation":"extends"},{"id":"design.quality.review-checklist","relation":"see-also"}]}
---

# AI evidence and status truthfulness

App Studio assistant, preview, and release surfaces must say what the system observed, what is still running, and what
remains unverified. A route being reachable, a process being healthy, or a
workflow reporting success is evidence for that narrow boundary; it is not a
license to claim rendered behavior, interaction success, deployment, or access.

## App Studio shipped contract

### Progress and action evidence stay distinct

App Studio assistant progress is user-facing commentary. The action feed is a bounded,
server-owned activity record. The feed accepts an allowlisted vocabulary of
action kinds and statuses, including running, waiting, succeeded, skipped,
failed, rejected, canceled, retrying, and recovered. Plan items are not rendered
as action rows. Opaque successful `other` events stay hidden, while waiting and
failure states remain visible so a silent failure cannot look like success.

Only adjacent, ordinary successful file reads or edits may be grouped. Keep
commits, clarifications, diagnostics, execution disclosures, milestones, and
all non-success states separate. The trace orders progress and action items by
their server sequence; it must not reorder evidence into a more reassuring
story.

### Worked time respects pauses

The App Studio server snapshot is authoritative. A client may advance a running display
between snapshots, but it must freeze the estimate during approval/input pauses
and use the terminal snapshot for the final value. Reload or reconnect may not
double-count elapsed time or make a terminal run appear to have continued.

### Preview states name the evidence boundary

App Studio development preview labels have deliberately narrow meanings:

- **Pending** or **Starting** means there is not yet a usable preview URL.
- **Loading** means a URL exists but the current document has not connected.
- **Loaded** means the current document handshake connected.
- **Loaded unverified** means the frame is rendered but the document/evidence
  bridge is unavailable (or the preview is deliberately tokenless); it must not
  be called fully verified.
- **Error** means authorization/recovery failed, not merely that an advisory
  console or annotation bridge is unavailable after a rendered frame loaded.

URL reachability and runtime readiness do not prove rendered content, data flow,
accessibility, or interaction behavior. Use a native browser receipt or the
supported preview inspection path for those claims; do not invent an assertion
from arbitrary page text, console output, or a successful navigation.

### Release and production states are separate gates

The App Studio release pipeline presents commit, build, verify, deploy, and access as
separate steps. CI success is explanatory evidence until exact-commit component
images are observed. A workflow run explains the selected release only when its
reported head SHA matches the reviewed commit; stale or unpinned runs remain
inconclusive. Partial artifacts, delayed registry observations, unavailable
status, and terminal failures retain distinct states.

App Studio production is not **ready** merely because a rollout was requested or a
provider says `Ready`. The requested rollout revision must be observed, and the
deployed image values must match the selected release. External access is live
only when the publication is ready and a URL exists. Keep an old production
release visibly online while a newer release is still building or converging.

## Copy and implementation rules

Use the smallest truthful verb and include the missing boundary where it helps:
“Preview loaded; document verification is unavailable,” “Build succeeded;
verifying release images,” “Waiting for the requested rollout,” or “Production
is running. Resolving external access…” are materially different outcomes.
Never turn a stale, missing, partial, or unavailable observation into a green
terminal badge. Preserve the distinction between commit, CI, artifact, deploy,
access, route, runtime, rendered, and interaction evidence in summaries and
assistive text.

## Explicit exclusions and gaps

- App Studio has no shipped generic confidence or uncertainty vocabulary. Do not add
  confidence percentages, model-certainty badges, or vague “AI verified” copy
  where concrete evidence states are available.
- App Studio has no generic undo or reversal contract. Cancellation and denial stop or
  prevent an eligible action; they do not reverse an effect that already ran.
- App Studio has no general generated-artifact provenance contract. Exact commit/image
  evidence gates this release pipeline, but the product does not yet provide a
  universal lineage record from an assistant output through generated files to
  a deployed artifact.
- Raw error strings in current App Studio views are implementation
  observations under privacy/sanitization review. They are not normative copy;
  new surfaces should use bounded, structured error categories and safe next
  steps rather than blindly echoing backend messages.
