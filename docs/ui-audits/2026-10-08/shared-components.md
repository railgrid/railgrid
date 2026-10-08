# Shared provider UI component audit

The canonical source review covers all 22 PortalKit Vue components and all 27 AgentKit Vue components. Each enabled provider receives synchronized outputs; vendored files are not separate implementations. These rows record source review and specific fixes. Mounted tests and live browser coverage are recorded in the main audit; unchanged rows do not claim every runtime branch was exercised.

## PortalKit

| Canonical component | Review and result |
| --- | --- |
| `ActionMenu.vue` | Named trigger, roving menu keys, disabled/busy items, anchored geometry and focus return. Shared anchored popovers now close when a kept-alive pane deactivates. |
| `ConditionsPanel.vue` | Typed condition/status/reason/message table and reconciliation warning. Transition timestamps now use the mono data role. A named parent can set showTitle=false to avoid repeating Conditions. Controller Message is the primary column so prose receives the available width. |
| `ConfirmDialog.vue` | Labelled protected dialog, initial focus, focus containment, Escape and explicit Cancel/confirm consequences retained. |
| `CreateGuidance.vue` | Secondary prerequisite guidance and links inside the shared creation hierarchy retained. |
| `FirstRunGuide.vue` | One task-facing primary action, ordered first-use journey, optional prerequisites and semantic loading/errors retained. |
| `FormSelect.vue` | Persistent field name, native combobox/listbox semantics, disabled choices, arrows/typeahead and focus return retained. Closes on kept-alive pane deactivation. |
| `InlineNotification.vue` | Tone-specific status/alert channels, named busy recovery and dismiss controls retained. |
| `LayoutSelector.vue` | Named view control, persisted view preference, menu keys and existing deactivation cleanup retained. |
| `ResourceBackLink.vue` | Real href/navigation and direction-aware arrow retained. Canonical back arrows are fixed at the design book’s 14px size. |
| `ResourceBoard.vue` | Semantic configurable columns and native resource/action controls retained; bounded provider content remains owned by callers. |
| `ResourcePage.vue` | Header/actions, stat/section slots, retained snapshots, stale/initial errors and foreground recovery retained. Shared title uses Archivo at 125%. Background failure leaves Retry idle; a queued user retry announces immediately before caller acknowledgement. |
| `ResourceSectionCard.vue` | Independent owned-section title/actions/body hierarchy, raised 6px panel and container-responsive layout retained. |
| `ResourceStatCards.vue` | Labelled numeric summary, semantic tone and responsive tracks retained; display/mono roles remain distinct. |
| `ResourceTable.vue` | Semantic resource table, client/server filtering and cursor paging, named rows, keyboard activation, selection and overflow disclosure reviewed. Empty client toolbar no longer appears during background refresh; retries retain error context, latch immediately, prevent duplicate activation and announce foreground progress. Empty/no-match changes announce politely. |
| `ResourceTableActionButton.vue` | Native named action, disabled/busy and tooltip integration retained. |
| `ResourceTableActionTooltip.vue` | Bounded visible tooltip and accessible description without making tooltip text the only name retained. |
| `ResourceTableDeleteButton.vue` | Specific destructive resource action and confirmation ownership retained. |
| `ResourceTableEditButton.vue` | Specific named edit action and navigation ownership retained. |
| `ResourceTableFilter.vue` | Searchable listbox and exact facet choice reviewed. Tab now returns to its source trigger before native tab order; popup closes on deactivation. Search and options have 44px coarse/hybrid targets. |
| `StatusBadge.vue` | Text-supported semantic tone, unknown/fallback status and compact 3px badge geometry retained. |
| `Tabs.vue` | Named section navigation, native button selection, aria-current, disabled choices and narrow scroll rail retained; callers own route or panel state. |
| `ToastHost.vue` | Shared live channels, bounded queue and dismissal/timer behavior retained. |

## AgentKit

