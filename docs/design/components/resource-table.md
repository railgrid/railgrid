---
{"schema":1,"id":"design.components.resource-table","title":"ResourceTable and filtering contract","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"ResourceTable preserves native table semantics, truthful read states, independently configured queryable or explicit simple modes, and opt-in controlled row selection."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-table.md#resourcetable-and-filtering-contract","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableFilter.vue","role":"implementation"},{"path":"provider-sdk/portalkit/resource-table-filter.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/table.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTable.selection.test.mjs","role":"reference"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"command","ref":"make verify-ui-conformance","status":"passing"},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"passing","evidence":"Organization workspaces bulk-selection and deletion fixtures passed in Chromium light/dark at 1440px and 390px, covering keyboard and checked/mixed states, page/query behavior, confirmation, partial-failure retry, and navigation cancellation. Fixture evidence covers this flow, not every provider consumer."}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"implements","path":"docs/design/patterns/resource-reads.md"},{"id":"design.patterns.controls","relation":"see-also","path":"docs/design/patterns/controls.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# ResourceTable and filtering contract

## Purpose

`ResourceTable` presents resource collections while preserving native table
semantics, truthful read states, and independently configured queryable or
explicit simple modes. It can opt into controlled, stable-key row selection
without coupling selection to resource navigation or page state.

## Use when

Use `.k-table` for resource collections. Existing tables remain Queryable until
an explicit review opts them into Simple. Use Simple only for a short bounded
contextual list; use Queryable for collections that need search, filters, or
pagination.

## Avoid when

Do not give a row `role="button"`; retain native `<table>` and `<tr>`
semantics. Do not infer an exact total from a cursor remaining-item count or
force streams into page navigation. Do not apply local slicing in server mode.

## Anatomy and variants

The table uses mono-uppercase headers, 13px secondary-text cells, and
`.k-cell-mono` for identifiers. The primary column takes remaining width and
its right edge owns compact row actions. A column marked `primary: true` wins;
otherwise `name`, then the first non-action column, is primary.

There are exactly two configurations:

- **Queryable (default):** search, filters, and client/server pagination remain
  independently configured. Configured controls have matching first-load
  skeletons; filters apply to the authoritative result set; query, filter, and
  page-size changes reset page one. Existing tables remain Queryable until an
  explicit review opts them into Simple.
- **Simple (`variant="simple"`):** a short bounded contextual list with no
  search, filters, pagination, controlled query, filter values, page, cursor, or
  page metadata. Loading begins with the table skeleton. Empty/error behavior,
  native semantics, nested-control isolation, and row-action accessibility are
  identical to Queryable.

## Behavior

Interactive row hover uses a 4% accent tint with primary text. Interactive rows
use `tabindex="0"` and Enter/Space emits `rowClick`; nested links, buttons,
inputs, selects, summaries, and other explicit controls remain independent.

Client pagination searches and filters the complete loaded row set before
slicing the page; filter or page-size changes return to page one while polling
retains the current valid page. Cursor-backed lists use
`pagination-mode="server"` and controlled `page`, `page-size`, `query`,
`filter-values`, `cursor`, and `page-info`; the typed `change` event drives the
fetch. Server mode renders supplied rows as-is, never applies local slicing, and
exposes only backend-returned next-page state.

Row selection is an explicit `selectable` opt-in. Bind `selected-keys` as the
controlled `string | number` keys for selected resources and handle
`update:selected-keys`. A stable `row-key` is required for selectable rows; the
default stable fields are `name`, `id`, and `uid`. Rows without a stable key and
duplicate keys in the supplied result cannot be selected. A `row-key` function
retains its existing `(row, index)` signature for row rendering. Selection
passes the matching index from the source `rows` array, not the locally filtered
page. Its result must remain stable across server page changes and derive
identity from resource data, independent of the index. `selection-label(row)`
can provide a complete checkbox name.

The header uses a native mixed-state checkbox to select eligible rows on the
currently rendered page: the locally filtered page in client mode or the
supplied page in server mode. It never selects all search matches. Selection
survives page and page-size changes, and a query or filter value change clears
it. The selection toolbar replaces the filters and shows the total controlled key count, including
keys preserved from other server pages; its `selection-actions` slot receives
`selectedKeys`, `keys` (an alias), and `count`. The built-in Clear selection
button clears the complete selection.

In client mode, selected keys are pruned against the full supplied `rows` set
only after `loaded` is true and the read is settled without an error or stale
result. In server mode, absence from the current page never prunes a key.
`selection-disabled` disables row/header checkboxes and Clear selection while
the caller reports a busy or unverified state; it does not itself prune keys.
`selection-clear-disabled` can override only Clear selection, allowing users to
leave an unverified selection after a read failure while mutations remain
disabled. Keep this override disabled while an operation is running.
`row-selectable(row)` and a non-empty
`row-selection-disabled-reason(row)` make a row ineligible. Ineligible rows
expose a keyboard-focusable help control with the reason, since a disabled
checkbox cannot receive keyboard focus.

Consumers own the available bulk actions, permissions, confirmation, request
execution, and per-resource outcomes. Clear selection when the organization or
resource scope changes, and revalidate targets before sending requests. In server
mode, consumers also reconcile off-page selections against authoritative data;
the table cannot infer deletion from absence on the current page.

Workspace settings use this same selection contract for service-account
deletion, workspace-member removal, and app-access grant revocation. Confirm
the selected resources and workspace before dispatching. Disable conflicting
row actions during a batch, keep failed resources selected for retry, and
refresh the affected inventory after completion. Navigation or lost authority
must stop further requests and discard feedback from the previous context.
Bulk member removal excludes the signed-in user; leaving a workspace remains
an individual action.

Organization member tables use the same bulk-removal flow. Their confirmation
also explains that removal revokes membership in every child workspace. Keep
self-removal separate from the bulk action. A failed cascade can leave workspace
access cleanup unfinished after the member disappears from the roster. Keep
these failed removal targets available through an explicit retry action,
independent of table selection, until cleanup succeeds or the scope changes.
Retry must confirm the targets again and revalidate the current authority.

A successfully loaded empty inventory with no selection omits column headers,
the selection column, and any otherwise empty toolbar space. Its message wraps
within the available width. Keep search and filter controls
when they are needed to recover from zero matching results. An empty server
page must retain the selection toolbar when resources on another page remain
selected.

## Content

Search and compact labeled facets sit above the table. Categorical filters use
the shared select-only listbox; resource-reference inventories explicitly opt
into search in that menu. `Clear filters` appears only while a facet is active.
Vue portals use `ResourceTableFilter.vue`; the Quickstart portal uses the
framework-neutral `railgrid-resource-table-filter`, which accepts `label`,
`allLabel`, `options`, and `value` and emits the same string-valued bubbling
`change` contract. Searchable resource-reference facets remain a Vue opt-in.
The compact pagination footer uses 4px ghost icon buttons for previous/next and
a mono tabular range label such as `12–24 of 96` in muted text. A current page
indicator uses accent-subtle background and accent text; avoid number soup.

## Layout and responsive behavior

The primary column takes remaining width, with compact row actions at its right
edge so operations remain reachable without horizontal scrolling. Narrow facets
stack full-width. For wide tables only the table canvas scrolls; controls and
footer remain pinned to the card width. Streams prefer Load more or infinite
scroll instead of forced page navigation.

## Accessibility

The table retains native `<table>` and `<tr>` semantics. Interactive rows use
`tabindex="0"` and Enter/Space emits `rowClick`; nested links, buttons, inputs,
selects, summaries, and other explicit controls remain independent. Resource
names disclose full values in a viewport overlay only when actually truncated,
using `fullValue(row)` when a slot label differs. Shared icon-only action buttons
use `ResourceTableActionTooltip` on hover and focus: a body-teleported,
viewport-clamped visual tooltip, with the button's full accessible label retained.
They never add a duplicate native title or clipped `data-k-tip`. Empty/error
behavior, nested-control isolation, and row-action accessibility are identical
between Queryable and Simple. See the [accessible interaction policy](../accessibility/interaction.md).

When enabled, selection keeps a native checkbox column before the primary
column. On mouse-driven desktops, row checkboxes and their help controls appear
on row hover or keyboard focus. Once any resource is selected, every row's
selection controls stay visible, including across pages. The header checkbox
is always visible; touch and hybrid devices keep row controls visible as well.
Hidden idle controls retain their space and keyboard focusability.
Header state uses the input's checked and indeterminate properties,
and every row checkbox has a resource-specific accessible name. The live count
remains mounted while selectable so clearing the selection is announced.
Disabled row explanations are available from a focusable help button and its
tooltip. These explanations use the body-teleported, viewport-clamped table
tooltip so the scroll container cannot clip them. Global selection disablement
applies to all selection controls.

For selectable tables, the search/filter toolbar and selection actions share a
reserved toolbar row. Selecting resources replaces the visible search and
filter controls with a compact selection count and actions; clearing the
selection restores the prior search and filter controls. The count and Clear
selection button stay together in a grid track, so changing the count does not
wrap the button or change the reserved row height. On narrow layouts, selection
actions use their own row. The inactive toolbar remains hidden and inert while
the row keeps the taller panel's space, including when mobile filters wrap.
At the narrowest widths, the count and Clear selection also use separate rows
for every selection count, preserving space for the button and its focus ring.
After Clear selection, focus returns to the header checkbox when it is fully
visible in both the table scroll area and the viewport. If it is clipped or
disabled, focus moves to the scroll area's visible focus outline. When the
whole table is outside the viewport, native focus scrolling reveals the header
checkbox, or the table region when the checkbox is disabled.

## Code and evidence

Canonical implementations are `provider-sdk/portalkit-vue/ResourceTable.vue`,
`provider-sdk/portalkit-vue/ResourceTableFilter.vue`,
`provider-sdk/portalkit/resource-table-filter.ts`,
`provider-sdk/portalkit-vue/table.ts` (including the focused
`ResourceTable.selection.test.mjs` contract), and
`provider-sdk/portalkit/railgrid-ui.css`. Verify with `make verify-portalkit` and
`make verify-ui-conformance`.

## Related guidance

Follow the [resource reads pattern](../patterns/resource-reads.md) for loading
and refresh authority, the [controls pattern](../patterns/controls.md) for
facets, the [accessible interaction policy](../accessibility/interaction.md),
and the [review checklist](../quality/review-checklist.md).
