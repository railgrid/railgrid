---
{"schema":1,"id":"design.components.visual-primitives","title":"Shared visual primitives","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Toggle, progress, avatar, shortcut, and dropzone recipes are implemented in the canonical stylesheet."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/visual-primitives.md#shared-visual-primitives","role":"design"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.foundations.colors","relation":"prerequisite","path":"docs/design/foundations/colors.md"},{"id":"design.foundations.geometry","relation":"prerequisite","path":"docs/design/foundations/geometry.md"},{"id":"design.foundations.iconography","relation":"see-also","path":"docs/design/foundations/iconography.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"}]}
---

# Shared visual primitives

## Purpose

These shared visual primitives provide the canonical toggle, progress, avatar,
shortcut, and dropzone recipes used across portal surfaces.

## Use when

Use the corresponding `.k-*` recipe for these primitive visual needs. The
canonical stylesheet is the source for their shape, semantic tones, and state
presentation.

## Avoid when

Do not introduce a second visual language or turn the progress track into a
pill. A dropzone's drag-over tint is a target, not an action, so it does not
glow.

## Anatomy and variants

- **Toggle:** `.k-toggle` is a sharp 3px track with `bg-accent` when
  `aria-checked="true"` and `border-default` when off; its knob is 2px
  `text-primary` and uses the standard focus ring. SkillsWorkbench's inline
  Tailwind toggle follows the same shape.
- **Progress:** `.k-progress` is a 2px-radius `surface-overlay` track with
  semantic `__bar` fills (`--accent`, `--warning`, and `--danger`) and a width
  transition.
- **Avatar:** `.k-avatar` is a mono-initials circle, 28px or `--sm` 20px. A
  6px success presence dot uses `.live-dot`; the mono email chip remains the
  preferred identity treatment.
- **Shortcut:** `.k-kbd` is a mono 9px uppercase keycap, 3px radius,
  `surface-overlay`, hairline, and darker bottom edge. Join separate keycaps
  with a muted `+`.
- **Dropzone:** `.k-dropzone` uses a dashed hairline. `.is-dragover` is an
  accent dashed border plus `accent-subtle` tint; `.is-error` uses danger tones
  and progress uses `.k-progress`.

## Behavior

The toggle's `aria-checked="true"` state selects `bg-accent`, while off uses
`border-default`; the standard focus ring remains in use. Progress uses a width
transition and semantic `__bar` fills. A dropzone uses `.is-dragover` for its
target state and `.is-error` for danger state.

## Content

Dropzone copy is verb-first, such as “Drop a file, or browse”. Keycaps join
separate keys with a muted `+`. The mono email chip remains the preferred
identity treatment alongside the avatar primitive.

## Layout and responsive behavior

Use the documented sharp geometry: a 3px toggle track, 2px progress radius,
28px or `--sm` 20px avatar, 6px presence dot, and 3px keycap radius. The
primitive recipes retain their own dimensions rather than adding provider-local
variants.

## Accessibility

The toggle exposes its state through `aria-checked="true"` and retains the
standard focus ring. A dropzone's drag-over treatment is a target, not an
action, and never substitutes for its verb-first instruction. Follow the
[accessible interaction policy](../accessibility/interaction.md) for the
complete keyboard, focus, naming, and contrast contract.

## Code and evidence

The canonical implementations are the toggle, progress, avatar, shortcut, and
dropzone recipes in `provider-sdk/portalkit/faros-ui.css`. Verify with
`make verify-portalkit`.

## Related guidance

Use the [color foundation](../foundations/colors.md) for semantic fills, the
[geometry foundation](../foundations/geometry.md) for sharp dimensions, the
[iconography foundation](../foundations/iconography.md) where icons accompany
these primitives, and the [accessible interaction policy](../accessibility/interaction.md).
