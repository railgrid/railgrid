---
{"schema":1,"id":"design.patterns.navigation-and-feedback","title":"Navigation, feedback, and state composition","kind":"pattern","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The host shell and PortalKit recipes implement the navigation, feedback, and state composition rules."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/navigation-and-feedback.md#navigation-feedback-and-state-composition","role":"design"},{"path":"portal/src/components/AppLayout.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[{"id":"design.foundations.principles","relation":"prerequisite"},{"id":"design.foundations.recipes","relation":"prerequisite"},{"id":"design.foundations.iconography","relation":"prerequisite"},{"id":"design.components.tabs","relation":"related"},{"id":"design.components.menu","relation":"related"},{"id":"design.components.toast","relation":"related"},{"id":"design.components.tooltip","relation":"related"},{"id":"design.quality.review-checklist","relation":"see-also"}]}
---

# Navigation, feedback, and state composition

Modals and dialogs are 6px `surface-raised` hairlined surfaces with heavy
elevation; use the [confirmation component](../components/confirm-dialog.md).
The scrim is `bg-surface/60` or a surface-derived `color-mix`, never black or
text-derived.

Shell/sidebar idle items are muted text on nothing. An active shell item is
accent text on `accent-subtle` with a `0 0 14px` nav glow. Provider route tabs
are the separate [PortalKit tabs](../components/tabs.md) pattern and never glow
or shadow. Section headers are 9px mono uppercase with a trailing hairline.

The sidebar is a 56px icon rail by default. Labels are a click away through a
persisted browser toggle; collapsed rows are centered icon-only controls with a
native `title`; category groups collapse to hairline rules. Sub-navigation,
tenant chip, and theme switch appear only when expanded; expanded width is
192px.

Chat bubbles may use the sanctioned 12–14px soft radius: counterpart bubbles
use `surface-overlay`, user bubbles `accent-subtle`, and neither glows. Empty
states use restrained contour-grid texture, an eyebrow, one-line explanation,
and one primary action.

A settings page may contain one primary action for each independently
persisted, visibly named form region. Each such region owns its own dirty,
busy, success, and error state, so saving one region cannot imply that another
region was saved. This is a persistence boundary, not permission to place
multiple competing primary actions in one form or task.

Native checkboxes and radios inherit `accent-color: var(--color-accent)` from
`body`; do not restyle them with raw blue. Custom toggles use a sharp 3px track
(`bg-accent` on, `bg-border-default` off) and 2px `bg-text-primary` knob, never
an iOS pill. Dense composite checkboxes use `.k-checkbox`: 14×14px with zero
min dimensions, native accent color, no ordinary focus shadow, and only a 3px
`accent-subtle` focus-visible ring. The composite row owns keyboard focus;
visually present checkboxes may be `tabindex="-1"`/`aria-hidden="true"` and
route pointer activation back to the row. Labels are 12px secondary text with
8px gap.

Progress uses a 2px (`rounded-xs`) `surface-overlay` track with semantic fill,
never a pill. Skeletons use `.shimmer` in the exact geometry of loaded state.
Motion uses `.stagger-item` (`stagger-in`) for entry, `.live-dot` (`live-pulse`)
for live state, component-owned feedback entry, and 120–200ms hover/focus or
control-state eases. The `.k-progress__bar` width transition is a sanctioned
300ms progress update; respect reduced-motion preferences.
