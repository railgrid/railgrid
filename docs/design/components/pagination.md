---
{"schema":1,"id":"design.components.pagination","title":"Pagination","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/pagination.md","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"hack/design-review/pagination-focus.test.ts","status":"passing","evidence":"Mounted client paging checks focus and inert activation at both boundaries."},{"kind":"browser","ref":"Pagination review fixture","status":"passing","evidence":"Client and cursor sample paging retained focus on Next at the last page; no assistive-technology or live-backend audit."},{"kind":"review","ref":"Product-wide pagination adoption and accessibility audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Pagination

## Purpose

Move through a collection in bounded pages.

## Use when

A collection is too large to display as one practical, bounded list and its data source supports paging.

## Avoid when

A small bounded list is easier to scan without paging, or the data source cannot support the requested navigation.

## Anatomy and variants

1. **Range:** the rows currently shown and an exact total only when known.
2. **Page size:** a labeled selector for rows per page when supported.
3. **Navigation:** named Previous and Next buttons with the current page
   indicator between them.

The shared table supports client paging and cursor-based server paging; the
footer is part of the collection, not a separate navigation bar.

## Behavior

Preserve query and filters while navigating. Page buttons remain focusable at
the boundary with `aria-disabled` so focus does not disappear; their handlers
ignore activation there. Changing page size returns to the first page and
resets cursor history. Cursor mode must not imply a known total or arbitrary
page access; the caller owns server requests and their failure state.

For the shared table's server mode, Next requires a non-null `nextCursor` and
`hasNext` must not be false (when omitted, it is inferred from that cursor).
Previous requires a cursor recorded for the prior
page. A caller restoring a later page without that history cannot navigate back
through the table. Pass an exact `total` only when the server supplies one.

An empty page alone does not establish an empty inventory: it may be a later
page, a filtered result, or an incomplete response. Before showing a first-use
guide for a cursor-backed collection, require a settled, current snapshot with
no active query or filters and no rows. `isCompleteFirstCursorPage` additionally
requires page one, no input cursor, explicit `hasNext: false`, and no next
cursor. A missing `hasNext` is not proof of completeness, even though the
table can infer whether Next is available from `nextCursor`. Callers must also
ensure the result covers the whole inventory relevant to the guide.

The table emits change snapshots; it does not fetch pages or discard stale
responses. Callers must associate each response with its request and scope and
ignore it if a newer request or scope change superseded it. This includes errors
and loading state as well as rows and page metadata, so a late response cannot
replace the current page or present the wrong empty state.

The displayed server range assumes every preceding page contained the selected
page size: page two at a size of 10 starts at 11, regardless of how many rows
page one returned. Use this footer only when the source or an adapter can
fulfill that assumption. A cursor API that can return short intermediate pages
needs a paging presentation based on its actual positions; otherwise the range
and, when a total is present, the page count can misrepresent the collection.

An empty server page with a positive total, or without an authoritative empty
first-page result, is labeled **No results on this page** in the body and
footer. A known total remains visible in the footer. A zero total or an
explicitly complete empty first page retains the caller's empty or no-match
copy. Use `resultsUnavailable` only when the current query has no usable
result, not to disguise a valid empty page.

## Content

Use **Rows per page**, **Previous page**, and **Next page** for the shared
controls. Show **Showing 1–10 of 42** only with an exact total; omit the total
when the server does not provide one. Avoid presenting an estimated remaining
count as a total.

## Layout and responsive behavior

Keep range, page size, and navigation in one compact footer. Allow them to wrap
without clipping the selector or controls; do not make users scroll the whole
page horizontally to reach them. Review both themes and the 390px frame.

## Accessibility

Name navigation and page-size controls, preserve focus, and announce resulting
range and page changes politely. Use `aria-disabled` only with a guarded
handler; it does not block activation by itself. Keep touch targets usable on
coarse pointers.

## Code and evidence

The [ResourceTable review](http://127.0.0.1:5197/?entry=design.components.pagination)
starts with twelve sample edges. Try the page-size selector, move to the last
page, and use Previous to go back. In cursor mode, a page with no rows and a
known nonzero total keeps the total and reports the page as empty.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
