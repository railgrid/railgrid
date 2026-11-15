---
{"schema":1,"id":"design.components.search-input","title":"Search input","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"The cited implementation is evidence for this review; product-wide adoption has not been audited."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/search-input.md","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"hack/design-review/table-search-state.test.ts","status":"passing","evidence":"Mounted search clear retains focus, an active facet, stale-result warning, and retry after a failed refresh."},{"kind":"review","ref":"Product-wide search and assistive-technology audit","status":"pending"}]},"relatedDocuments":[{"id":"design.accessibility.interaction","relation":"see-also"},{"id":"design.foundations.recipes","relation":"see-also"}]}
---

# Search input

## Purpose

Narrow a collection by a user-entered query.

## Use when

People need to find items within a named collection. Preserve the distinction between the collection being empty, a query with no matches, and a read failure.

## Avoid when

People are entering a durable value for submission: use a text input. Search does not replace structured filters where those help.

## Anatomy and variants

The queryable ResourceTable offers a labeled search field with a leading
decorative icon and a clear button when the query is nonempty. Review empty
and populated queries, no matches, and the initial-loading skeleton. The
disabled example in the design book is a study: the shared table does not
currently expose a disabled-search prop.

## Behavior

Client-side filtering waits 100ms after the latest query or filter change before
scanning rows; the input itself updates immediately. Server pagination passes
query changes to the caller, which owns the request and results. A changed
query resets the page and clears table selection so an action cannot silently
apply to hidden rows. Clearing search restores focus to the field and removes
only the query; active facet filters remain.

For client tables, `searchKeys` selects top-level row properties to search. If
it is omitted or empty, the table uses the declared column keys except
`actions`; if that leaves no keys, the search helper falls back to all row
properties. The helper recursively collects string, number, and boolean values
from arrays and objects, trims the query, and matches it case-insensitively as
a substring. It searches raw row data, not rendered cell text or formatted
labels. Supply explicit keys and searchable display values when the default
would include unrelated nested data or omit a label users expect to find. Do
not put secrets in table rows on the assumption that hiding a column excludes
them from search. Server-paged callers own their search semantics; the table
does not apply these client rules to server results.

Keep no matches distinct from an empty collection or failed read. During the
first read the current table shows a search-shaped skeleton, not a disabled
input. After a successful read, a background refresh keeps the useful snapshot
visible; a failed refresh reports the stale result and offers retry rather than
claiming there are no matches.

## Content

Name the collection in the accessible label and use a short, task-specific
placeholder such as “Search services…”. No-results copy describes the search
or filters; it does not suggest creating the first resource. Keep retry and
failure messages specific to the read operation.

## Layout and responsive behavior

Place search with its collection's filters and results. On narrow widths it fills
the available toolbar row, and filters wrap without widening the page. Review
the example in the 390 px frame and in both themes.

## Accessibility

The input is a native search field named for its collection; the clear button
has its own accessible name and returns focus to the input. Loading and empty
results use the table's live status and error semantics. Keep the clear action
keyboard-operable and large enough for coarse pointers.

## Code and evidence

The [shared ResourceTable](../../../provider-sdk/portalkit-vue/ResourceTable.vue)
implements this collection search; this is not a product-wide adoption audit.
Its key selection and recursive matching are implemented in
[`table.ts`](../../../provider-sdk/portalkit-vue/table.ts).
Compare states in the [design book](http://127.0.0.1:5197/?entry=design.components.search-input)
and open the [Edge Services product fixture](http://127.0.0.1:5199/product.html) for context.
Both use sample data.

## Related guidance

See [shared recipes](../implementation/recipes.md) and the [interaction policy](../accessibility/interaction.md).
