---
{"schema":1,"id":"design.components.portalkit-assets","title":"Distributed PortalKit asset index","kind":"reference","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The index mirrors the explicit source manifests in hack/sync-portalkit.sh; vendored copies are generated distribution outputs."},"appliesTo":["portalkit","portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/portalkit-assets.md#distributed-portalkit-asset-index","role":"design"},{"path":"hack/sync-portalkit.sh","role":"implementation"},{"path":"provider-sdk/portalkit/README.md","role":"reference"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copies and explicit source manifests passed; this fully covers the asset-index contract."}]},"relatedDocuments":[]}
---

# Distributed PortalKit asset index

The sync script has explicit vanilla, shared Vue, Vue-toast, and Agents-legacy
distribution groups. Every distributed file is listed here and has a component
or supporting-contract document. The canonical source is always under
`provider-sdk`; copies under portal `src/portalkit/` are generated and must
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
