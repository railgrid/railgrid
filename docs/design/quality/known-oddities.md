---
{"schema":1,"id":"design.quality.known-oddities","title":"Known design oddities and open specifications","kind":"decision","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"partial","notes":"These boundaries are intentionally recorded; palette accessibility and native select behavior remain open work."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/quality/known-oddities.md#known-design-oddities-and-open-specifications","role":"design"},{"path":"providers/kuery/portal/src/graph.ts","role":"implementation"},{"path":"providers/edges/portal/src/Services.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"accessibility review for graph palette","status":"pending"}]},"relatedDocuments":[]}
---

# Known design oddities and open specifications

The following are deliberately visible gaps or boundaries, not silently
normalized in the migration:

- **Kuery graph relation palette.** `graph.ts` `RELATION_METADATA` is a
  hand-picked categorical set sanctioned as data visualization rather than UI
  chrome. It carries theme-specific colors and non-color line styles; source
  contract tests require 3:1 contrast on the light graph surface. A complete
  both-theme, color-blind, browser, and assistive-technology review is still
  pending and must remain deliberate.
- **Edges native select.** The service `<select>` keeps `appearance: auto` on
  purpose for native popup UX. Its closed control must still carry `.svc-input`
  or `.k-input` styling.
- **Slider.** No implementation is shipped. The build-to-this contract is a
  native range input with accent color or a 2px track, accent fill, square
  12×12px thumb, standard focus ring, and mono tabular value.
- **Date/time picker.** No custom picker is shipped. Use read-only mono output
  from `portal/src/utils/time.ts` or native `date`/`datetime-local` styled as
  `.k-input`; a range is two inputs joined by an en dash.
- **Command palette.** No implementation is shipped. The build-to-this
  contract is a centered 560px surface-raised panel with surface-derived scrim,
  borderless input, mono keycap, and grouped menu results.
