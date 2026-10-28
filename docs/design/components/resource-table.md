---
{"schema":1,"id":"design.components.resource-table","title":"ResourceTable and filtering contract","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"ResourceTable preserves native table semantics, truthful read states, independently configured queryable or explicit simple modes, and opt-in controlled row selection and sorting."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-table.md#resourcetable-and-filtering-contract","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableFilter.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTimestamp.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/timestamp.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/timestampClock.ts","role":"implementation"},{"path":"provider-sdk/portalkit/resource-table-filter.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/table.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTable.selection.test.mjs","role":"reference"},{"path":"provider-sdk/portalkit-vue/ResourceTable.sorting.test.mjs","role":"reference"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"test","ref":"provider-sdk/portalkit-vue/ResourceTimestamp.behavior.test.mjs","status":"pending","evidence":"Timestamp formatting, DST timezone labels, chronological values, and mounted shared-clock lifecycle have not yet been run."},{"kind":"command","ref":"make verify-ui-conformance","status":"passing"},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"passing","evidence":"Organization workspaces bulk-selection and deletion fixtures passed in Chromium light/dark at 1440px and 390px, covering keyboard and checked/mixed states, page/query behavior, confirmation, partial-failure retry, and navigation cancellation. Fixture evidence covers this flow, not every provider consumer."},{"kind":"browser","ref":"2026-10-05 live Tilt Agents table overflow","status":"passing","evidence":"Agents Activity at 390px used a 300px scroll viewport over a 740px table, with visible directional guidance, a named focusable region, and a resolving aria-describedby. The hint changed at the right edge. Evidence: .kcp/agents-design-conformance/access-pass1/tilt-matrix.json."},{"kind":"browser","ref":"Shared inactive filter contrast fixture","status":"passing","evidence":"Chromium 145 rendered the canonical inactive filter at 1280x800 and 390x844 in light and dark with host tokens and fonts. The Any value measured 4.92:1 light and 4.96:1 dark, with no viewport overflow."}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"implements","path":"docs/design/patterns/resource-reads.md"},{"id":"design.patterns.controls","relation":"see-also","path":"docs/design/patterns/controls.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
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
slicing the page; opted-in sortable columns sort that complete filtered set
before pagination. Stable ties retain source order, and missing or invalid
values remain last in both directions. Numbers, dates, and booleans compare
numerically; strings use natural locale collation, and mixed numeric/string
values place numeric values before strings in ascending order. Sort changes
return to page one without clearing selection; filter or page-size changes also
return to page one while polling retains the current valid page. Cursor-backed lists use
`pagination-mode="server"` and controlled `page`, `page-size`, `query`,
`filter-values`, `cursor`, `page-info`, and optionally `sort`; the typed `change`
event drives the fetch. Server mode renders supplied rows as-is, never applies
local sorting, filtering, or slicing, and exposes only backend-returned
next-page state. A sort action emits `update:sort` and a `change` snapshot with
reason `sort`, page one, and a null cursor so the caller can request the new
server order.

Sorting is opt-in per column with `sortable: true`. A column may provide
`sortValue(row)` when its raw value is not the desired comparison value, such as
a parsed timestamp. `sortDefaultDirection` chooses the direction when the user
first selects that column; it defaults to ascending. `defaultSort` seeds an
uncontrolled table. Supplying `sort` makes the state controlled, including when
the value is `null`; bind `update:sort` to apply changes. Selecting the active
column toggles between ascending and descending, with no unsorted third step.
When sorting is configured, all table change snapshots include the current
sort. `ResourceTableChange.sort` is optional so existing consumers that do not
configure sorting keep their prior payload shape. Cursor pager helpers preserve
the optional sort across query/filter changes and reset page/cursor history when
the sort itself changes.

Timestamp cells use the shared Vue `ResourceTimestamp` component with an absolute
RFC3339 timestamp (including a timezone). Invalid calendar dates and timestamps
without a timezone are unavailable, rather than guessed or normalized. It presents
relative time while retaining the source instant in a semantic `<time>`, and
shows the caller-selected localized date and time, including seconds and time
zone, through `ResourceTableActionTooltip` on hover or focus. Clicking toggles
that full value inline. Missing or invalid values render as an em dash without
a button. `timestampSortValue(value)` returns epoch milliseconds or `null` for
missing or invalid values; use it as a sortable column's `sortValue` so rows
sort by instant and the table's existing missing-value rule keeps invalid dates
last in either direction. Mounted timestamp cells share one clock that stops
when the last consumer unmounts; the server render does not start a timer.

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
stack full-width. Streams prefer Load more or infinite scroll instead of forced
page navigation.

Long content truncates; it does not widen the table. The primary cell removes
its content from the table's intrinsic width (`width: 100%; max-width: 0`) and
ellipsizes at whatever width the other columns leave it, never less than its
`min-width: 14rem` floor. A secondary cell's
value sits in `.k-table__cell-value`, which ellipsizes at
`--k-table-cell-max-width` (32ch unless the column sets `maxWidth`). A table
therefore fits its card at desktop widths however long a name, message or
identifier is.

Horizontal scrolling is the fallback for when the capped columns alone no
longer fit, typically many columns on a phone. Then only the table canvas
scrolls; controls and footer remain pinned to the card width, and a visible
continuation hint is tied to the named, keyboard-focusable scroll region with
`aria-describedby`. Observe resize and content changes so the hint disappears
when all columns fit; at the right edge, explain how to return to earlier
columns. The viewport itself must not overflow the page.

## Accessibility

The table retains native `<table>` and `<tr>` semantics. Interactive rows use
`tabindex="0"` and Enter/Space emits `rowClick`; nested links, buttons, inputs,
selects, summaries, and other explicit controls remain independent. Resource
names disclose full values in a viewport overlay only when actually truncated,
using `fullValue(row)` when a slot label differs. A truncated secondary cell
discloses the same way: the column's `fullValue`, else the row's scalar, else
the text its slot rendered. Cells are never tab stops: a focused row steps
the disclosure through its truncated cells with the Left and Right arrow keys
(the keys keep their default behaviour when nothing is truncated), so a long
table gains no stops and a click or Enter on a cell still opens the row. A
control focused inside a secondary cell discloses the cell and keeps its
focus ring unclipped. Any column
may set `maxWidth` (a CSS length) to move its cap: tighter than the remaining
width for the primary column, or wider or narrower than 32ch for a secondary
one whose values deserve it. Shared icon-only action buttons
use `ResourceTableActionTooltip` on hover and focus: a body-teleported,
viewport-clamped visual tooltip, with the button's full accessible label retained.
They never add a duplicate native title or clipped `data-k-tip`. Empty/error
behavior, nested-control isolation, and row-action accessibility are identical
between Queryable and Simple. See the [accessible interaction policy](../accessibility/interaction.md).

Sortable headers keep the native `<th scope="col">` and contain a real button.
Only the active header exposes `aria-sort="ascending"` or `"descending"`.
Each button's accessible name describes the direction its next activation will
apply, and its Lucide icon is decorative.

`ResourceTimestamp` keeps `<time datetime="…">` semantics inside a keyboard
button. Its accessible name includes the caller's label, relative value, and
exact localized date and time. Hover or focus shows the caller's label and that
exact value in the shared viewport-clamped tooltip; Enter, Space, or click
toggles the exact value inline, and the click is isolated from row activation.

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
`provider-sdk/portalkit-vue/ResourceTimestamp.vue`,
`provider-sdk/portalkit-vue/timestamp.ts`,
`provider-sdk/portalkit-vue/timestampClock.ts`,
`provider-sdk/portalkit-vue/table.ts` (including the focused
`ResourceTable.selection.test.mjs` and `ResourceTable.sorting.test.mjs`
contracts, plus `ResourceTimestamp.behavior.test.mjs` for timestamp formatting
and mounted-clock behavior), and
`provider-sdk/portalkit/railgrid-ui.css`. Verify with `make verify-portalkit` and
`make verify-ui-conformance`.

## Related guidance

Follow the [resource reads pattern](../patterns/resource-reads.md) for loading
and refresh authority, the [controls pattern](../patterns/controls.md) for
facets, the [accessible interaction policy](../accessibility/interaction.md),
and the [review checklist](../quality/review-checklist.md).
