---
{"schema":1,"id":"design.components.resource-table","title":"ResourceTable and filtering contract","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"ResourceTable preserves native table semantics, truthful read states, and independently configured queryable or explicit simple modes."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-table.md#resourcetable-and-filtering-contract","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableFilter.vue","role":"implementation"},{"path":"provider-sdk/portalkit/resource-table-filter.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/table.ts","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"command","ref":"make verify-ui-conformance","status":"passing"},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"implements","path":"docs/design/patterns/resource-reads.md"},{"id":"design.patterns.controls","relation":"see-also","path":"docs/design/patterns/controls.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# ResourceTable and filtering contract

## Purpose

`ResourceTable` presents resource collections while preserving native table
semantics, truthful read states, and independently configured queryable or
explicit simple modes.

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

## Content

Search and compact labeled facets sit above the table. Categorical filters use
the shared select-only listbox; resource-reference inventories explicitly opt
into search in that menu. `Clear filters` appears only while a facet is active.
Vue portals use `ResourceTableFilter.vue`; the Quickstart portal uses the
framework-neutral `faros-resource-table-filter`, which accepts `label`,
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
using `fullValue(row)` when a slot label differs. Icon-only actions use
`data-k-tip` on hover and focus, never a duplicate native title. Empty/error
behavior, nested-control isolation, and row-action accessibility are identical
between Queryable and Simple. See the [accessible interaction policy](../accessibility/interaction.md).

## Code and evidence

Canonical implementations are `provider-sdk/portalkit-vue/ResourceTable.vue`,
`provider-sdk/portalkit-vue/ResourceTableFilter.vue`,
`provider-sdk/portalkit/resource-table-filter.ts`,
`provider-sdk/portalkit-vue/table.ts`, and
`provider-sdk/portalkit/faros-ui.css`. Verify with `make verify-portalkit` and
`make verify-ui-conformance`.

## Related guidance

Follow the [resource reads pattern](../patterns/resource-reads.md) for loading
and refresh authority, the [controls pattern](../patterns/controls.md) for
facets, the [accessible interaction policy](../accessibility/interaction.md),
and the [review checklist](../quality/review-checklist.md).
