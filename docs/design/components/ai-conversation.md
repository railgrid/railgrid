---
{"schema":1,"id":"design.components.ai-conversation","title":"Shared AI conversation components","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Optional AgentKit owns shared AI conversation and model presentation. Canonical Vue sources live in provider-sdk/agentkit-vue and styles in provider-sdk/agentkit. Only App Studio and Agents receive AgentKit copies; provider behavior remains provider-owned."},"appliesTo":["portalkit","portal","provider-portals","assistant"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/ai-conversation.md#shared-ai-conversation-components","role":"design"},{"path":"provider-sdk/agentkit-vue/AIActionRow.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActivityDisclosure.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIComposer.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationHeader.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationIdentity.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceBackLink.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationLayout.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationRail.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIInterrupt.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIMessage.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITimestamp.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/timestamp.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPrimaryAction.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITranscript.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkspace.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchPane.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchTab.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ai.ts","role":"implementation"},{"path":"provider-sdk/agentkit/agent-ui.css","role":"implementation"},{"path":"hack/sync-portalkit.sh","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationTurn.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITurnProgress.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPlanDisclosure.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPlanSteps.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActivityFeed.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/conversation.ts","role":"implementation"},{"path":"provider-sdk/agentkit/activity.ts","role":"implementation"},{"path":"provider-sdk/agentkit/conversation.css","role":"implementation"},{"path":"provider-sdk/agentkit/activity.css","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIExecutionDetails.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPaneDivider.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchTabs.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchLauncher.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationComposer.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AICommandPalette.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AISkillCatalogList.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIAttachmentReceipts.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AgentKitAttachmentPreview.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AgentKitAttachmentTextPreview.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/composer.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/composerAttachments.ts","role":"implementation"},{"path":"hack/test-agentkit-input-geometry.mjs","role":"reference"},{"path":"provider-sdk/agentkit-vue/AIConversationRenameDialog.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITranscriptActivity.vue","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Final AgentKit sync parity passed; shared sync, design-doc, and UI-conformance gates also passed, covering 429 files with zero violations across 31 focused tests."},{"kind":"command","ref":"final Agents and App Studio portal gates","status":"passing","evidence":"Agents final result: 399 tests across 29 files, typecheck, and Vite build passed. App Studio final result: 67-file suite, typecheck, formatter/projection focused suites, Vite build, and budgets passed; formatter output remained unchanged."},{"kind":"browser","ref":"AgentKit final conversation fixture matrix","status":"passing","evidence":"Final Agents and App Studio built bundles passed desktop and mobile light/dark fixtures, including the Agents six-case rail/titlebar matrix (932px with a 216px host sidebar, 1551px desktop, and 390px mobile) and the final App Studio timestamp/disclosure check. Rail/titlebar geometry, collapse/reopen, mobile open/Escape, and overflow checks passed; see /tmp/adoption-rail-header-browser.log. This is mocked rendering evidence; it is separate from served runtime and Tilt, and does not establish a live backend model call."},{"kind":"browser","ref":"AgentKit standalone run fixture matrix","status":"passing","evidence":"2026-09-09: standalone Agents run passed 1551x900 and 390x844 in light/dark (4 cases): progress, step expansion, run inspector and no horizontal overflow/runtime errors. The evidence panel is width-constrained so its child-run table scrolls internally. /tmp/agentkit-final-run-browser.log and /tmp/agentkit-rendered/run-final-*.png. Built bundle with mocked APIs; no live approval/backend action."},{"kind":"browser","ref":"Shared AIPaneDivider verification","status":"passing","evidence":"2026-09-09: both built providers passed desktop/mobile light/dark fixtures (8 cases). Desktop separator measures 6px, zero margins, 16px grip, 1.75 stroke with identical theme colors; pointer and keyboard resizing work, divider hidden on mobile. Parent reviewed screenshots. /tmp/divider-{agents,app-studio}-browser.log and /tmp/divider-rendered/. Mocked APIs, not live backend interaction. Final Agents and App Studio portal builds and typechecks passed; shared sync, design-doc, and UI-conformance gates passed for 429 files with zero violations across 31 focused tests."},{"kind":"browser","ref":"Served Agents and App Studio runtime with Tilt health","status":"passing","evidence":"Both Tilt health endpoints were healthy, and served Agents main.js plus App Studio page-element bytes exactly matched the final builds. Evidence: /tmp/adoption-final-runtime-evidence.json. This served-runtime check did not make a model call."},{"kind":"browser","ref":"2026-10-05 actual Tilt coarse-pointer confirmation","status":"passing","evidence":"With real host Light/Dark selection at390px, PortalKit31/AgentKit8 loaded, workbench frames measured144x46px and inner tab controls44x44px; keyboard ArrowRight moved focus and selection. Dark Delete chat icon contrast was4.76:1 against its composited row. No page overflow or browser errors. Evidence: .kcp/agents-design-conformance/access-pass1/agents-render-confirm.json."},{"kind":"command","ref":"2026-11-05 composer parity design gates","status":"passing","evidence":"make verify-design-docs, verify-portalkit, verify-ui-conformance, verify-agentkit, verify-provider-contract and verify-boilerplate passed. UI conformance scanned 523 authored files with zero violations; Vite dependency caches are excluded with regression coverage. Impeccable source detector reported no findings."},{"kind":"test","ref":"2026-11-05 composer parity review validation","status":"passing","evidence":"Agents portal: 1,061 tests across 62 files, typecheck and production build passed. App Studio full portal suite, typecheck and production build passed. Total bundle 1,403,842 B raw /388,421 B gzip; a measured total-only ceiling amendment retains shared canonical CSS. Full Agents Go suite passed against disposable Postgres, including deletion/migration checkpoint fences; full runner suite passed, including dispatch-scoped batch staging. Pinned root and Agents formatting/lint passed."},{"kind":"browser","ref":"2026-11-05 composer parity live Tilt review","status":"passing","evidence":"Actual existing Tilt hub and Agents backend passed Chromium checks at 1440x1000 and 390x844 in html.light and html.dark. Verified inline skill semantics, readable mobile headings, 288px/128px editors, slash palette keyboard selection/Escape/focus return, bounded popup, draft image/text/PDF upload/removal and opaque-file copy. Coarse palette options and image remove controls were at least44x44px; sampled heading-description contrast was6.81:1 light and5.61:1 dark. No horizontal document overflow, page errors or model calls. Screenshots: artifacts/screenshots/agents-review-{skills,composer}-{desktop,mobile}-{light,dark}.png. This is a bounded interaction/rendered check, not a screen-reader audit or an upstream model test."},{"kind":"test","ref":"2026-11-05 input overlap follow-up","status":"passing","evidence":"Agents portal full suite passed 1,062 tests across62 files; App Studio full portal suite and typecheck passed. Both production builds passed. App Studio total1,404,850 B raw /388,569 B gzip fits its existing ceilings; no budget increase. Shared parity, AgentKit and UI conformance gates passed."},{"kind":"browser","ref":"2026-11-05 live cross-portal input geometry regression","status":"passing","evidence":"make test-agentkit-input-geometry passed against the existing Tilt hub with temporary Agent and Project resources at1440x1000,900x900 and390x844 in light/dark Chromium. Checked30 search instances across activation, libraries and palettes, icon/text separation and centering, filled-search clear spacing/focus,12 Add menu bounds and dismissal, single composer focus frame, action clipping inside Config and readable mobile project title/context. All six matrix cases passed with no page errors or model calls. Source: hack/test-agentkit-input-geometry.mjs. Current screenshots: artifacts/screenshots/agents-config-skills-{desktop,mobile}-{light,dark}.png, agents-input-controls-{desktop-light,mobile-dark}.png and studio-input-controls-{desktop-light,mobile-dark}.png. Prior review screenshots are retained locally under .kcp/ui-disparities/previous-screenshots/. This is a bounded rendered/interaction check, not a screen-reader or cross-browser audit."},{"kind":"test","ref":"2026-11-05 attachment recovery validation","status":"passing","evidence":"Agents final full suite: 1,066 tests across63 files, typecheck and production build passed, including4 mounted attachment recovery tests. App Studio full suite/typecheck passed; final composer, attachment and deletion checks passed after canonical-class cleanup. Both final builds fit existing bundle ceilings: App Studio total1,404,779 B raw /388,637 B gzip. Shared parity, AgentKit, design-doc and UI-conformance checks passed."},{"kind":"browser","ref":"2026-11-05 shared attachment recovery live Tilt matrix","status":"passing","evidence":"Expanded hack/test-agentkit-input-geometry.mjs assertions passed in both portals at1440x1000,900x900 and390x844 in light/dark, using real existing Tilt services and browser-injected429 upload failures. Verified24 visible image/text error reasons with accessible group associations,12 image action/status geometry checks and24 keyboard removals restoring composer focus. Coarse image retry/remove targets44px in104px thumbnails. No page errors or model calls. Evidence: .kcp/ui-review-pass2/fix-geometry.json and fix-browser.log; final phone captures agents-fixed-recovery.png and studio-fixed-recovery.png. Entry bytes were checked against the rebuilt bundles. Temporary Agent and empty Project are removed after verification. This is bounded Chromium interaction evidence, not a screen-reader or upstream-model test."},{"kind":"browser","ref":"2026-11-05 attachment focus code-review follow-up","status":"passing","evidence":"Two new mounted tests reproduced a second-render-tick focus steal and keyboard Retry focus loss before the fix. All6 recovery tests pass after a shared guard checks ownership, scope and focus immediately before focusing the editor. Shared parity, AgentKit, UI-conformance and design-doc checks pass; provider builds and App Studio focused composer/attachment/deletion tests and typecheck pass. App Studio final total1,404,944 B raw /388,666 B gzip fits unchanged budgets. Existing Tilt services served matching rebuilt entrypoints. Expanded six-case live matrix passed in both portals at1440x1000,900x900 and390x844 in light/dark, including24 keyboard retries reaching the mocked upload transport and24 keyboard removals, without page errors or model calls. Evidence: .kcp/ui-review-pass2/review-geometry.json and review-browser.log. Temporary fixtures are removed after validation."},{"kind":"browser","ref":"2026-11-05 full-branch UI and interaction review","status":"passing","evidence":"Seven new mounted regression cases reproduced stale palettes/menus after owner or draft changes, IME Enter selecting options, native picker results crossing drafts, multi-file uploads continuing into a new draft, queued picker-focus callbacks surviving a scope switch, and divergent Agents multi-term search. All now pass. Full Agents suite1075 tests across64 files, typecheck and production build passed; full App Studio suite, typecheck and production build passed. App Studio final total1,404,801 B raw /388,635 B gzip fits unchanged ceilings. Shared parity, AgentKit, UI conformance, design-doc gates and detector pass. Both Tilt providers passed expanded1440x1000,900x900,390x844 light/dark checks: multi-term library search, Agents create-form/resource controls, Studio detail Escape/focus/bounds, IME confirmation, keyboard skill-chip selection/removal, and existing input/attachment recovery checks. Batched desktop-light/mobile-dark form and dialog screenshots were inspected. No page errors or model calls. Evidence: .kcp/ui-review-pass2/branch-browser.log, branch-geometry.json and branch-{agent-form,studio-detail}-{1440,390}.png. Temporary Agent and empty Project are removed after verification. Chromium-only bounded interaction evidence; no upstream model or assistive-technology audit."},{"kind":"browser","ref":"2026-11-05 palette multi-root and narrow-pane review","status":"passing","evidence":"Five new mounted regressions reproduced duplicate palette/option and image-error IDs across separate Vue roots, keyboard entry on an already-added skill, missing unmatched-command feedback, and ambiguous chip action labels. Updated disabled-hover regression passes after the phone matrix exposed a pointer-enter override. Full Agents suite1080 tests across65 files and App Studio full suite passed; final focused palette/composer tests, typechecks, provider builds, sync, AgentKit, UI-conformance and design-doc gates pass. App Studio final total1,404,776 B raw /388,673 B gzip fits unchanged budgets. Final expanded live Tilt matrix passed both portals at1440x1000,900x900,390x844 in light/dark, including empty command status/no stale active ID, first available keyboard option, active-option ownership, explicit Remove chip labels and44px coarse chip targets, plus prior search/dialog/IME/scope/attachment checks. A visual check also found clipped starter prompts in a narrow desktop pane; a pane-aware grid and full labels now pass clipping checks in every case, with the corrected desktop screenshot inspected. No page errors or model calls. Evidence: .kcp/ui-review-pass2/final-review-browser.log, final-review-geometry.json, final-review-regressions.log and artifacts/screenshots/studio-input-controls-desktop-light.png. Cross-root ownership is mounted-test evidence; this is bounded Chromium validation, not an assistive-technology audit. Temporary Agent and empty Project are removed after validation."},{"kind":"browser","ref":"2026-11-05 palette dismissal and async lifecycle review","status":"passing","evidence":"Added8 mounted palette lifecycle cases covering focus-leave dismissal from the filter and external slash editor, the native body focus gap before Back receives focus, Escape while Back owns focus, internal view focus transfers, queued-open unmount cleanup, atomic selection/dismissal, resource-lookup rejection/retry and late failures. The three focused AgentKit files pass19 tests. Full Agents portal build/test gate and App Studio full suite pass; final focused Studio accessibility/palette/composer tests, typecheck, provider builds, shared parity, AgentKit, design-doc and UI-conformance gates pass. Option-class consolidation retains fixed bundle ceilings: App Studio total1,404,856 B raw /388,843 B gzip. Both served provider entrypoints were checked against fresh builds. Final six-case existing-Tilt matrix passed both portals at1440x1000,900x900 and390x844 in light/dark, including Shift+Tab to Back, Escape returning to commands, Tab dismissing from editor/filter without focus theft, subsequent keyboard skill selection, and all prior search/IME/chip/attachment/layout checks. No page errors or model calls. Discovery failures and queued-unmount behavior are mounted-test evidence. Evidence: .kcp/ui-review-pass2/round-lifecycle.log, lifecycle-browser.log, lifecycle-geometry.json and lifecycle-studio-focused.log. Temporary Agent and empty Project are removed after verification. Bounded Chromium validation; not a screen-reader audit."}]},"relatedDocuments":[{"id":"design.ai.app-studio-conversation","relation":"see-also"},{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also"},{"id":"design.ai.evidence-and-status","relation":"see-also"},{"id":"design.components.portalkit-assets","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also","path":"docs/design/foundations/recipes.md"}]}
---

# Shared AI conversation components

## Purpose

AgentKit's AI conversation set supplies neutral conversation chrome, transcript,
activity, composer, interrupt, action, conversation-list, identity, and workspace
presentation. It keeps the visual and interaction frame consistent while leaving
identity content, transport,
authorization, projection, and provider-specific content with the caller.

## Use when

Use these components for a Vue portal that presents an assistant conversation,
an action trace attached to a turn, an approval or clarification pause, a
persistent message composer, or a searchable conversation list. Use the
neutral view types in `ai.ts` when the provider can map its durable records to
the shared presentation without losing state or evidence.

## Avoid when

Do not use the shared components as an orchestration, API, authentication,
Markdown, attachment storage, queue, or cancellation layer. Providers own API and
tenant access, Markdown sanitization, message and run lifecycle, reconnect and
projection, attachment receipts, queued follow-ups, and stop/cancel
acknowledgements. A slot or shared status value does not grant a provider any
of those capabilities. Do not infer success from an unknown action or from a
missing provider state.

## Anatomy and variants

The foundational AgentKit conversation components are:

| Component | Responsibility and caller-owned surface |
| --- | --- |
| `AIMessage` | Neutral `user` or `assistant` transcript geometry; `before`, default, and `after` slots; optional user `bubble`. |
| `AITimestamp` | Provider-owned timestamp presentation: a semantic `<time datetime>` with a relative label and full locale value on hover, focus, or click; missing or invalid values are omitted. |
| `AIActivityDisclosure` | Compact count and summary trigger with busy, attention, error, and expanded states; ordered activity arrives through the default slot. |
| `AITranscriptActivity` | One action renders directly; multiple contiguous calls share a semantic disclosure and stay in chronological order. Long lists expose Show all activities. |
| `AIActionRow` | One ordered action with explicit status, target, outcome, busy/attention/error/canceled flags, and optional details disclosure. The details slot holds provider-safe execution data. |
| `AIComposer` | Persistent composer frame with disabled and queued variants; editor, context, attachments, queue, and provider controls are slots. |
| `AIConversationComposer` | Rich text editor, removable skill/resource chips, contextual slash commands, file picker, paste/drop, draft previews and retry/removal UI. Providers inject discovery and attachment transport and retain receipt ownership and the accepted-send boundary. |
| `AICommandPalette` | Contextual slash-command, enabled-skill and resource selection, with anchored desktop placement and a bounded mobile panel. It is separate from a global application command palette. |
| `AISkillCatalogList` | Searchable skill summaries and selection; the caller owns detail presentation, creation, editing, activation and resource writes. |
| `AgentKitAttachmentPreview`, `AgentKitAttachmentTextPreview` | Local image/text draft previews and truthful metadata-only treatment for opaque files. The caller owns upload authorization and storage. |
| `AIAttachmentReceipts` | Accepted attachment receipt display and abortable image previews through a caller-supplied authorized loader; it never discovers files or constructs provider URLs. |
| `AIConversationHeader` | Compact titlebar frame; title, navigation, loading, and provider controls remain in the default slot. |
| `AIConversationIdentity` | Compact identity with caller-owned `icon`, `title`, and optional `context` slots; the shared tile is 32px, with 13px primary title and 11px secondary context. |
| `AIConversationLayout` | Rail/main presentation frame; child layout, scroll roots, focus refs, and provider navigation remain caller-owned. |
| `AIPrimaryAction` | Caller-controlled `send`, `stop`, or `stopping` button state. It emits a click; it does not submit or cancel. |
| `AIInterrupt` | Approval or follow-up frame with pending, busy, resolved, or failed status, optional invalid state, error slot, and action slot. |
| `AIConversationRail` | Searchable, collapsible, resizable conversation list with mobile overlay behavior, focus management, and capability-gated create/pin/unread/archive/delete actions. |
| `AITranscript` | Centered readable transcript column; the caller owns its scroll root, focus behavior, and message lifecycle. |
| `AIPaneDivider` | Shared 6px pane separator with a 16px grip, 1.75 stroke, hover/focus color and reduced-motion support; callers own pointer/keyboard resize logic. |
| `AIWorkspace` | Resizable conversation/workbench split with minimum pane widths and a narrow single-pane transition; callers own route state and both pane contents. |
| `AIWorkbenchPane` | Shared workbench canvas with a plain document heading, optional description and actions, or a tool variant for editor and preview content. Callers own pane content, controls, and resource behavior. |
| `AIWorkbenchTab` | Shared workbench tab wrapper and control with ARIA wiring and forwarded click, keyboard, and drag events. Optional `leading`, `after-label`, and `trailing` slots support lifecycle chrome; callers own collections, selection, and keyboard behavior. |
| `AIWorkbenchTabs` | Shared tab strip with non-shrinking tab widths, conditional previous/next scroll controls, and selected-tab visibility. It forwards caller selection, keyboard, drag, close, and launcher events without moving focus during scrolling. |

Composed presentation extends those primitives:

| Component | Responsibility and caller-owned surface |
| --- | --- |
| `AIConversationTurn` | Orders provider-owned progress, trace, output, interrupt, and metadata slots within the neutral message frame. |
| `AITurnProgress` | Presents Working/Worked/Stopping state, optional provider-formatted duration, interrupted state, and a controlled run-details disclosure. It inspects the details slot at render time, so empty details have no chevron and later trace updates can appear. It never starts a clock. |
| `AIPlanDisclosure`, `AIPlanSteps` | Display an already-validated plan with completion counts, status icons, accessible disclosure, and mobile geometry. Provider admission remains authoritative. |
| `AIActivityFeed` | Composes ordered rows and optional named groups with keyed expansion and bounded scrolling. Providers supply summaries, groups, status, and safe details. Empty-label groups render as flat activity. |
| `AIExecutionDetails` | Renders provider-sanitized command, arguments, output, status, timing, and safe detail links for approval and activity variants; parsing and authorization remain with the provider. |

AgentKit is opt-in: only Agents and App Studio receive its generated copies.
General UI primitives and tenant helpers remain in PortalKit. See the
[AgentKit source guide](../../../provider-sdk/agentkit/README.md) for the import,
style-loading, and distribution contracts.

### Rich input ownership

Agents and App Studio use the same canonical rich composer. `composer.ts` owns
neutral content parts, exact attachment receipts, skill summaries and resource
coordinates; it has no provider API imports. Thin adapters supply provider
fetch/discovery callbacks, upload routes, draft/session scope and acceptance.
A successful upload is still a draft until the provider admits the turn. On
acceptance the provider transfers receipt ownership to the transcript before
clearing the editor; failed or canceled sends retain retryable drafts.

The Agents composer waits until its active session is selected before accepting input. New thread can claim a session during bootstrap; a late history response must not replace that session or its draft.

The slash palette keeps the caret in the editor for command discovery, then
moves focus to the skill/resource filter. Arrow keys navigate, Enter selects,
and Escape moves back or dismisses and restores the invoking focus. Selection
is removable through the keyboard as well as a pointer. Per-agent activation
and per-turn selection are distinct: the library does not enable skills by
itself. Resource references supply context, never additional authority.
The mounted Agents palette refreshes when the Agent’s enabled skill references change, removes disabled selections, and ignores obsolete catalog responses.
Initial model and provider lookups interrupted by a workbench route change are retried; completed metadata remains cached for the current authority.

Agents exposes reusable package management in its top-level **Skills library**, outside chat. It reads workspace Skill objects through the caller’s kube client and provider packages through the published hub catalog; no Agent is required. Library package keys are presentation identifiers, not runtime skill selections. Workspace libraries hide per-owner activation badges; they have no global enabled/disabled state. The Agent **Skills** workbench reuses activation controls, defaults to enabled skills, offers available skills separately, and allows missing-reference cleanup. Config contains no skill activation controls. A library visit from an Agent retains a return link to that Agent’s Skills tab. Execution qualification, enablement checks, and per-turn selection remain in the existing authorized Agent catalog.

The library editor parses supported frontmatter with the backend’s YAML semantics, including quoted keys and folded scalar descriptions. Root YAML merge keys are rejected; a quoted literal `"<<"` key remains an ordinary string. Editing and export retain the original source document, including unrelated frontmatter and Markdown line endings. Pending writes and imports lock conflicting editor actions so an earlier operation cannot discard a newer draft.

Catalog rows advertise a dialog only when the caller opens one; inline details
retain inline semantics. Catalog refreshes retain the loaded snapshot, mark its
region busy, and announce progress outside that busy region. Failed detail and
authorized image-preview reads expose an explicit Retry action.

Image and text previews use supported content kinds within their size bounds.
PDFs and other opaque files show “Stored file; contents unavailable for analysis”; do not present
successful storage as successful content analysis. Preview object URLs and
requests are released on scope changes or unmount. Coarse-pointer add, remove,
retry and palette controls retain 44×44px targets. Shared styles are injected
through AgentKit so teleported panels and standalone providers receive the
same theme and geometry without relying on a consumer's utility compiler.
Workbench headings and their actions wrap at narrow pane widths so controls do
not squeeze the description into a word-by-word column.

The `k-ai-search` recipe owns the leading icon, text inset, clear-button reserve
and vertical centering for skill activation, the skill library and palette
filters. Clear actions return focus to their input; custom clear controls hide
the browser's native cancel decoration. Coarse pointers retain 44px controls.
The Add menu anchors to its actual trigger rather than the editor's static
flow position. The rich editor uses the shared composer's keyboard focus frame.
These controls must not depend on a host's generated utility classes.

Attachment failures expose their reason as visible status text associated with
the attachment group. An ambiguous upload response retains the retryable draft
under its stable upload identity; automatic cleanup must not race an idempotent
retry. Explicit removal and leaving the draft still clean up uncommitted inputs.
Image retry and remove actions sit above the status label
with 44px coarse-pointer targets. Removing an attachment returns keyboard focus
to the composer only when that action owned focus and the draft scope remains
current; an asynchronous deletion must not steal focus after the user moves on. Retry
uses the same recovery path, and the final scope/focus check and focus operation
run in one render-flush callback so a second queued tick cannot steal focus.

Accepted attachments sit above the user text bubble in both portals; attachment-only turns have no empty bubble. Files uses the same synchronous hidden-input activation as App Studio project conversations. The shared Add menu is teleported to the document body and positioned with PortalKit’s `useAnchoredPopover`, outside provider container-query, clipping, and transformed layout ancestors. Dismissal and keyboard navigation include both the trigger and the teleported panel. Style version 48 resets the previous inline-menu offsets when updating a live host. Native cancellation restores editor focus; element focus events must not impersonate window focus.

Draft or attachment-owner changes dismiss menus/palettes and invalidate native
file-picker completion and focus callbacks. Multi-file uploads stop when their
starting draft changes. Palette filtering leaves IME confirmation keys to the
browser. Skill-library consumers sort their catalog and delegate multi-term
filtering to the shared list so searching behaves consistently across portals.

Palette listboxes/options and attachment error descriptions use instance-owned
IDs that remain distinct across separate Vue roots. Keyboard entry selects the
first available option and recalculates it when the result count changes.
Unmatched commands expose a status message. Inline skill/resource chips name
their Remove action explicitly and use the shared touch-target recipe.
App Studio starter prompts adapt to conversation-pane width and retain their
full labels rather than clamping text inside viewport-sized columns.

The command palette dismisses when focus leaves either its panel or its slash
editor, emitting `close(false)` so consumers preserve the new focus target.
Focus transitions use the event’s `relatedTarget`, because the browser can
report `body` as active between two internal controls. Escape works from the
Back button as well as the filter/list. Internal focus transfers are guarded
while changing views, and a queued focus transfer cannot rebind an editor after
unmount. Selection callbacks commit atomically before dismissal so consumer
focus changes cannot trigger a second outside-focus close. Resource lookup
rejections settle into an announced warning; Back and provider selection retry
the lookup, and results arriving after Back cannot overwrite the new view.

The neutral types are also part of the contract: `AIMessageRole` is `user` or
`assistant`; `AIActionStatus` accepts the built-in `idle`, `running`, `waiting`,
`succeeded`, `skipped`, `failed`, `rejected`, `canceled`, `retrying`,
`recovered`, and `stopping` values plus an opaque provider value;
`AIActionView` carries action identity and display state; `AIConversationItem`
is the minimum conversation identity and optional title/status/timestamps;
`AIConversationRailCapabilities` gates create, pin, unread, archive, and
delete; `AIConversationRailLabels` supplies caller-owned accessible copy;
`AIInterruptKind` is `approval` or `follow-up`; `AIInterruptStatus` is
`pending`, `busy`, `resolved`, or `failed`; and `AIPrimaryActionState` is
`send`, `stop`, or `stopping`.

The canonical prose recipe is `.k-ai-prose` in AgentKit’s `agent-ui.css`. After the
provider sanitizes Markdown, wrap the rendered content in that class. It
provides semantic links, headings, paragraphs, lists, blockquotes, inline and
block code, rules, tables, overflow handling, and theme tokens. Code-copy
controls or richer Markdown behavior remain provider-owned and must be added
after sanitization.

## Behavior

`AIMessage` aligns user content to the end and assistant content to the start;
the caller chooses whether user content receives the compact bubble treatment.
Activity is independently expandable. `AIActivityDisclosure` exposes its
panel through `aria-expanded` and `aria-controls`; `AIActionRow` uses the same
contract for details and reports caller-supplied state with visible and
screen-reader text. An action may remain busy, waiting for attention, failed,
or canceled; the component does not transition it on its own.

`AIComposer` stays a frame around caller content and marks disabled or queued
state. `AIPrimaryAction` presents `send`, `stop`, and `stopping`; the caller
must acknowledge submission or cancellation and decide when state changes.
`AIInterrupt` keeps invalid or unavailable details fail-closed through its
error presentation, while the caller validates authority and controls the
resolution actions. App Studio uses its `approval` and `follow-up` kinds;
Agents uses the approval kind for its provider-owned approval flow.

The rail filters items by title, groups pinned items when supplied, and shows
loading and empty states. It disables selection and lifecycle actions while
the caller reports a busy or in-flight operation. Width and anchored state are
persisted only when the caller provides a non-empty, caller-owned
`storageScope`; persisted layout state is best effort. Desktop supports hover,
focus, keyboard resize, and a context menu. Mobile opens the rail as an
overlay and restores focus to the opener. Capability flags control which
lifecycle events can be emitted; the provider still owns confirmation and
mutation results.

Clicking the conversation title, double-clicking a thread in the list, or
choosing Rename in a thread's context menu
opens `AIConversationRenameDialog` in both Agents and App Studio. The shared
modal has explicit Save and Cancel controls, Escape dismissal, focus restoration,
and errors that keep the editor open for retry. Renaming a background thread
from its context menu preserves the current thread. A single thread-list click selects the
conversation; a double-click opens the modal after selection finishes. Page
titles open the modal on a single click and are never edited inline. The context menu remains available to keyboard and touch users.

Anchoring or collapsing the rail changes its reserved width immediately. Only
the panel's transform and opacity animate, so opening and closing do not
interpolate layout width on every frame. Pointer resizing remains immediate.

`AITimestamp` preserves the provider value in a semantic `<time>` element's
`datetime` attribute while showing a relative label. Hover and focus expose the
full locale value in the tooltip; clicking toggles the visible label to that full
value and back. Missing or invalid values render no timestamp surface. The
provider remains authoritative for whether a message has a timestamp.

`AITurnProgress` determines whether its `details` or default slot contains
renderable nodes during render. It omits the disclosure trigger and details
region for an empty slot, and reacts when a later trace update supplies nodes.

`AIConversationHeader` supplies a 56px compact titlebar frame and belongs above
`AIConversationLayout` when the titlebar should span both the conversation rail
and transcript. The rail's `Threads` header starts immediately below that
titlebar. `AIConversationLayout` supplies the rail/main frame.
`AIConversationIdentity` supplies the
caller-slotted 32px icon tile, primary title, and secondary context. The shared
`.k-ai-conversation-header__divider` is a 1px by 24px visual divider; callers
own its placement. `ResourceBackLink` can opt into the
`.k-back-action--icon-only` 32px icon-only recipe for this titlebar while keeping
its real `href` and caller-owned accessible name. `AITranscript` supplies the
readable inner column. Native transcript scrolling, focus placement, keyboard handling, and
provider navigation remain caller-owned; the shared frames do not fetch,
scroll, focus, or change provider state.

`AIWorkspace` keeps a conversation visible beside a caller-owned workbench on
wide layouts. It switches to one pane when the available width cannot satisfy
the minimum conversation and workbench widths; its narrow workbench view
provides **Back to conversation**. The desktop divider supports pointer and
keyboard resizing within bounded split percentages. Opening focuses the
workbench (or its narrow back control), Escape closes it, and closing restores
focus to the triggering control or visible conversation region. The component
does not own route state or workbench contents. It emits
`compact-change(compact: boolean)` on initial measurement and when its layout
changes, accounting for both `mobileBreakpoint` and the measured minimum pane
widths. Callers can use this event to suppress an automatic workbench open
without overriding an explicit user open or deep link.

`AIWorkbenchTab` provides the shared `.k-workbench-tab` wrapper and
`.k-workbench-tab__button` control, including the icon and label layout, ARIA
props, and click, keyboard, and drag event forwarding. Its optional `leading`,
`after-label`, and `trailing` slots preserve App Studio's grip, close control,
and review dot without imposing lifecycle controls on fixed tab sets.
`AIWorkbenchTabs` keeps tab frames at their natural width, up to 240px, inside a
horizontal viewport. When that viewport overflows it shows accessible
previous/next controls; when selection changes, the selected tab scrolls into
view without moving focus. The shared recipe owns 32px bordered tab geometry,
112–240px width bounds, 6px radius, 12px/500 typography, an accent active
border at 40% and background at 10%, and 44px sizing for coarse pointers.
Coarse-pointer tabs use a 144px minimum width so their labels remain
identifiable beside close controls. App Studio owns tab lifecycle (drag,
reorder, and close); Agents uses the same primitive for fixed Config and Runs
tabs without drag or close controls while retaining provider-owned route and
workbench behavior.

`AIWorkbenchPane` gives document tabs a white light-theme canvas from
`--color-surface-raised` and 16px of inset space. Its heading uses a plain
16px/600 title, an optional 12px description, and a trailing actions slot; it
has no decorative icon, title bar, or enclosing card. The standalone
`.k-ai-workbench-pane__header`, `__heading`, `__title`, `__description`, and
`__actions` recipes also support headers rendered outside the pane, such as a
teleported Settings heading. Use the `tool` variant for editor or preview tabs:
it keeps the same host canvas while leaving intrinsic tool surfaces and their
internal spacing to the provider. Group independent forms or actions with
subtle separators and neutral same-canvas surfaces only where that grouping
helps the task; avoid wrapping an entire pane in a second card.

When this chrome is nested in `ResourcePage`, use its optional `fill` prop only
when the conversation must absorb the parent’s remaining height. The default
is content-sized; the fill modifier owns only root/body flex sizing and leaves
transcript scroll and focus behavior with the caller.

## Content

Use concrete action and state labels supplied by the provider. Keep exact tool
names, arguments, outcomes, source links, and errors in provider-owned slots
when the user needs them to assess a run. Preserve the distinction between a
pending approval, a running action, a failed action, and a completed action.
Unknown states admitted by a provider adapter remain opaque in AgentKit and must not be relabeled as success. Provider transport parsers retain their own schema allowlists; AgentKit does not bypass admission checks.
Accessible labels may be passed through the rail's label map so provider
terminology remains intact.

## Layout and responsive behavior

The shared recipe uses semantic color tokens, compact 4px control radii, and
6px card or transcript surfaces. `AITranscript` is capped at `820px`, centered,
and uses a `20px` message gap. Its caller-owned scroll root uses `12px 16px`
gutters. Assistant prose uses 13px text with generous line height; user bubbles
are capped at 86% of the available message width below `640px`, and 72% from
`640px` upward.
The shared conversation layout uses an opaque mix of 70% `surface-raised` and
30% `surface`, preserving the raised tint in both host and standalone provider
portals.
Activity details scroll within a panel capped at `min(40vh, 320px)` and
long prose/code/table content can scroll horizontally inside `.k-ai-prose`.

`AIComposer` exposes separate editor, footer, help, and primary slots. The
shared recipes also provide a bottom control row (`.k-ai-composer__controls`)
with control groups and trailing actions; editor behavior, attachments,
context, queue, and submission remain caller-owned.

The conversation rail is hidden as a desktop rail below 768px and opens as a
256px mobile overlay. At desktop its width is caller-resizable from 192px to
384px, subject to preserving a 240px chat area. Coarse-pointer controls grow
to at least 44px. Reduced-motion users receive no spinner animation or
transition.

`AIWorkspace` uses a resizable desktop split with a default 55% conversation
share, bounded between 35% and 70%, while guarding its default 480px
conversation and 360px workbench minimums. At compact widths it presents one
pane at a time and hides the desktop divider.

## Accessibility

Use the native buttons, `article`, `aside`, `section`, `ol`, and list semantics
provided by the components. Disclosure triggers expose their expanded state
and controlled panel. The rail gives the list, search, current item, loading,
unread, and updating state accessible names; context-menu actions use menu-item
roles with keyboard navigation. Escape closes transient rail presentation and
focus returns to the opener when the mobile rail or context menu closes.

For `AIWorkspace`, keep the separator focusable with its resize semantics and
preserve the component's open/close focus contract. Escape closes the workspace
unless a nested dialog or menu owns the event; closing returns focus to the
trigger when it is still connected, otherwise to the visible conversation.

Keep provider editor semantics, focus placement, live announcements, and
sanitized content behavior in the caller. Do not replace the shared focusable
controls with click-only wrappers. Preserve reduced motion and the semantic
color tokens in both light and dark themes. `AITimestamp` uses a native button
around its semantic `<time>` and exposes the full value through its accessible
label and title; retain that keyboard and assistive-technology path.

## Code and evidence

The canonical implementation is under
[`provider-sdk/agentkit-vue/`](../../../provider-sdk/agentkit-vue/):
`AIMessage.vue`, `AIActivityDisclosure.vue`, `AIActionRow.vue`,
`AIComposer.vue`, `AIConversationHeader.vue`, `AIConversationIdentity.vue`,
`AIConversationLayout.vue`,
`AIPrimaryAction.vue`, `AIInterrupt.vue`, `AIConversationRail.vue`,
`AITranscript.vue`, `AIWorkspace.vue`, `AIWorkbenchPane.vue`,
`AIWorkbenchTab.vue`, and `ai.ts`. Shared recipes are in
[`agent-ui.css`](../../../provider-sdk/agentkit/agent-ui.css), and the
distribution contract is [`hack/sync-portalkit.sh`](../../../hack/sync-portalkit.sh).
`AITimestamp.vue` and `timestamp.ts` are canonical in the same AgentKit Vue
directory and are synced only to Agents and App Studio, where both consumers use
the shared timestamp. App Studio remains the visual reference; provider adapters
own timing, status, phase, and authority decisions. `AITurnProgress`'s render-time
slot inspection is likewise canonical in `AITurnProgress.vue`.
App Studio adopts the shared conversation chrome in `App.vue` and
`AssistantActionLog.vue`, with `.k-ai-prose` for assistant content. Its
project split remains App Studio-owned and has not migrated to `AIWorkspace`.
Agents uses `AIWorkspace` for its agent workbench. Run
`make sync-portalkit` after canonical changes and `make verify-portalkit` to
check source copies and explicit manifests, including the declared canonical-to-vendored core-component path mapping. Standalone runtime fallback
CSS is imported through Vite's `?inline` path and may be minified in the
bundle; the authored canonical rules and synced source copies remain
byte-identical. The current contracts are core PortalKit version 36 (from
`RAILGRID_UI_CORE_VERSION` in `provider-sdk/portalkit/styles.ts`) and AgentKit
style/runtime version 46 (from `AGENT_UI_VERSION` in
`provider-sdk/agentkit/styles.ts`). Desktop rails stretch
to the flex row's cross-axis so content-sized conversation frames retain a
visible panel; mobile rails keep their absolute inset overlay geometry. Run
`make verify-design-docs` for this contract. These static checks do not
establish a live model call. Final rendered fixture evidence is separately
recorded for the Agents and App Studio consumers; real served-runtime and Tilt
health evidence remains a distinct check.

## Related guidance

Use [App Studio conversation and modes](../ai/app-studio-conversation.md) for
thread authority, response modes, approvals, and bounded context. Use
[Agents autonomy and run transparency](../ai/agents-autonomy-and-runs.md) for
interactive/background grants, run evidence, and approval phases. Use
[evidence and status](../ai/evidence-and-status.md) for state language and
[the distributed PortalKit asset index](portalkit-assets.md) for sync
ownership.

The workbench separator is `AIPaneDivider.vue` in both `AIWorkspace` (Agents) and App Studio. It occupies its own 6px track without negative margins; providers retain split geometry and resize events. Historical rendered evidence labels the separator as AgentKit stylesheet version 2; the current AgentKit stylesheet/runtime contract is version 15.

The shared `AIWorkbenchTabs` strip supplies close and new-tab controls and
forwards selection and drag events. Its fixed-width tab frames scroll
horizontally when needed, with accessible previous/next controls and selected
tab visibility that does not move focus. `AIWorkbenchLauncher` supplies the search,
existing-tab, and suggested-tool presentation. Providers own filtering, tab
identity, ordering, navigation, and persistence; the shared components do not
open resources or mutate provider state themselves.

The tab strip and launcher rules live in injected `agentkit/agent-ui.css`,
not SFC-only stylesheets: classic provider bundles must receive them through
`ensureAgentUIStyles()`. Version 6 upgrades an already-loaded version 5.

Workbench lifecycle verification (2026-09-10): Agents passed four built-bundle
fixture cases (review/mobile, light/dark), including New tab, close/last-tab,
keyboard and drag reorder, retained drafts, and computed layout styles.
The same fixture verified host hash-only navigation back to the Agents list.
Evidence: `/tmp/adoption-workbench-injected-final.log`. Both local Tilt provider
health endpoints returned 200 and served bytes matched their final builds
(`/tmp/workbench-lifecycle-runtime.json`). These are separate rendered/mock and
served-runtime checks; they do not establish live backend model execution.

App Studio also passed four built-bundle fixture cases (desktop/mobile,
light/dark) for the shared picker, existing-tab selection, close, and drag
reorder. Final settled screenshots are under
`/tmp/agent-studio-workbench-browser-v7/`; parent reviewed both providers.

### Command output receipts

`AIExecutionDetails` presents command activity as one terminal surface with a
quiet Shell label, copy and line-wrap controls, preserved output whitespace,
and status plus duration. Long output starts with a twelve-line preview;
Show full output reveals the provider-supplied content in the transcript's
scroll area. A provider-truncated result remains explicitly marked. Generic
tools retain labeled arguments; transcript cards omit redundant execution
metadata. Approval authority and App Studio source-provenance warnings
remain provider-owned. Agents recognizes only known shell tools when
projecting readable command summaries.

Live verification on 2026-11-07 used the existing Tilt stack: saved Agents
command results and an App Studio temporary project executing real commands
with exit codes 0 and 7. Both portals passed light/dark desktop and phone-width
checks. App Studio additionally passed preview expansion/collapse, complete
output copying, and line wrapping. The temporary project, development instance,
model binding, and credential copy were removed and original model settings
restored. A separate approval-resume attempt failed before execution; the
command-output checks used the normal `on_request` policy and do not establish
approval-resume correctness.

Rendered evidence: [Agents desktop](../evidence/command-output/command-output-agents-1440-light.png),
[Agents phone](../evidence/command-output/command-output-agents-390-dark.png),
[App Studio desktop](../evidence/command-output/command-output-studio-1440-light.png),
and [App Studio phone](../evidence/command-output/command-output-studio-390-dark.png).

Worked for contains the ordered commentary, tool groups, and command output. It opens during execution so streamed tool starts are visible, and
preserves explicit reader choices across updates. Single actions have no count wrapper; multiple actions share one semantic summary
(such as Edited files, read files, ran commands) with a call count and aligned
individual rows, without category subgroups. Worked for, tool groups, and individual output
nodes reveal their intrinsic height with a brief fade (180ms open, 140ms close);
reduced-motion preferences disable these transitions. Generic inspections, including preview
screenshots, use Inspected resources rather than claiming to read files. Only
explicit file operations use Read files.
The shared presentation classifies only unambiguous simple command arguments
for labels such as Listed files, Read, and Searched for; compound shell
programs retain their exact command description. Unknown, blocked, and canceled
outcomes do not acquire successful-execution labels. Active groups open as calls
arrive, unless the reader explicitly closes them; long active lists follow new
calls until the reader scrolls up. Group and row text stays at 12px. Transcript command cards omit internal
tool/invocation and source-sync metadata; approval information is still shown in full.

Live transcript verification covered native Agents shell calls and App Studio
commands while turns were still running, 12px group/row text, tool output inside expandable
Worked for details, and desktop/phone light/dark views. The source metadata block no
longer appears beneath App Studio command cards.

The App Studio preview transcript regression is covered by the
[corrected three-call group](../evidence/command-output/preview-group-corrected.png).
Live verification confirmed two delayed commands appeared before completion,
expanded output survived completion and group/Worked for collapse and reopen,
and no tool rows appeared outside Worked for.

Message copy controls use provider-supplied visible text for user messages and
assistant responses. On hover-capable desktops, sender labels, timestamps, and
message actions appear on message hover or keyboard focus; touch layouts keep
them visible. Copy confirmation and failure feedback remain accessible.
