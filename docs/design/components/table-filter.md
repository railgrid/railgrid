---
{"schema":1,"id":"design.components.table-filter","title":"Table filter","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"ResourceTable owns filter state; the shared Vue filter supplies the menu, with a separate vanilla implementation for simple facets."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/table-filter.md#table-filter","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTableFilter.vue","role":"implementation"},{"path":"provider-sdk/portalkit/resource-table-filter.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"providers/agents/portal/src/test/resource-table-filter.test.ts","status":"passing","evidence":"Mounted Vue tests cover opening, keyboard selection, and cleanup."},{"kind":"review","ref":"Cross-product and assistive-technology filter audit","status":"pending"}]},"relatedDocuments":[{"id":"design.components.resource-table","relation":"related"},{"id":"design.components.search-input","relation":"related"},{"id":"design.accessibility.interaction","relation":"related"}]}
---

# Table filter

## Purpose

Narrow a collection by one named facet.

## Use when

Place filters alongside the collection
search and results; keep a selected value visible on the trigger. Use search
for free text and a facet for a defined category such as status or service type.

## Avoid when

The user needs to enter free text or choose a value for a form submission.

## Anatomy and variants

The trigger pairs the facet name with its current value, initially “Any”, and
opens a list of choices. The selected choice remains identifiable by text and
selection state. A searchable Vue facet adds an input and a match count inside
the menu; show a specific no-matches message when the query finds nothing.
Use searchable facets when the option set makes scanning a simple list unwieldy.

## Behavior

Arrow keys move the active option; Enter selects it. Escape closes the menu and
returns focus to its trigger. For a non-searchable filter, Home and End move to the first and last option. In a searchable filter, leave text-editing keys such as Home, End, and modified arrow keys to the browser so users can move the caret and select text. During IME composition, leave candidate navigation and confirmation keys to the input method; do not select a filter option. Tab closes the menu and follows the surrounding controls. The menu positions within
the viewport and may open above the trigger when space below is limited.
Escape leaves the selected value unchanged and does not dismiss an enclosing
dialog; a subsequent Escape can reach that dialog.
Changing a facet resets pagination and table selection; clearing a separate
search query does not clear facets. Client tables derive results locally;
server-paged tables emit the requested filter state to their caller.

The vanilla filter supports simple facets. Searchable resource-reference
facets are currently a Vue-only option. See the [ResourceTable contract](resource-table.md)
for the full state and integration rules.

## Content

Name the facet and its choices in product language; use “Any” for the
unrestricted state and describe empty search results within the menu. In the
searchable Vue filter, show “No matching” only for a nonblank query with no
matches; an empty option set without a query still offers “Any”.

## Layout and responsive behavior

Keep the trigger with the collection controls and the floating menu within the
viewport; allow the toolbar to wrap at narrow widths.
When a filter is inside an open native dialog, its menu is hosted in the dialog
so it remains available in the browser's top layer.

## Accessibility

Expose a named listbox with the selected option and active keyboard option.
Keep the trigger and searchable input named, and restore focus on dismissal.

## Code and evidence

The [table filter review](http://127.0.0.1:5197/?entry=design.components.table-filter)
mounts the same shared filter as the ResourceTable alongside a searchable Edge
facet. Open the menu, use the arrow keys, and press Escape to return to its
trigger. Compare the toolbar in the
[Edge Services product fixture](http://127.0.0.1:5199/product.html).

## Related guidance

See the [search input](search-input.md) and [accessible interaction policy](../accessibility/interaction.md).
