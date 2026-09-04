---
{"schema":1,"id":"design.accessibility.interaction","title":"Accessible interaction policy","kind":"policy","status":"active","authority":{"design":"normative","implementation":"reference"},"implementation":{"state":"partial","notes":"PortalKit and several provider surfaces implement these interaction patterns; no complete keyboard, screen-reader, touch, contrast, or motion audit has been run."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/accessibility/interaction.md#accessible-interaction-policy","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourceTable.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceTableActionButton.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ActionMenu.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/FormSelect.vue","role":"implementation"},{"path":"provider-sdk/portalkit/form-select.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ConfirmDialog.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourcePage.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourceBackLink.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"providers/kuery/portal/src/graph.ts","role":"implementation"},{"path":"providers/kuery/portal/src/components/TopologyView.vue","role":"implementation"},{"path":"providers/kuery/portal/src/components/ImpactView.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-design-docs","status":"passing","evidence":"The design knowledge-base metadata, IDs, sources, and links validate."},{"kind":"review","ref":"comprehensive accessibility and provider interaction audit","status":"pending"}]},"relatedDocuments":[{"id":"design.components.resource-table","relation":"see-also","path":"docs/design/components/resource-table.md"},{"id":"design.components.resource-table-actions","relation":"see-also","path":"docs/design/components/resource-table-actions.md"},{"id":"design.components.action-menu","relation":"see-also","path":"docs/design/components/action-menu.md"},{"id":"design.components.form-select","relation":"see-also","path":"docs/design/components/form-select.md"},{"id":"design.components.confirm-dialog","relation":"see-also","path":"docs/design/components/confirm-dialog.md"},{"id":"design.components.resource-page","relation":"see-also","path":"docs/design/components/resource-page.md"},{"id":"design.components.resource-back-link","relation":"see-also","path":"docs/design/components/resource-back-link.md"},{"id":"design.patterns.resource-reads","relation":"see-also","path":"docs/design/patterns/resource-reads.md"},{"id":"design.foundations.colors","relation":"prerequisite","path":"docs/design/foundations/colors.md"},{"id":"design.foundations.geometry","relation":"see-also","path":"docs/design/foundations/geometry.md"},{"id":"design.quality.known-oddities","relation":"see-also","path":"docs/design/quality/known-oddities.md"}]}
---

# Accessible interaction policy

Accessibility is a behavior contract, not a color or component checklist.
Reuse the [shared component contracts](../components/) and preserve a usable
fallback when a rich surface cannot expose the same information. This entry
sets cross-cutting expectations; it does not replace component-specific
keyboard or markup mechanics.

## Normative contract

- **Use native semantics first.** Prefer real links, buttons, form controls,
  headings, landmarks, lists, and tables. Every interactive control has an
  accessible name that describes its action and object; decorative icons are
  hidden from assistive technology. Do not turn a table row into
  `role="button"`; keep nested links, buttons, and fields independent. Custom
  roles are justified only when the shared component owns their complete
  semantics.
- **Make the keyboard path complete.** Every pointer action has a keyboard
  path. Preserve visible `:focus-visible` treatment. Shared menus and selects
  support their documented Enter/Space, Arrow, Home/End, and Escape behavior;
  dialogs contain focus while open and return it to the invoking control.
  Focus must not disappear because an action is compact, icon-only, or inside a
  provider light-DOM bundle.
- **Announce meaningful read state.** Use a polite `role="status"` for loading,
  background updates, and settled empty-state changes; use an assertive alert
  for an initial failure that blocks the page. Mark the busy resource region,
  announce `Retrying`/`Refreshing` truthfully, and retain a useful loaded
  snapshot during a background failure. Do not repeatedly announce unchanged
  content or make a visual spinner the only explanation.
- **Support touch and hybrid input.** Interactive targets are at least 44×44px
  for coarse pointers, including row actions, retry, pagination, filter, and
  back controls. Actions that are hidden on desktop hover remain reachable and
  visible on touch. Spacing must prevent adjacent controls from becoming one
  ambiguous target.
- **Preserve contrast across themes.** Use the semantic token palette, not raw
  colors, and check both `html.dark` and `html.light` for portal/provider
  surfaces. Normal-size token text retains at least 4.5:1 contrast on every
  supported surface; meaning cannot depend on color alone, so pair status color
  with text, a label, or a shape. Focus and error states must remain perceivable
  in both portal/provider themes; Dex is fixed dark and is checked against its
  standalone dark palette.
- **Respect reduced motion.** Motion is never required to understand state or
  complete an action. Keep the shared entry, loading, and live-state behavior,
  but reduce or remove it under `prefers-reduced-motion: reduce`; preserve the
  static focus, busy, and status cues.
- **Keep rich graphs recoverable.** A canvas or graph must have a labeled
  region and a non-pointer representation when practical. Kuery's topology and
  impact views provide a list path, but the Cytoscape relation palette remains
  an explicit unresolved accessibility boundary (see below).

## Informative examples

The current shared implementations demonstrate the contract:

- `ResourceTable.vue` keeps native table rows, gives interactive rows a
  keyboard entry point, and isolates nested controls. `ResourceTableActionButton.vue`
  turns its caller-supplied resource label into both an accessible name and a
  busy label.
- `ActionMenu.vue`, the Vue and framework-neutral FormSelect implementations,
  and `ConfirmDialog.vue` own complete names/roles, keyboard handling, and focus
  return for their respective rich controls. Use them instead of recreating
  partial ARIA behavior.
- `ResourcePage.vue` exposes `aria-busy`, polite loading/refresh announcements,
  and an assertive initial error with a retry control. Its read-state contract
  is detailed in [resource reads](../patterns/resource-reads.md).
- `portal/src/assets/main.css` and `provider-sdk/portalkit/faros-ui.css`
  provide the theme tokens, focus styles, coarse-pointer target expansion, and
  reduced-motion media rules. These are implementation evidence, not proof that
  every provider has adopted them correctly.

## Kuery graph boundary

`providers/kuery/portal/src/graph.ts` keeps theme-specific colors and line
styles in `RELATION_METADATA` because Cytoscape cannot consume CSS variables.
`TopologyView.vue` and `ImpactView.vue` label their graph regions and offer list
representations. Source contract tests require every light-theme relation color
to reach 3:1 contrast on the graph surface and require a non-color line style,
but color-blind safety and assistive-technology behavior remain unverified.
Keep this limitation visible; do not call the graph accessibility-verified until
a dedicated browser and assistive-technology review covers the canvas, legend,
focus path, and list fallback in both themes.

## Verification boundary

`make verify-design-docs`, `make verify-portalkit`, and
`make verify-ui-conformance` validate documentation, distribution, and selected
source/style contracts. They do not constitute a comprehensive accessibility
audit: they do not replace keyboard-only traversal, screen-reader output,
touch/hybrid hit testing, theme contrast measurement, reduced-motion inspection,
or provider-by-provider copy and state review. Record that evidence separately
when a surface changes.
