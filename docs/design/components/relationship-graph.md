---
{"schema":1,"id":"design.components.relationship-graph","title":"Relationship graph","kind":"component","status":"draft","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"partial","notes":"Kuery has product-specific Impact and Topology graphs; this is not a shared PortalKit graph or a product-wide adoption audit."},"appliesTo":["provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/relationship-graph.md#relationship-graph","role":"design"},{"path":"providers/kuery/portal/src/graph.ts","role":"implementation"},{"path":"providers/kuery/portal/src/components/ImpactView.vue","role":"implementation"},{"path":"providers/kuery/portal/src/components/TopologyView.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"node --test topology.contract.test.mjs in providers/kuery/portal","status":"passing","evidence":"Checks relation metadata and a 3:1 line-to-surface contrast floor in both current themes; this does not establish categorical distinguishability."},{"kind":"browser","ref":"Kuery Impact synthetic fixture","status":"passing","evidence":"Graph and list rendered with synthetic relationships; list inspection was activated by keyboard at 390px in dark theme. No live backend or screen-reader verification."},{"kind":"review","ref":"Graph color-blindness and assistive-technology review","status":"pending"}]},"relatedDocuments":[{"id":"design.quality.known-oddities","relation":"see-also"},{"id":"design.patterns.resource-reads","relation":"see-also"},{"id":"design.accessibility.interaction","relation":"see-also"}]}
---

# Relationship graph

## Purpose

Show declared relationships among resources and help an operator inspect their context and possible impact.

## Use when

Relationships and direction are central to the task, such as understanding what a resource depends on or may affect. Offer a corresponding list for inspection.

## Avoid when

A table or list communicates the task more directly, or the data cannot support a truthful relationship or impact claim. A graph is not evidence of an exhaustive traversal.

## Anatomy and variants

Kuery's Impact view combines a named anchor, Graph and List controls, a relation legend, a bounded-analysis explanation, and a graph or grouped list. The graph uses labeled edges with theme-specific colors and line styles. Topology is a separate fleet view with its own hierarchy, filters, and canvas controls; it is not the same component.

The Impact graph and legend select relation colors from the rendered document
theme, so their palettes remain aligned even if the provider context reports a
different theme. A mounted test covers that mismatch and a theme change; it is
not browser or assistive-technology evidence.

## Behavior

On initial mount, Impact opens the grouped List at widths of 600px or less, where fitted graph labels can be difficult to read; wider screens open Graph. Users can switch views, and resizing does not override their choice. Graph-specific guidance and its legend appear with Graph, while bounds and warnings remain visible in both views. In Impact, selecting a related graph node or list item requests inspection of that resource. Graph and list represent the same declared results. Visible zoom-in, zoom-out, and fit controls let users adjust the graph without relying on a gesture. Keep the last successful result through a background failure and provide recovery. When the SavedView changes, clear the previous analysis and ignore late responses from it; results belong to the view that requested them. State traversal limits and reported truncation separately; do not imply missing relations were checked.

For query failures, lead with the affected task and recovery. A technical
disclosure may show a validated HTTP status; do not render untrusted response
bodies. A missing anchor is distinct from a failed query. Graph-render and
expansion failures offer their respective recovery paths without exposing raw
exception text.

Retain a previous Impact result only within the same workspace and user scope.
When the scope changes, discard its graph, list, bounds warnings, and stale
state before the next read. A token refresh within the same scope may retain
the result if a subsequent read fails.

If Impact's graph cannot render, keep the error and focused retry control visible while a retry is pending. Ignore repeated activation; when rendering succeeds and removes the focused control, return focus to the object heading. The List representation remains available.

Topology retains a successful result through a refresh failure only while its
workspace, user, and edge scope still match. On a workspace or user change,
discard the old topology and filters and stop its graph expansion. When the user selects a different edge, show loading
or an error for that selection instead of presenting the previous edge's graph
as its result. Keep the edge selector available so the user can change scope.
If rendering the graph fails, retain the error and focused retry control while
retrying. When the graph returns, move focus to the topology heading if the
removed retry control still owned focus. The List representation remains an
alternative for resource inspection.

When no relationships are returned, both representations say that none were
found within the stated bounds. The graph may still show the anchor; the empty
message describes the result rather than implying an exhaustive traversal. A
mounted fixture test exercises both representations and retains the empty
result with a stale warning when a subsequent read fails.

## Content

Name the anchor and explain arrow direction in plain language. A legend pairs relation names with descriptions and visible line samples. Directional edges use arrows; lateral associations use lines without arrows and do not imply deletion impact. Distinguish upstream, downstream, and lateral associations rather than treating every edge as a deletion dependency. Topology initially draws an edge and namespace hierarchy derived from membership; its hierarchy lines alone do not establish deletion effects. Direct users to Impact for declared deletion context.

An unrecognized relation is shown by name with no arrow and an explicit
unknown direction in the legend and list. Do not infer deletion impact from
its presence. The [unknown-relation fixture](http://127.0.0.1:5197/kuery-impact.html?scenario=unknown-relation)
uses synthetic data to exercise this fallback.
The non-transitive `descendants` and `linked` names use the same line and
legend recipes as their transitive counterparts. Compare them in the
[relation-variants fixture](http://127.0.0.1:5197/kuery-impact.html?scenario=relation-aliases).

## Layout and responsive behavior

Keep representation controls, legend, bounds, and the selected view readable at narrow widths. The graph has a bounded viewport; the list presents grouped resource buttons and wraps within the page. Impact identifies its highlighted anchor in the page heading and help text, leaving the center of the graph clear for converging relation labels. Topology retains its Edge labels. When the viewport changes, preserve the centered graph point and user zoom rather than resetting the view. Nodes can remain outside the viewport after narrowing; the list remains an accessible way to inspect them. Review both themes, including actual graph content, before accepting palette changes.

Long relation and resource names wrap in the legend and list rather than
expanding the page. The [long-label fixture](http://127.0.0.1:5197/kuery-impact.html?scenario=long-relations)
uses synthetic names to exercise this at narrow widths.

## Accessibility

Do not rely on color alone: pair the graph with textual relation labels, line styles, a legend, and a keyboard-operable list. In Impact the canvas itself is exposed as an image and does not provide keyboard node selection; the visible List control offers the inspection path. When a focused list item is replaced by a new inspection, focus moves to the new object's heading unless the user has moved focus elsewhere. Topology's focusable graph supports pan and zoom keys, while its list provides resource actions. These measures do not constitute a completed screen-reader or color-vision review.

## Code and evidence

[ImpactView](../../../providers/kuery/portal/src/components/ImpactView.vue), [TopologyView](../../../providers/kuery/portal/src/components/TopologyView.vue), and [graph metadata](../../../providers/kuery/portal/src/graph.ts) are Kuery-specific implementations. The [Impact fixture](http://127.0.0.1:5197/kuery-impact.html) mounts the real view with synthetic data and no hub calls. Choose **List**, then **Simulate access change** to see the previous relationships cleared when a replacement transport denies access. **Restore sample access** reloads the sample. Use **Simulate missing object** to review a failed refresh with retained results. Turn on **Slow reads** before Retry to inspect the pending state. Choose **Allow next read to succeed** to review recovery when Retry completes. Its [empty result](http://127.0.0.1:5197/kuery-impact.html?scenario=empty) can be compared in Graph and List. The [Topology fixture](http://127.0.0.1:5197/kuery-topology.html) also simulates a failed refresh and delayed retry while retaining the graph or list. Mounted [Impact tests](../../../hack/design-review/kuery-impact.test.ts) cover a SavedView change, a failed new read, and a late old response; that view switch is not exposed by this fixture. Automated contrast and focused keyboard fixture checks do not complete the remaining accessibility review.

In the Topology fixture, select a resource in List to open its synthetic Impact
view and return with Back. Chromium checked this path at 1440px light and
390px dark without page overflow; it does not establish live query behavior.

The [mounted Impact test](../../../hack/design-review/kuery-impact.test.ts) simulates a graph-render failure and delayed recovery, including focus and repeat-activation behavior. The browser fixture above does not simulate a rendering failure.
It also checks that a workspace change removes the previous list while the
replacement read is pending and after it fails; same-scope refresh failure
retains the last result.
In the fixture, enable **Slow reads**, select **List**, then use **Switch sample
workspace** to see the old list clear while the next read is pending. Combine
it with **Simulate missing object** to review a failed read in the new scope.
The Topology fixture also offers **Switch sample workspace**; combine it with
**Fail next read** to check a failed read after the switch. Its mounted
test covers that transition and focus after the old list is removed.

Browser review of the synthetic Impact graph included deuteranopia and grayscale simulations, and light and dark themes. Relation names and line styles remain present without color; that review used the smaller default sample and does not establish categorical distinguishability. The [all-relations fixture](http://127.0.0.1:5197/kuery-impact.html?scenario=all-relations) combines all nine relation types for further visual review; the combination is not a realistic topology. At 390 px, List opens by default; fitted canvas text in Graph remains small, and zoom controls enlarge it. The zoom controls measured at least 44 px high under simulated coarse input. A resize from desktop to 390 px kept the graph centered after the resize fix. The topology fixture was also checked at desktop width. These checks do not replace real-device, color-vision, or assistive-technology review.

An additional Chromium fixture check at desktop and 390px in both themes
exercised Graph/List and zoom controls by keyboard and focus after choosing a
list item. The measured coarse-pointer controls and related row were at least
44 × 44px. Fitted graph labels remained small on mobile.

A further all-relations review used deuteranopia in both themes, plus
protanopia and tritanopia in dark mode. Some relations sharing a line style
appeared similar under red–green simulations, so color and dash style alone
are not a reliable way to identify every relation. List displayed all nine
named groups; keyboard selection moved focus to the newly selected object.
The result does not establish perceptual distinction or replace testing with
people who have color-vision deficiencies.

## Related guidance

See [known design oddities](../quality/known-oddities.md), [resource reads](../patterns/resource-reads.md), and the [interaction policy](../accessibility/interaction.md).
