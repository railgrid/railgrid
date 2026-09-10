---
{"schema":1,"id":"design.ai.app-studio-conversation","title":"App Studio conversation and modes","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"App Studio ships durable project threads, explicit response and approval modes, bounded context selections, and an explicit plan-to-implementation handoff, presented through the shared conversation chrome."},"appliesTo":["app-studio","assistant","provider-portals"],"owner":"app-studio","canonicalSource":[{"path":"docs/design/ai/app-studio-conversation.md#app-studio-conversation-and-modes","role":"design"},{"path":"providers/app-studio/api/assistant_mode_prompt.go","role":"implementation"},{"path":"providers/app-studio/api/assistant_threads.go","role":"implementation"},{"path":"providers/app-studio/portal/src/App.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/AssistantActionLog.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/ResponseModePicker.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/ApprovalModePicker.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/assistantThreadProjection.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/conversationResilience.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationIdentity.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceBackLink.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchTab.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/threadRailIntegration.test.mjs","role":"reference"},{"path":"provider-sdk/agentkit-vue/AIMessage.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActivityDisclosure.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActionRow.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIComposer.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPrimaryAction.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIInterrupt.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationLayout.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationRail.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITranscript.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"},{"path":"providers/app-studio/portal/src/responseModePicker.test.mjs","role":"reference"},{"path":"providers/app-studio/portal/src/approvalModePicker.test.mjs","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-design-docs","status":"passing","evidence":"The design knowledge-base metadata, IDs, sources, and links validate after final evidence updates."},{"kind":"test","ref":"providers/app-studio/portal/src/responseModePicker.test.mjs","status":"passing","evidence":"App Studio focused portal tests passed."},{"kind":"test","ref":"providers/app-studio/portal/src/approvalModePicker.test.mjs","status":"passing","evidence":"App Studio focused portal tests passed."},{"kind":"test","ref":"App Studio thread integration and Vite/typecheck","status":"passing","evidence":"App Studio thread integration tests passed; the Vite production bundle and budget gate passed after the measured CSS v14 shared-header ceiling amendment (1,220,000 raw / 346,000 gzip; actual 1,218,262 raw / 345,034 gzip)."},{"kind":"test","ref":"App Studio full runner","status":"passing","evidence":"App Studio full runner passed 26/26 tests after workbench tab extraction; source assertions follow AIWorkbenchTab ARIA and slot behavior and the canonical 240px maximum width."},{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"PortalKit parity passed for 3/3 canonical and distributed asset sets."},{"kind":"command","ref":"make verify-ui-conformance","status":"passing","evidence":"UI conformance covered 404 files with zero violations and 28 tests passed."},{"kind":"browser","ref":"Prior App Studio rendered verification","status":"passing","evidence":"Prior evidence covered the earlier seven-component surface and host fallback; it does not verify current shared chrome convergence or CSS version 10."},{"kind":"browser","ref":"Current App Studio rendered verification (CSS/runtime v14)","status":"passing","evidence":"Current actual App Studio production bundle fixtures passed at the 1129px target with a simulated 216px host sidebar and at mobile 390x844, in light and dark themes (8 cases across Agents and App Studio). The header used the UI14 host stylesheet (index-CpQ6oqXU.css), 56px chrome, a 32px identity tile, 13px title, 11px context, centered controls, no horizontal overflow, and provider list navigation. App Studio rename emitted PATCH and the mocked server-backed display updated. Evidence: /tmp/header-alignment/shared-header-studio-final-repair.log and /tmp/header-alignment/shared-header-studio-final-mobile.log. Parent visually reviewed four screenshots. No live backend, Tilt, or actual-host browser claim is made."},{"kind":"browser","ref":"Current App Studio AIWorkbenchTab rendered verification (CSS/runtime v14)","status":"passing","evidence":"Current built App Studio fixture passed at the 1129px target with a simulated 216px host sidebar and at mobile 390x844 in light and dark themes (8/8 cases across Agents and App Studio). The shared UI14 tab frame measured a 32px outer wrapper and 30px control with a 14px icon at 1.75 stroke; state colors matched and there was no horizontal overflow. Studio keyboard Preview activation, reorder, and close passed with no browser or request errors. Logs: /tmp/header-alignment/shared-tabs-agents-final.log and /tmp/header-alignment/shared-tabs-studio-final.log. Screenshot: *-tab-app-studio-target-light.png; parent reviewed it. Mock backend and simulated host; no live Tilt claim is made."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"see-also"},{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also"},{"id":"design.ai.evidence-and-status","relation":"see-also"},{"id":"design.components.ai-conversation","relation":"see-also"}]}
---

# App Studio conversation and modes

App Studio is the gold-standard conversation reference in this workspace. Its
assistant is a durable project conversation, not an opaque chat box. The thread
projection keeps user messages and assistant message segments
as the public conversation. Progress, action activity, plans, approvals, and
errors belong to the assistant segment that owns them so a reload or reconnect
does not invent a second answer or lose the run state.

The presentation uses the shared `AIConversationLayout`, `AIConversationRail`,
`AITranscript`, `AIMessage`,
`AIActivityDisclosure`, `AIActionRow`, `AIComposer`, `AIPrimaryAction`,
`AIInterrupt`, and `AIConversationIdentity` components. `.k-ai-prose` supplies
the shared Markdown display recipe after App Studio's own content handling, and
the titlebar uses the shared `.k-ai-conversation-header` recipe. These shared
surfaces provide presentation and disclosure semantics; App Studio retains
thread projection, mode and approval authority, context and attachment
receipts, queueing, reconnect, and mutation acknowledgements.

The 56px titlebar places the thread side-panel control, an icon-only
`ResourceBackLink` named **Back to projects**, a shared divider, the App Studio
icon, and `AIConversationIdentity`. The back link uses `ctx.basePath` (falling
back to `/ui/providers/app-studio`) as its real `href` and sends active in-app
navigation through `navigate('')`. The identity's title slot retains the
provider-owned editable thread title; its context slot shows the project and
Git repository or the Git connection prompt. Rename and Git behavior remain
App Studio-owned.

App Studio uses `AIInterrupt` for both approval and follow-up frames. The
shared interrupt supports both kinds, while other providers may consume only
the kind their own API and lifecycle expose.

## Shipped contract

### Mode is authority

The response mode is explicit and fixed for a run:

- **Default** answers, inspects, or makes a requested change. The user's
  request determines whether the turn is inspection-only or may act.
- **Plan** investigates with bounded evidence and produces a decision-complete
  plan. It is read-only: it does not edit files, hydrate templates, restart or
  rebuild runtimes, provision infrastructure, commit, or imply implementation.
- **Review** is a separately scoped read-only execution over the current
  workspace and repository. It reports prioritized correctness, security,
  regression, durability, and missing-test findings; review text cannot grant
  mutation authority.

The server fixes the mode for the run. User wording or model output cannot
change it. The client may offer **Implement the plan** only for a completed,
error-free Plan run; selecting it explicitly switches the next turn to
Default and sends the implementation request. A plan card is not evidence that
implementation happened.

### Approval is a separate policy

Approval mode controls effectful actions independently of response mode:

- **Ask when needed** runs routine workspace, build, test, and lint actions and
  asks before consequential external effects.
- **Always ask** asks before actions that change state or invoke external
  operations.
- **Never allow** keeps the assistant read-only and rejects actions requiring
  approval.

When approval is requested, show the action disclosure and its current
permission state. If command details are unavailable or invalid, disable
**Allow** and require denial/retry; never fill in missing arguments from
memory or prose.

### Context is bounded and receipt-backed

User turns may select a bounded set of skill IDs, provider resource references,
annotations, and attachment receipts. The server validates those selections,
records the selected metadata with the turn, and retains attachment bytes in
the attachment store. Skill bodies and attachment/text contents are not part
of the public thread projection merely because a selection chip exists. Invalid
or stale references are dropped or rejected rather than rendered as trustworthy
context.

Thread and turn reads/writes are scoped to the authenticated user, organization,
workspace, and project. A reconnect must reconcile the server-owned durable
projection with the live stream by stable turn/message identity and revision;
it must not let an older response overwrite a newer segment.

## Retrieval guidance

For a conversation change, read this document together with
[resource reads](../patterns/resource-reads.md), the relevant component
contract, and [evidence and status](evidence-and-status.md). Keep user-facing
copy concrete about the authority granted, the action waiting, or the evidence
observed. Treat status, progress, and action activity as different disclosures;
do not collapse them into a generic “working” or “done” label.

## Explicit exclusions and gaps

These are not shipped generic App Studio design contracts:

- There is no generic confidence or uncertainty vocabulary. Use concrete
  states, sources, and missing evidence; do not invent a confidence score or
  badge.
- There is no generic assistant undo or reversal contract. Workspace history
  restore is a separate, explicit operation and must not be presented as an
  automatic undo for arbitrary assistant actions.
- There is no general generated-artifact provenance contract. Exact commit and
  release evidence is documented in [evidence and status](evidence-and-status.md),
  but it does not establish lineage for every model-generated output.
- Current raw run-error presentation is an implementation observation under
  privacy review, not normative content guidance. Do not copy an unsanitized
  backend error into a new conversation, approval, or notification surface.
