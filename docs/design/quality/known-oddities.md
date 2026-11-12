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
- **Slider.** Native range is the current fallback. A shared custom slider's
  geometry and interaction require a rendered review in both themes and with
  keyboard and touch input.
- **Date/time picker.** No custom picker is shipped. Native inputs need an
  explicit timezone and daylight-saving policy when used for scheduling.
- **Global command palette.** No global implementation is shipped. Scope,
  shortcut, focus, results, and responsive behavior remain to be validated.
  The contextual AgentKit slash palette is a separate shipped component.