| Canonical component | Review and result |
| --- | --- |
| `AIActionRow.vue` | Named execution state and bounded expandable action details retained. |
| `AIActivityDisclosure.vue` | Keyboard disclosure, summary, progress and grouped activity retained. |
| `AIActivityFeed.vue` | Readable chronological action/state feed and announcement boundaries retained. |
| `AIComposer.vue` | Labelled editor, submit/cancel state, disabled/busy affordances and attachment slots retained. |
| `AIConversationHeader.vue` | Identity/context/actions hierarchy and narrow wrapping retained. |
| `AIConversationIdentity.vue` | Readable human name and technical identity roles retained. |
| `AIConversationLayout.vue` | Parent-owned fluid conversation/workbench regions and responsive minimums retained. |
| `AIConversationRail.vue` | Session navigation, named controls, mobile disclosure and keyboard sizing retained. |
| `AIConversationTurn.vue` | Speaker/turn grouping and pending/completed presentation retained. |
| `AIExecutionDetails.vue` | Bounded command/output/arguments disclosure and safe technical presentation retained. |
| `AIInterrupt.vue` | Approval/clarification boundary, visible action names and semantic state retained. |
| `AIMessage.vue` | User/assistant content grouping, markdown and bounded detail slots retained. |
| `AIPaneDivider.vue` | Named separator, keyboard and pointer resizing, bounds and reduced-motion behavior retained. |
| `AIPlanDisclosure.vue` | History disclosure and plan summary retained. |
| `AIPlanSteps.vue` | Ordered plan states with textual support retained. |
| `AIPrimaryAction.vue` | Named primary action, disabled/busy state and shared button hierarchy retained. |
| `AITimestamp.vue` | Machine-readable time and human-readable mono timestamp retained. |
| `AITranscript.vue` | Conversation log, scroll/follow state and explicit jump controls retained. |
| `AITurnProgress.vue` | Meaningful ongoing/terminal turn progress retained. |
| `AIWorkbenchLauncher.vue` | Searchable labelled workbench choices and keyboard selection retained. Live phone inspection found a 36px search field; its coarse/hybrid height is now 44px. |
| `AIWorkbenchTab.vue` | Named tab/close and active/disabled state retained. |
| `AIWorkbenchTabs.vue` | Roving tabs, reorder interaction and narrow scroll treatment retained. |
| `AIWorkspace.vue` | Fluid split, clamped pointer/keyboard resizing, mobile sheet, inert/focus-return behavior retained. |
| `ModelConnectionCard.vue` | Independent compact saved-model boundary. Fixed ground-colored background to raised surface in both themes; preserved hairline/6px radius, text-supported state, private credential summary and resource-owned actions. Decorative icon now hidden from assistive technology. |
| `ModelConnectionForm.vue` | One canonical name/connection/credential/model surface, short persistent labels, linked help/errors, busy state, provider slots and Cancel/test/save order retained. Shared create typography, back arrow and heading focus now match the documented contract. |
| `ModelIDSelector.vue` | Search/manual ID choice, grouped suitability, disabled choices and keyboard selection retained. Tab returns to the source trigger; popup closes when its kept-alive pane deactivates. |
| `ModelUsageSection.vue` | One raised section with unavailable/incomplete coverage, cost/token metrics and bounded detail disclosure retained. |

## Plain TypeScript controls and visual recipes

The canonical vanilla `modal.ts`, `form-select.ts`, `resource-table-filter.ts`, `tabs.ts`, `toast.ts`, and `icons.ts` were reviewed for naming, keyboard/focus, dismissal, native interaction and token parity. Dashboard polling/read-state helpers were reviewed for useful snapshots and authority boundaries. CSS recipes were reviewed against color, geometry, typography and coarse/hybrid pointer rules.

The table touch rules now include `any-pointer: coarse`; filter search and options use 44px minimum targets. Creation and resource page titles use Archivo at 125%. Dashboard secondary copy uses the full muted token rather than reducing its opacity. Core CSS and loader markers move together from 31 to 32; AgentKit moves from 9 to 10. Existing versioned style nodes remain immutable.

The enabled App Studio skill switch places a decorative 40×22 track inside a native 44×44 switch button, so its hit area already meets the contract without changing track geometry. Kuery’s relation palette keeps the existing documented graph exception and its assistive-technology validation boundary.
