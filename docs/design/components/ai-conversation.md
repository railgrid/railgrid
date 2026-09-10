---
{"schema":1,"id":"design.components.ai-conversation","title":"Shared AI conversation components","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Optional AgentKit owns shared AI conversation and model presentation. Canonical Vue sources live in provider-sdk/agentkit-vue and styles in provider-sdk/agentkit. Only App Studio and Agents receive AgentKit copies; provider behavior remains provider-owned."},"appliesTo":["portalkit","portal","provider-portals","assistant"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/ai-conversation.md#shared-ai-conversation-components","role":"design"},{"path":"provider-sdk/agentkit-vue/AIActionRow.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActivityDisclosure.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIComposer.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationHeader.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationIdentity.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceBackLink.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationLayout.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationRail.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIInterrupt.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIMessage.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITimestamp.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/timestamp.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPrimaryAction.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITranscript.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkspace.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchTab.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ai.ts","role":"implementation"},{"path":"provider-sdk/agentkit/agent-ui.css","role":"implementation"},{"path":"hack/sync-portalkit.sh","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIConversationTurn.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AITurnProgress.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPlanDisclosure.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPlanSteps.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIActivityFeed.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/conversation.ts","role":"implementation"},{"path":"provider-sdk/agentkit/activity.ts","role":"implementation"},{"path":"provider-sdk/agentkit/conversation.css","role":"implementation"},{"path":"provider-sdk/agentkit/activity.css","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIExecutionDetails.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIPaneDivider.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchTabs.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/AIWorkbenchLauncher.vue","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Final AgentKit sync parity passed; shared sync, design-doc, and UI-conformance gates also passed, covering 420 files with zero violations."},{"kind":"command","ref":"final Agents and App Studio portal gates","status":"passing","evidence":"Agents final result: 399 tests across 29 files, typecheck, and Vite build passed. App Studio final result: 67-file suite, typecheck, formatter/projection focused suites, Vite build, and budgets passed; formatter output remained unchanged."},{"kind":"browser","ref":"AgentKit final conversation fixture matrix","status":"passing","evidence":"Final Agents and App Studio built bundles passed desktop and mobile light/dark fixtures, including the Agents six-case rail/titlebar matrix (932px with a 216px host sidebar, 1551px desktop, and 390px mobile) and the final App Studio timestamp/disclosure check. Rail/titlebar geometry, collapse/reopen, mobile open/Escape, and overflow checks passed; see /tmp/adoption-rail-header-browser.log. This is mocked rendering evidence; it is separate from served runtime and Tilt, and does not establish a live backend model call."},{"kind":"browser","ref":"AgentKit standalone run fixture matrix","status":"passing","evidence":"2026-09-09: standalone Agents run passed 1551x900 and 390x844 in light/dark (4 cases): progress, step expansion, run inspector and no horizontal overflow/runtime errors. The evidence panel is width-constrained so its child-run table scrolls internally. /tmp/agentkit-final-run-browser.log and /tmp/agentkit-rendered/run-final-*.png. Built bundle with mocked APIs; no live approval/backend action."},{"kind":"browser","ref":"Shared AIPaneDivider verification","status":"passing","evidence":"2026-09-09: both built providers passed desktop/mobile light/dark fixtures (8 cases). Desktop separator measures 6px, zero margins, 16px grip, 1.75 stroke with identical theme colors; pointer and keyboard resizing work, divider hidden on mobile. Parent reviewed screenshots. /tmp/divider-{agents,app-studio}-browser.log and /tmp/divider-rendered/. Mocked APIs, not live backend interaction. Final Agents and App Studio portal builds and typechecks passed; shared sync, design-doc, and UI-conformance gates passed for 420 files with zero violations."},{"kind":"browser","ref":"Served Agents and App Studio runtime with Tilt health","status":"passing","evidence":"Both Tilt health endpoints were healthy, and served Agents main.js plus App Studio page-element bytes exactly matched the final builds. Evidence: /tmp/adoption-final-runtime-evidence.json. This served-runtime check did not make a model call."}]},"relatedDocuments":[{"id":"design.ai.app-studio-conversation","relation":"see-also"},{"id":"design.ai.agents-autonomy-and-runs","relation":"see-also"},{"id":"design.ai.evidence-and-status","relation":"see-also"},{"id":"design.components.portalkit-assets","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also","path":"docs/design/foundations/recipes.md"}]}
---

# Shared AI conversation components

## Purpose

PortalKit's AI conversation set supplies neutral conversation chrome, transcript,
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
Markdown, attachment, queue, or cancellation layer. Providers own API and
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
| `AIActionRow` | One ordered action with explicit status, target, outcome, busy/attention/error/canceled flags, and optional details disclosure. The details slot holds provider-safe execution data. |
| `AIComposer` | Persistent composer frame with disabled and queued variants; editor, context, attachments, queue, and provider controls are slots. |
| `AIConversationHeader` | Compact titlebar frame; title, navigation, loading, and provider controls remain in the default slot. |
| `AIConversationIdentity` | Compact identity with caller-owned `icon`, `title`, and optional `context` slots; the shared tile is 32px, with 13px primary title and 11px secondary context. |
| `AIConversationLayout` | Rail/main presentation frame; child layout, scroll roots, focus refs, and provider navigation remain caller-owned. |
| `AIPrimaryAction` | Caller-controlled `send`, `stop`, or `stopping` button state. It emits a click; it does not submit or cancel. |
| `AIInterrupt` | Approval or follow-up frame with pending, busy, resolved, or failed status, optional invalid state, error slot, and action slot. |
| `AIConversationRail` | Searchable, collapsible, resizable conversation list with mobile overlay behavior, focus management, and capability-gated create/pin/unread/archive/delete actions. |
| `AITranscript` | Centered readable transcript column; the caller owns its scroll root, focus behavior, and message lifecycle. |
| `AIPaneDivider` | Shared 6px pane separator with a 16px grip, 1.75 stroke, hover/focus color and reduced-motion support; callers own pointer/keyboard resize logic. |
| `AIWorkspace` | Resizable conversation/workbench split with minimum pane widths and a narrow single-pane transition; callers own route state and both pane contents. |
| `AIWorkbenchTab` | Shared workbench tab wrapper and control with ARIA wiring and forwarded click, keyboard, and drag events. Optional `leading`, `after-label`, and `trailing` slots support lifecycle chrome; callers own collections, selection, and keyboard behavior. |

Composed presentation extends those primitives:

| Component | Responsibility and caller-owned surface |
| --- | --- |
| `AIConversationTurn` | Orders provider-owned progress, trace, output, interrupt, and metadata slots within the neutral message frame. |
| `AITurnProgress` | Presents Working/Worked/Stopping state, optional provider-formatted duration, interrupted state, and a controlled trace disclosure. It inspects the details slot at render time, so empty details have no chevron and later trace updates can appear. It never starts a clock. |
| `AIPlanDisclosure`, `AIPlanSteps` | Display an already-validated plan with completion counts, status icons, accessible disclosure, and mobile geometry. Provider admission remains authoritative. |
| `AIActivityFeed` | Composes ordered rows and optional named groups with keyed expansion and bounded scrolling. Providers supply summaries, groups, status, and safe details. Empty-label groups render as flat activity. |
| `AIExecutionDetails` | Renders provider-sanitized command, arguments, output, status, timing, and safe detail links for approval and activity variants; parsing and authorization remain with the provider. |

AgentKit is opt-in: only Agents and App Studio receive its generated copies.
General UI primitives and tenant helpers remain in PortalKit. See the
[AgentKit source guide](../../../provider-sdk/agentkit/README.md) for the import,
style-loading, and distribution contracts.

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
does not own route state or workbench contents.

`AIWorkbenchTab` provides the shared `.k-workbench-tab` wrapper and
`.k-workbench-tab__button` control, including the icon and label layout, ARIA
props, and click, keyboard, and drag event forwarding. Its optional `leading`,
`after-label`, and `trailing` slots preserve App Studio's grip, close control,
and review dot without imposing lifecycle controls on fixed tab sets. The
shared recipe owns 32px bordered tab geometry, 112–240px width bounds, 6px
radius, 12px/500 typography, an accent active border at 40% and background at
10%, and 44px sizing for coarse pointers. App Studio owns tab lifecycle (drag,
reorder, and close); Agents uses the same primitive for fixed Config and Runs
tabs without drag or close controls while retaining provider-owned route and
workbench behavior.

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
`AITranscript.vue`, `AIWorkspace.vue`, `AIWorkbenchTab.vue`, and `ai.ts`. Shared recipes are in
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
byte-identical. The current contracts are core PortalKit version 15 and AgentKit
style/runtime version 5. Desktop rails stretch
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

The workbench separator is `AIPaneDivider.vue` in both `AIWorkspace` (Agents) and App Studio. It occupies its own 6px track without negative margins; providers retain split geometry and resize events. Historical rendered evidence labels the separator as AgentKit stylesheet version 2; the current AgentKit stylesheet/runtime contract is version 5.

The shared `AIWorkbenchTabs` strip supplies close and new-tab controls and
forwards selection and drag events. `AIWorkbenchLauncher` supplies the search,
existing-tab, and suggested-tool presentation. Providers own filtering, tab
identity, ordering, navigation, and persistence; the shared components do not
open resources or mutate provider state themselves.

The tab strip and launcher rules live in injected `agentkit/agent-ui.css`,
not SFC-only stylesheets: classic provider bundles must receive them through
`ensureAgentUIStyles()`. Version 5 upgrades an already-loaded version 4.

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
