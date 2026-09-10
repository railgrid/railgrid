# Agents conversation UX: App Studio alignment

Status: implemented and verified in this workspace; the changes are not
deployed.

## Direction

App Studio remains the sole gold-standard visual and interaction reference:
neutral right-aligned user messages, unboxed assistant responses, activity
attached to the owning turn, compact disclosures, a persistent composer, and a
collapsible conversation rail. Agents consumes the shared presentation while
retaining its own run phase, interactive/background authority, approvals,
sources, usage, and child-run evidence.

This review uses the current worktree, including its existing App Studio changes. It is a source review, not a live-product audit. The mockup uses fictional research content and local state transitions; it invokes no model or provider actions.

## Opportunities

| Priority | Shared opportunity | Current implementation and mismatch | Proposed boundary |
| --- | --- | --- | --- |
| First | Conversation message and transcript layout | App Studio `App.vue` and Agents `ChatMessage.vue`/`RunDetail.vue` consume `AIMessage` for neutral transcript geometry. | `AIMessage` supplies transcript geometry and slots. Providers own stable IDs, history, revisions, sanitization, attachments, and run projection. |
| First | Activity disclosure and tool rows | App Studio `AssistantActionLog.vue` and Agents chat/run views consume `AIActivityDisclosure` and `AIActionRow`. | Shared rows keep ordered state and disclosure semantics; provider adapters supply labels, safe arguments/results, durations, failures, and lifecycle truth. |
| First | Composer shell and primary action | App Studio and Agents chat consume `AIComposer` and `AIPrimaryAction`. | Shared components provide frame and explicit send/stop/stopping presentation. Providers own IME-safe submit, drafts, queueing, and stop acknowledgement. |
| First | Markdown presentation and code actions | Both providers retain their own Markdown parsing and sanitization and use the shared `.k-ai-prose` recipe after content handling. | Share the prose recipe and trusted code-copy affordance. Keep parser, sanitizer, URL, streaming, and custom-content behavior provider-owned. |
| First | Approval/interrupt frame | App Studio consumes `AIInterrupt` for permission and follow-up frames; Agents consumes it for approval frames while retaining provider validation. | Shared frame owns layout and status presentation. Providers retain authority checks, disclosed details, validation, inbox resolution, and action acknowledgements. |
| Next | Conversation rail | App Studio and Agents consume `AIConversationRail`; App Studio supports its configured rail actions, while Agents exposes session create/select/delete only. | Use the shared rail with explicit capability flags and caller-owned navigation. Do not imply Agents has durable titles, pins, unread, or archive semantics today. |
| Next | Model and policy controls | App Studio `ModelPicker.vue`, `ResponseModePicker.vue`, and `ApprovalModePicker.vue` are compact composer controls. Agents configures model and autonomy on the agent. | Reuse picker geometry and accessibility, with provider-owned choices and persistence. Initially display Agents model/autonomy as context. Agents Suggest/Ask/Auto are not aliases for App Studio approval policy or Default/Plan/Review. |
| Next | Structured plan presentation | App Studio `AssistantPlanDisclosure.vue`, `AssistantPlanSteps.vue`, and `AssistantPlanPopover.vue` present typed plan state. Agents' inspected chat/run DTOs lack this structured plan contract. | Extract `AIPlanDisclosure` with neutral plan/step types. Enable in Agents only when structured plan events exist. Never infer checked steps from assistant prose. |
| Later | Queued follow-ups and steering | App Studio `AssistantMessageQueue.vue` and its queue controller support edit/remove/steer. The inspected Agents composer does not expose that contract. | Share queue presentation after Agents defines submit/queue/steer acknowledgement, persistence, deduplication, cancellation, and reconnect behavior. A shared button cannot supply these guarantees. |
| Later | Attachments and context selection | App Studio's `AssistantRichComposer.vue` directly imports project APIs, attachment receipts, resources, skills, and preview annotations. Agents' inspected message shape is text/tools/approval/usage. | Separate receipt/chip/preview presentation from upload/read/context services. Pass provider adapters; retain tenant ownership and receipt validation. Preview annotations remain App Studio extensions. |
| Cross-cutting | Loading, stale, error, and scroll state | Both providers already implement recovery, with different durable data and stream reconciliation. | Reuse existing PortalKit notifications and focused scroll/focus behavior. Preserve last good history on refresh failure, retain drafts, announce bounded state changes, and reconcile by stable identities. Keep transport and projection in each provider. |

## Revised Agents composition

The agent identity stays in the header. A collapsible Conversations rail replaces the session select; the center is the App Studio conversation pattern. The composer stays anchored below the transcript. Run details open alongside the transcript on wide screens and replace the center temporarily on narrow screens with an explicit close/back control.

Each turn shows activity before its answer. Completed activity collapses, while pending approval and failed activity remain visible. Tool rows use plain-language labels with exact tool names and arguments available on expansion. Unknown tools retain their exact name; labels are never invented from presumed effects. Source links remain next to the answer.

