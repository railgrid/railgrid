---
{"schema":1,"id":"design.ai.app-studio-conversation","title":"App Studio conversation and modes","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"App Studio ships durable project threads, explicit response and approval modes, bounded context selections, and an explicit plan-to-implementation handoff, presented through the shared conversation chrome. Workspace file writes run alongside assistant activity under exact-version fences, with stale paths shown for explicit retry."},"appliesTo":["app-studio","assistant","provider-portals"],"owner":"app-studio","canonicalSource":[{"path":"docs/design/ai/app-studio-conversation.md#app-studio-conversation-and-modes","role":"design"},{"path":"providers/app-studio/api/assistant_mode_prompt.go","role":"implementation"},{"path":"providers/app-studio/api/assistant_threads.go","role":"implementation"},{"path":"providers/app-studio/portal/src/App.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/AssistantActionLog.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/ApprovalModePicker.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/assistantThreadProjection.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/conversationResilience.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationIdentity.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceBackLink.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchTab.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/threadRailIntegration.test.mjs","role":"reference"},{"path":"provider-sdk/agentkit-vue/AIMessage.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActivityDisclosure.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActionRow.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIComposer.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPrimaryAction.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIInterrupt.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationLayout.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationRail.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITranscript.vue","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"},{"path":"providers/app-studio/portal/src/approvalModePicker.test.mjs","role":"reference"},{"path":"providers/app-studio/portal/src/CodeExplorer.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/CodeExplorer.test.mjs","role":"reference"},{"path":"provider-sdk/modelcatalog/reasoning.go","role":"implementation"},{"path":"providers/app-studio/portal/src/ReasoningEffortControl.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/reasoning.test.mjs","role":"reference"},{"path":"providers/app-studio/api/assistant_reasoning_test.go","role":"reference"},{"path":"provider-sdk/agentkit-vue/AIModelReasoningPicker.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/threadReasoning.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/AssistantRichComposer.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-design-docs","status":"passing","evidence":"The design knowledge-base metadata, IDs, sources, and links validate after final evidence updates."},{"kind":"test","ref":"providers/app-studio/portal/src/approvalModePicker.test.mjs","status":"passing","evidence":"App Studio focused portal tests passed."},{"kind":"test","ref":"App Studio thread integration and Vite/typecheck","status":"passing","evidence":"App Studio thread integration tests passed; the Vite production bundle and budget gate passed after the measured CSS v14 shared-header ceiling amendment (1,220,000 raw / 346,000 gzip; actual 1,218,262 raw / 345,034 gzip)."},{"kind":"test","ref":"App Studio full runner","status":"passing","evidence":"App Studio full runner passed 26/26 tests after workbench tab extraction; source assertions follow AIWorkbenchTab ARIA and slot behavior and the canonical 240px maximum width."},{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"PortalKit parity passed for 3/3 canonical and distributed asset sets."},{"kind":"command","ref":"make verify-ui-conformance","status":"passing","evidence":"UI conformance covered 404 files with zero violations and 28 tests passed."},{"kind":"browser","ref":"Prior App Studio rendered verification","status":"passing","evidence":"Prior evidence covered the earlier seven-component surface and host fallback; it does not verify current shared chrome convergence or CSS version 10."},{"kind":"browser","ref":"Current App Studio rendered verification (CSS/runtime v14)","status":"passing","evidence":"Current actual App Studio production bundle fixtures passed at the 1129px target with a simulated 216px host sidebar and at mobile 390x844, in light and dark themes (8 cases across Agents and App Studio). The header used the UI14 host stylesheet (index-CpQ6oqXU.css), 56px chrome, a 32px identity tile, 13px title, 11px context, centered controls, no horizontal overflow, and provider list navigation. App Studio rename emitted PATCH and the mocked server-backed display updated. Evidence: /tmp/header-alignment/shared-header-studio-final-repair.log and /tmp/header-alignment/shared-header-studio-final-mobile.log. Parent visually reviewed four screenshots. No live backend, Tilt, or actual-host browser claim is made."},{"kind":"browser","ref":"Current App Studio AIWorkbenchTab rendered verification (CSS/runtime v14)","status":"passing","evidence":"Current built App Studio fixture passed at the 1129px target with a simulated 216px host sidebar and at mobile 390x844 in light and dark themes (8/8 cases across Agents and App Studio). The shared UI14 tab frame measured a 32px outer wrapper and 30px control with a 14px icon at 1.75 stroke; state colors matched and there was no horizontal overflow. Studio keyboard Preview activation, reorder, and close passed with no browser or request errors. Logs: /tmp/header-alignment/shared-tabs-agents-final.log and /tmp/header-alignment/shared-tabs-studio-final.log. Screenshot: *-tab-app-studio-target-light.png; parent reviewed it. Mock backend and simulated host; no live Tilt claim is made."},{"kind":"test","ref":"App Studio source version-conflict tests","status":"passing","evidence":"CodeExplorer and project-file API tests passed, including delete-version gating, one-shot stale conflict reporting, mixed create/replace uploads, and changedFiles parsing."},{"kind":"browser","ref":"App Studio source version-conflict rendered fixture","status":"passing","evidence":"The rendered fixture passed at 1280px and 390px in light and dark themes: delete stayed disabled until the file version loaded, a stale delete sent the captured version once, changed paths were visible, and no browser errors occurred."},{"kind":"test","ref":"App Studio reasoning capabilities and persistence","status":"passing","evidence":"The catalog, complete App Studio API and controller suites, focused store suite, and actual isolated Postgres legacy-migration and preference roundtrip test passed. Reasoning snapshots cover request presence, precedence, replay, frozen transport, and legacy automatic behavior."},{"kind":"test","ref":"App Studio reasoning portal validation","status":"passing","evidence":"The complete portal npm test runner, TypeScript checks, and production bundle build passed. Reasoning persistence regressions cover per-model drafts, sibling-model preferences, missing-thread hydration, stale callbacks, and PATCH/event ordering. The inspected bundle is 328,296 bytes gzip for the page path and 360,062 bytes gzip in total, within the amended feature budget."},{"kind":"browser","ref":"Prior App Studio reasoning dropdown browser validation","status":"passing","evidence":"Actual isolated Tilt hub and App Studio backend passed Chromium checks at 1440x1000 and 390x844 in light and dark themes for landing/project composers and saved-model settings. Verified keyboard open/Escape/focus, per-model choices, custom endpoint hiding, server preference persistence, reload restoration, atomic reset preserving sibling models, invalid-selection reset, populated-composer send blocking, and connection-test save gating. No browser page errors or horizontal document overflow. Placeholder credentials were used without upstream model calls. Screenshots: artifacts/screenshots/app-studio-reasoning-{landing,project,settings}-{desktop,narrow}-{light,dark}.png; execution logs: /tmp/app-studio-reasoning-browser-matrix.log and /tmp/app-studio-reasoning-settings-browser.log."},{"kind":"command","ref":"Combined model and reasoning picker portal validation","status":"passing","evidence":"Full App Studio npm test and vue-tsc --noEmit passed, including mounted slider commit, per-model restoration, keyboard navigation, focus, saving and recovery regressions. Final production build and budget check passed: page 1,213,197 B raw / 330,285 B gzip; total 1,320,976 B raw / 364,568 B gzip. PortalKit parity, provider contract and UI conformance gates passed."},{"kind":"browser","ref":"Combined model and reasoning picker live browser validation","status":"passing","evidence":"2026-10-17: Actual isolated Tilt hub and App Studio backend passed Chromium checks. Landing verified model-default inheritance, per-model restoration, unsupported endpoint gating, nested Escape and focus, internal Tab order, and keyboard model selection. Project chat verified pointer preview with exactly one preference write on release, server persistence and reload restoration, and light/dark popup bounds at 1440x1000 and 390x844 with no horizontal document overflow or page errors. Placeholder credentials were used without upstream model calls. Screenshots: artifacts/screenshots/app-studio-picker-effort-light.png, app-studio-picker-models-dark.png, and app-studio-picker-narrow-dark.png. Execution log: /tmp/app-studio-picker-browser-check.log."},{"kind":"command","ref":"App Studio reasoning code-review verification","status":"passing","evidence":"2026-10-17: make test-app-studio-reasoning passed the catalog, full API/store/Studio-controller suites, portal typecheck and complete portal runner. Pinned fix-lint and lint targets passed after review fixes. The malformed preference PATCH regression passed after the final presence-validation change. Full store tests also passed against task-owned Postgres with unique schemas, including the deterministic turn/preference event-ordering regression. Regenerating 200 App Studio schema/catalog artifacts produced identical hashes. Boilerplate, provider contract, PortalKit parity, design-doc and UI-conformance gates passed; UI conformance scanned 463 files with zero violations. Full repository make verify was not run."},{"kind":"browser","ref":"App Studio reasoning coarse-pointer review verification","status":"passing","evidence":"2026-10-17: Actual Tilt hub and App Studio backend passed Chromium checks at 390x844 with mobile/coarse-pointer emulation in light and dark themes. Reset, model and back targets measured at least 44x44 pixels; touch navigation and popup bounds passed without horizontal document overflow or page errors. Placeholder model credentials were used without upstream calls. Screenshots: artifacts/screenshots/app-studio-picker-review-touch-light.png and artifacts/screenshots/app-studio-picker-review-touch-dark.png. Execution log: /tmp/app-studio-reasoning-review-browser-touch.log."},{"kind":"command","ref":"App Studio reasoning second-review verification","status":"passing","evidence":"2026-10-17: The catalog and complete API/store/Studio-controller legs of make test-app-studio-reasoning passed. After replacing Object.hasOwn for the configured JavaScript target, portal typecheck and make test-app-studio-portal passed. Regression coverage verifies an already-recorded legacy thread migration on isolated Postgres, repeated startup and reopen, invalid saved-default repair with stored or replacement credentials, and scoped first-thread preference snapshots and retry copies. Pinned formatting/lint and the final portal/provider build passed; the Go build used GOFLAGS=-buildvcs=false after a managed-worktree VCS-stamping error. Final bundle: page 1,214,179 B raw / 330,615 B gzip; total 1,321,958 B raw / 364,901 B gzip, within unchanged budgets. All 200 generated artifacts still match the previously verified hashes. PortalKit, provider-contract, UI-conformance and boilerplate gates passed. No markup or style changes were made in this pass; browser evidence remains the earlier live checks. Full repository make verify was not run."},{"kind":"command","ref":"App Studio reasoning third-review verification","status":"passing","evidence":"2026-10-17: Independent runtime and persistence reviews found no new confirmed defects; their focused catalog/API/store/controller tests passed. The reasoning wire-payload regression also passed with a local test server. The canonical picker now skips its disabled Reset control on opening during a pending save. Its mounted regression reproduces native disabled-control focus behavior, keyboard model selection and absence of an extra effort write; focused picker/persistence/reasoning/resilience/settings tests and portal typecheck passed. PortalKit parity, provider contract and UI conformance passed. The actual Tilt portal/provider build passed with GOFLAGS=-buildvcs=false; bundle page 1,214,190 B raw / 330,623 B gzip, total 1,321,969 B raw / 364,905 B gzip, within unchanged budgets. All 200 generated schema/catalog artifacts still match the previously verified hashes. Full repository make verify was not run."},{"kind":"browser","ref":"App Studio reasoning third-review live keyboard recheck","status":"not-run","evidence":"2026-10-17: Attempted actual Tilt validation of opening the picker during a pending real preference save. App Studio and its production bundle became ready, but make dev-browser-ready timed out while Agents initialization failed with test timeouts and worker startup errors despite a bounded retry. The new browser script never reached login or interaction, so this round has no new live browser pass or screenshot. Earlier live browser evidence remains historical. Logs: /tmp/app-studio-reasoning-third-review-tilt.log and /tmp/app-studio-reasoning-third-review-ready.log. Task-owned stack cleanup uses make dev-browser-down."},{"kind":"browser","ref":"App Studio compact composer on shared Tilt","status":"passing","evidence":"2026-10-17: Live existing Hi App project verified in Chromium at 390px, 560px and 760px composer widths in light and dark themes. Response-mode trigger absent; permissions icon-only below 560px; toolbar has no overflow or overlap; permission and model menus open and Escape dismisses. Screenshots: artifacts/screenshots/app-studio-composer-390-dark.png and app-studio-composer-760-light.png. Managed dev-browser-ready declined the shared legacy stack; direct live verification used the existing stack and preserved it as requested."},{"kind":"browser","ref":"Polished model/effort popup and removable draft modes","status":"passing","evidence":"2026-10-17: Verified on the existing live Tilt project in Chromium at 1440px and 390px, light and dark. Popup stays in bounds; no lightning icon; model list navigation and nested Escape work. /plan and /review each show their label, survive reload, and exit to Default without clearing draft text while restoring editor focus. Evidence: artifacts/screenshots/composer-plan.png, composer-review.png. Focused tests: 50 passed. Shared design, PortalKit and UI conformance gates passed. Existing shared Tilt stack retained as requested."},{"kind":"browser","ref":"Quiet effort saving and compact approval menu","status":"passing","evidence":"2026-10-17: Live shared Tilt verified at 1440px and 390px in light and dark. Real preference responses delayed 1s: no indicator at 150ms, indicator after 500ms, unchanged popup x/y/width/height and composer height before/during/after saves including Model default transitions. Eight real saves completed and original values restored. Permission menu measured 320px wide with no overflow; Home/End/Escape and focus restoration passed. Twelve focused tests and design/PortalKit/UI-conformance gates passed. Evidence: artifacts/screenshots/permissions-light.png, permissions-dark.png, quiet-save-light.png, quiet-save-dark.png. Shared stack retained."},{"kind":"browser","ref":"Compact composer code-review touch verification","status":"passing","evidence":"2026-10-17: The live shared Tilt app passed at 320px and 390px in both themes with coarse-pointer and reduced-motion emulation. A long model name exposed a shrinking permissions control group; reserving that group fixes the overlap. Verified no overflow or overlapping controls, 44px permission targets, bounded menus, Home/End navigation and Escape focus restoration. Log: /tmp/composer-review-touch-final.log. Shared stack retained."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"see-also"},{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also"},{"id":"design.ai.evidence-and-status","relation":"see-also"},{"id":"design.components.ai-conversation","relation":"see-also"}]}
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

### Narrow conversation composer

Below a composer width of 560px, the project chat keeps one toolbar row:
attachment and icon-only permission controls on the left, an inline
model and reasoning summary on the right, and reserved space for Send/Stop.
The breakpoint follows the pane rather than the viewport. The permission button
retains its accessible name and title; model labels truncate instead of wrapping.
Its menu is 320px wide, clamped to the viewport, with wrapping descriptions and
the shared menu item, focus, and touch-target recipes.
The chat input has no response-mode picker; slash commands retain mode selection.
Selecting `/plan` or `/review` shows a labeled icon above the draft with an
accessible remove button. Removing it returns to Default mode and focuses the
editor, without clearing the draft. The label follows the persisted draft mode.
Wide composers retain their labeled controls and stacked model summary.

### Reasoning belongs to the selected model

The model settings form provides a **Reasoning effort** default for verified
OpenAI model connections. Both composers use AgentKit's combined
`AIModelReasoningPicker`: one compact trigger opens an effort card with a
stepped slider, reset action, and a model button that opens the model list
inside the same popup. The popup uses a left-aligned effort heading, a labeled
model row, and a 320px maximum width for the slider labels. No lightning icon
is shown: the control represents model and reasoning effort, not speed.
Successful saves do not add a footer or move the popup. Saves lasting over 500ms
show a muted indicator in reserved header space (or the closed trigger's chevron
slot), with accessible status text. Normal effort-description space is reserved,
and the popup anchors to the trigger's right edge. Errors retain visible recovery
feedback; save locking and persistence remain caller-owned. The card uses Railgrid's surface, accent, typography,
and focus tokens. **Model default** inherits the saved model's current default;
explicit efforts are remembered per conversation and saved model. A model
switch never transfers an effort from a different model. The UI offers only
the capability profile's supported choices; `none` is an explicit choice,
not another name for inheriting a default.

Unsupported connections omit the reasoning controls. If an existing selection
becomes invalid after a model edit, retain an actionable error and reset control
until the user selects a supported value or **Model default**. Preference saves
must settle before sending. The submitted choice is captured with the pending
message, and the server freezes the effective setting for the run. Resuming or
steering that run does not apply a later preference change.

Before a conversation exists, choices stay scoped to the current workspace and
project draft. Creating its first thread captures all supported per-model choices,
including models selected earlier in the draft. Pending first submissions retain
their own copy for retries. Removed models and unsupported inactive choices are
excluded when creating the thread; the selected model still requires correction
before sending if its choice becomes invalid.

The first slider stop and reset action select **Model default**; every other
stop is an explicit supported effort. Dragging previews a value, and committing
the gesture saves it. The model list identifies the workspace default without
implying automatic model routing. Unsupported connections keep the model picker
but omit the reasoning slider. Escape returns from the model list to the effort
card, then closes the card and restores focus to its trigger.
Opening the picker while a preference save is pending skips the disabled reset
control and focuses the model button, keeping keyboard model selection available.
On coarse pointers, the reset, model, and back controls have targets of at least
44×44px, with header columns that accommodate those targets.

This is provider-owned behavior composed through AgentKit's shared picker and
PortalKit's anchored-popup geometry and menu vocabulary. Reasoning effort controls model execution;
it does not add reasoning text to the transcript.

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

### Workspace file changes are version-fenced

The Code Explorer remains available while an assistant run is streaming;
assistant activity is not a blanket lock on project source files. New paths
are create-only. Replacing or deleting an existing file requires the exact
version read from the workspace. The Explorer loads that version before
offering deletion, captures it before confirmation, and reports a stale
conflict with the affected paths when another source change wins. It never
retries a stale replacement or deletion with a newer version behind the
user's back. Refreshing and confirming again is the user's choice.

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
