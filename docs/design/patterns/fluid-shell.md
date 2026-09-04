---
{"schema":1,"id":"design.patterns.fluid-shell","title":"Fluid page composition","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"AppLayout owns the ordinary fluid column; local readability bounds belong to the task region."},"appliesTo":["portal","provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/fluid-shell.md#fluid-page-composition","role":"design"},{"path":"portal/src/components/AppLayout.vue","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[{"id":"design.foundations.principles","relation":"prerequisite"},{"id":"design.foundations.geometry","relation":"prerequisite"},{"id":"design.patterns.resource-creation","relation":"related"},{"id":"design.patterns.resource-reads","relation":"related"},{"id":"design.quality.review-checklist","relation":"see-also"}]}
---

# Fluid page composition

The ordinary AppLayout scroll column carries px-4 py-5 sm:px-8 padding. A
full-bleed surface uses p-0 and owns its own viewport.

`AppLayout` owns one fluid `w-full` column for ordinary routes. Providers do
not add page-level centering or `max-w-*` wrappers: moving between routes must
not change shell geometry, and wide tables, visualizations, and responsive
collections may use the viewport.

Readability is local to the task region. Keep prose near 65–75 characters per
line and simple forms or search controls to about `42rem` or less. Dense
provisioning forms may fill the content column but reflow responsively with a
roughly `20rem` minimum field width and no more than three columns. Fieldsets,
validation summaries, and nested structural groups span those columns rather
than being squeezed into one track. Collection grids choose deliberate minimum
card widths and columns. Tables own horizontal overflow inside their canvas;
surrounding controls remain reachable.

`<AppLayout full-bleed>` is a separate viewport-ownership contract: it removes
ordinary page padding and delegates scrolling to the surface, as the App Studio
workbench does. Do not use full-bleed merely because a table or grid is wide,
and do not add a local readable region that competes with the page shell.

Every view has one solid-accent primary button with the sanctioned glow (`0 0
16px var(--color-accent-glow)`, 22px on hover). Other actions are ghost
(overlay plus hairline) or text-level. Danger uses danger-subtle tint, or solid
danger inside confirmation dialogs, and never glows.

Inputs use overlay background, default-border hairline, and 4px radius. Focus
is accent border plus subtle ring and glow. There are no floating labels;
eyebrows sit above fields. Badges use the square mono tag. Status dots are 5–6px
circles in `currentColor`, with `.live-dot` for live state. Map ready/active/
connected to success, pending/provisioning/running to warning,
failed/terminating/disconnected to danger, and unknown to muted.
