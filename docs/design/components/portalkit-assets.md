---
{"schema":1,"id":"design.components.portalkit-assets","title":"Distributed PortalKit asset index","kind":"reference","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The index mirrors the explicit source manifests in hack/sync-portalkit.sh; vendored copies are generated distribution outputs."},"appliesTo":["portalkit","portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/portalkit-assets.md#distributed-portalkit-asset-index","role":"design"},{"path":"hack/sync-portalkit.sh","role":"implementation"},{"path":"provider-sdk/portalkit/README.md","role":"reference"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Final AgentKit and PortalKit sync parity passed; design-doc validation and UI conformance also passed for 429 files with zero violations across 31 focused tests."}]},"relatedDocuments":[]}
---

# Distributed PortalKit asset index

The sync script has explicit vanilla, shared Vue, Vue-toast, and Agents-legacy
distribution groups, plus optional AgentKit consumers. Every distributed file is listed here and has a component
or supporting-contract document. The canonical source is always under
`provider-sdk`; copies under portal `src/portalkit/` and `src/agentkit/` are generated and must
not be edited directly.

## Vanilla TypeScript assets

| Source file | Contract |
|---|---|
| `dashboardtile.ts` | [dashboard and tile support](portalkit-support.md) |
| `faros-ui.css` | [shared recipes](../foundations/recipes.md) |
| `form-select.ts` | [form select](form-select.md) |
| `icons.ts` | [iconography](../foundations/iconography.md) |
| `modal.ts` | [modal and confirmation support](confirm-dialog.md) |
| `resource-table-filter.ts` | [resource table](resource-table.md) |
| `styles.ts` | [style handoff](portalkit-support.md) |
| `tabs.ts` | [provider route tabs](tabs.md) |
| `tenant.ts` | [tenant request contract](portalkit-support.md) |
| `toast.ts` | [legacy toast compatibility](toast.md) |

These files are copied to the Quickstart portal. Vue portals receive only the
shared plain assets named after the Vue table below; framework-neutral
`form-select.ts`, `modal.ts`, and `resource-table-filter.ts` are not copied into
Vue portals because the SFC kit owns those contracts there.

## Vue assets

| Source file | Contract |
|---|---|
| `ActionMenu.vue` | [ActionMenu](action-menu.md) |
| `confirm.ts` | [modal and confirmation support](confirm-dialog.md) |
| `ConditionsPanel.vue` | [conditions panel](conditions-panel.md) |
| `ConfirmDialog.vue` | [modal and confirmation support](confirm-dialog.md) |
| `CreateGuidance.vue` | [creation guidance](create-guidance.md) |
| `FirstRunGuide.vue` | [first-run guide](first-run-guide.md) |
| `FormSelect.vue` | [form select](form-select.md) |
| `LayoutSelector.vue` | [layout selector](layout-selector.md) |
| `layoutPreference.ts` | [layout selector](layout-selector.md) |
| `useAnchoredPopover.ts` | [layout selector](layout-selector.md) |
| `ResourceBackLink.vue` | [resource back link](resource-back-link.md) |
| `ResourcePage.vue` | [resource page](resource-page.md) |
| `ResourceSectionCard.vue` | [resource section card](resource-section-card.md) |
| `ResourceStatCards.vue` | [resource stat cards](resource-stat-cards.md) |
| `ResourceTable.vue` | [resource table](resource-table.md) |
| `ResourceTableFilter.vue` | [resource table](resource-table.md) |
| `table.ts` | [resource table](resource-table.md) |
| `ResourceTableActionButton.vue` | [resource table actions](resource-table-actions.md) |
| `ResourceTableDeleteButton.vue` | [resource table actions](resource-table-actions.md) |
| `ResourceTableEditButton.vue` | [resource table actions](resource-table-actions.md) |
| `InlineNotification.vue` | [toast and contextual notifications](toast.md) |
| `StatusBadge.vue` | [status badge](status-badge.md) |
| `Tabs.vue` | [provider route tabs](tabs.md) |
| `ToastHost.vue` | [toast notifications](toast.md) |
| `toast.ts` | [toast transport](toast.md) |
| `useDelayedLoading.ts` | [PortalKit support contracts](portalkit-support.md) |

The manifest also copies `dashboardtile.ts`, `faros-ui.css`, `icons.ts`,
`page-state.ts`, `styles.ts`, `tabs.ts`, and `tenant.ts` to the root,
Agents, App Studio, Code, Databricks, Edges, Infrastructure, and Kuery Vue
portals. The Vue toast trio is copied to every one of those except Agents;
Agents receives the framework-neutral `toast.ts` compatibility file instead.
Quickstart receives the complete vanilla TypeScript manifest, including that
plain toast bus. Canonical READMEs and tests remain source-only support files
and are not distributed, including `Toast.behavior.test.mjs` and
`Toast.conformance.test.mjs`.

## Optional AgentKit assets

Only Agents and App Studio receive these files under `src/agentkit/`. Vue
components and conversation/model types are canonical in
`provider-sdk/agentkit-vue`; plain styles, activity views, and the style loader
are canonical in `provider-sdk/agentkit`.

| Source file | Contract |
|---|---|
| `AIActionRow.vue` | [AI presentation](ai-conversation.md) |
| `AIActivityDisclosure.vue` | [AI presentation](ai-conversation.md) |
| `AIComposer.vue` | [AI presentation](ai-conversation.md) |
| `AIConversationHeader.vue` | [AI presentation](ai-conversation.md) |
| `AIConversationIdentity.vue` | [AI presentation](ai-conversation.md) |
| `AIConversationLayout.vue` | [AI presentation](ai-conversation.md) |
| `AIConversationRail.vue` | [AI presentation](ai-conversation.md) |
| `AIInterrupt.vue` | [AI presentation](ai-conversation.md) |
| `AIMessage.vue` | [AI presentation](ai-conversation.md) |
| `AIWorkbenchLauncher.vue` | [AI presentation](ai-conversation.md) |
| `AITimestamp.vue` | [AI presentation](ai-conversation.md) |
| `AIPrimaryAction.vue` | [AI presentation](ai-conversation.md) |
| `AITranscript.vue` | [AI presentation](ai-conversation.md) |
| `AIWorkbenchTab.vue` | [AI presentation](ai-conversation.md) |
| `AIWorkbenchTabs.vue` | [AI presentation](ai-conversation.md) |
| `AIPaneDivider.vue` | [AI conversation](ai-conversation.md) |
| `AIWorkspace.vue` | [AI presentation](ai-conversation.md) |
| `ModelConnectionCard.vue` | [model connections](model-connections.md) |
| `ModelUsageSection.vue` | [model connections](model-connections.md) |
| `ModelConnectionForm.vue` | [model connections](model-connections.md) |
| `ModelIDSelector.vue` | [model connections](model-connections.md) |
| `ai.ts` | [AI presentation](ai-conversation.md) |
| `modelIDSelection.ts` | [model connections](model-connections.md) |
| `AIExecutionDetails.vue` | [AI presentation](ai-conversation.md) |
| `AIActivityFeed.vue` | [AI presentation](ai-conversation.md) |
| `AIConversationTurn.vue` | [AI presentation](ai-conversation.md) |
| `AITurnProgress.vue` | [AI presentation](ai-conversation.md) |
| `AIPlanDisclosure.vue` | [AI presentation](ai-conversation.md) |
| `AIPlanSteps.vue` | [AI presentation](ai-conversation.md) |
| `conversation.ts` | [AI presentation](ai-conversation.md) |
| `timestamp.ts` | [AI presentation](ai-conversation.md) |
| `clock.ts` | [AI presentation](ai-conversation.md) |
| `activity.css` | [AI presentation](ai-conversation.md) |
| `activity.ts` | [AI presentation](ai-conversation.md) |
| `agent-ui.css` | [AI presentation](ai-conversation.md) |
| `conversation.css` | [AI presentation](ai-conversation.md) |
| `styles.ts` | [AI presentation](ai-conversation.md) |

See the [AgentKit guide](../../../provider-sdk/agentkit/README.md) for consumer
registration and the canonical-to-vendored core-component import mapping.