Run details retain phase, run ID, trigger, class, session, parent, timing, usage, and child runs. Scheduled/background run inspection reuses the same turn renderer with input/output from the run record; it does not show a composer that implies the user can steer that background run. An explicit Open conversation action is appropriate only when a session is available.

The prototype shows Agents' existing elapsed-duration concept as “Elapsed,” rather than claiming App Studio's pause-excluding “Worked for” semantics. Matching that label requires an authoritative active-work duration in Agents. A live stream loss must remain distinct from run termination: reconnecting does not mean stopped.

## Extraction architecture and sequence

Canonical Vue presentation belongs in `provider-sdk/portalkit-vue/`; the seven shared components and neutral types are implemented there and consumed by App Studio and Agents in this workspace. Shared CSS recipes belong in `provider-sdk/portalkit/faros-ui.css`. The sync manifest distributes the canonical files to Vue portals; run `make sync-portalkit` after canonical changes and consume generated copies in provider portals. Provider adapters and lifecycle decisions remain provider-owned.

1. Completed the shared extraction for message layout, prose styling, activity, composer, approval frame, primary action, and conversation rail; App Studio and Agents consume the seven shared components.
2. Agents adapters in `ChatMessage`, `AgentChat`, and `RunDetail` preserve server order and the provider's pending-approval and child-run semantics; shared presentation cannot establish interleaved chronology or lifecycle authority.
3. Address rich context, structured plans, queueing, steering, and Agents session adaptation as separate API work only where useful. Do not expand the extraction into a shared orchestration framework.

Shared components must not import provider `api`, project types, tenant lookup, event streams, provider storage keys, or backend policy. Use callbacks/slots and neutral view data. Providers remain responsible for sanitization boundaries, authority checks, tenant scoping, projection, reconnect, and mutation acknowledgements.

## Acceptance and verification

For this proposal: enumerate source-backed opportunities, distinguish API gaps, and record the shared extraction boundary. The earlier responsive mockup remains illustrative; production behavior is established by the canonical components and each provider's adapters.

For implementation: verify App Studio parity and Agents integration separately. Focus behavioral tests on ordered/stable messages, pending and failed action visibility, approval fail-closed behavior, IME/newline input, drafts, Stop acknowledgement versus termination, session changes, and reconnect deduplication. Preserve existing regression tests; use representative component fixtures rather than tests that mirror markup.

Run `make verify-design-docs`, `make verify-portalkit`, and `make verify-ui-conformance`, plus affected provider checks. Browser evidence must separately cover both themes, desktop/mobile, keyboard/focus, long prose/code/arguments, empty/loading/stale/failed states, and relevant live flows. Static gates cannot prove rendered or backend behavior.

## Source map

The earlier local mockup review covered desktop and mobile themes, activity expansion, approval and failure states, and focus recovery; that evidence verifies the illustrative fixture only. Automated validation passed: the full App Studio runner, Agents' 356 tests plus build/typecheck, UI conformance over 390 files with zero violations and 28 tests, PortalKit parity, and design-doc parity. The App Studio current-host and stale-host-v6 matrices passed across desktop/mobile and light/dark themes, including fallback version 7, message/action/composer/prose rendering, Escape close/focus, selection header, and no-overflow/no-error checks; title-bar Escape uses `rail.close()`. Agents chat and run fixtures each passed 4/4 desktop/mobile theme cases with no errors or overflow; mobile rail open/Escape/select focus passed, and run unknown/error/waiting/success/approval/inspector/four-child and mobile table-scroll cases passed. These are named fixture checks, not backend or live-product evidence. No deployment or live/Tilt evidence is claimed.

The optional Impeccable detector ran in degraded regex mode because its parser dependencies were unavailable. Its font warning conflicts with the explicitly selected canonical Instrument Sans typography, which is intentionally retained. No claim of an automated contrast audit is made.

- [App Studio message/progress rendering](../providers/app-studio/portal/src/App.vue)
- [App Studio action disclosure](../providers/app-studio/portal/src/AssistantActionLog.vue)
- [App Studio rich composer](../providers/app-studio/portal/src/AssistantRichComposer.vue)
- [Canonical conversation rail](../provider-sdk/portalkit-vue/AIConversationRail.vue)
- [App Studio queue](../providers/app-studio/portal/src/AssistantMessageQueue.vue)
- [Agents chat and lifecycle](../providers/agents/portal/src/views/AgentChat.vue)
- [Agents message rendering](../providers/agents/portal/src/views/ChatMessage.vue)
- [Agents run inspection](../providers/agents/portal/src/views/RunDetail.vue)
- [Agents data contracts](../providers/agents/portal/src/types.ts)
- [Agents safe markdown](../providers/agents/portal/src/vue/chat.ts)
- [Agents approval validation](../providers/agents/portal/src/approval-disclosure.ts)
- [Shared AI conversation components](design/components/ai-conversation.md)
- [Canonical tokens](../portal/src/assets/main.css)
- [Conversation authority](design/ai/app-studio-conversation.md), [Agents autonomy](design/ai/agents-autonomy-and-runs.md), and [evidence states](design/ai/evidence-and-status.md)
