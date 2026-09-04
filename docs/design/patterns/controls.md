---
{"schema":1,"id":"design.patterns.controls","title":"Control selection and input patterns","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Native controls and PortalKit FormSelect implement current input contracts; slider, date/time, and command-palette specs remain planned."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/controls.md#control-selection-and-input-patterns","role":"design"},{"path":"provider-sdk/portalkit-vue/FormSelect.vue","role":"implementation"},{"path":"provider-sdk/portalkit/form-select.ts","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.components.form-select","relation":"see-also"},{"id":"design.components.layout-selector","relation":"related"},{"id":"design.components.visual-primitives","relation":"related"},{"id":"design.foundations.geometry","relation":"prerequisite"},{"id":"design.foundations.recipes","relation":"prerequisite"},{"id":"design.quality.review-checklist","relation":"see-also"}]}
---

# Control selection and input patterns

The Vue and framework-neutral implementations in the
[FormSelect component](../components/form-select.md) own the product-consistent
single-select popup. Native select popups remain sanctioned. Native checkboxes
and radios are the baseline; custom styling is reserved for dense composite
rows and shared toggle recipes.

For a range input, no implementation is currently shipped. When needed, use a
native `<input type="range">` with `accent-color` first. A custom variant is a
2px `surface-overlay` track (`rounded-xs`), accent filled portion, and a
12×12px square 2px-radius `text-primary` thumb matching the toggle knob; focus
uses the standard ring and readouts use mono `tabular-nums`.

For date/time input, no custom picker is shipped. Dates are read-only mono
output via `portal/src/utils/time.ts` (relative plus title absolute). If input
is needed, use native `date`/`datetime-local` styled as `.k-input`. A range is
two inputs joined by an en dash, not a popover calendar.

For the command palette, no implementation is shipped. The planned contract is
a centered 560px, 6px `surface-raised` panel with hairline, heavy elevation,
and `surface/60` scrim. Its borderless 14px `.k-input` variant carries a mono
`⌘K` keycap; results use dropdown-menu items with a 10px mono uppercase group
eyebrow. This is the one larger-feeling chrome surface, but it still has no
gradients and one accent.
