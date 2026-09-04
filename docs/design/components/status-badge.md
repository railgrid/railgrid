---
{"schema":1,"id":"design.components.status-badge","title":"StatusBadge","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"StatusBadge is the shared square mono status tag; state-to-tone mapping remains caller-owned but vocabulary is fixed."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/status-badge.md#statusbadge","role":"design"},{"path":"provider-sdk/portalkit-vue/StatusBadge.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.foundations.colors","relation":"prerequisite","path":"docs/design/foundations/colors.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# StatusBadge

## Purpose

`StatusBadge` is the shared square mono status tag for operational and lifecycle
state. State-to-tone mapping remains caller-owned, but the status vocabulary is
fixed.

## Use when

Use `StatusBadge` for operational and lifecycle status. Keep status vocabulary
consistent: ready, active, and connected use success; pending, provisioning,
and running use warning; failed, terminating, and disconnected use danger;
unknown uses muted.

## Avoid when

Finite non-status enum values use a muted square `.k-badge`, not
`StatusBadge`. Verbose provider feedback belongs in the status title and
accessible name rather than a second visible status line. Badges do not glow.

## Anatomy and variants

The badge is a square 3px-radius mono tag with a status dot. A live state may
layer `.live-dot` on the 5–6px circle. The semantic tone variants are success,
warning, danger, and muted as defined by the state vocabulary above.

## Behavior

Only live dots pulse; badges do not glow. The caller chooses the lifecycle state
and the shared vocabulary supplies its semantic tone.

## Content

Use the status value as the visible tag and put verbose provider feedback in the
status title and accessible name. Keep finite non-status enum values as muted
square `.k-badge` tags.

## Layout and responsive behavior

Use the square 3px-radius tag and a 5–6px live-state circle. Do not turn the
badge into a pill or add decorative glow.

## Accessibility

Expose the operational or lifecycle status through the status title and
accessible name when additional provider feedback is needed; do not rely on a
second visible status line. The [accessible interaction policy](../accessibility/interaction.md)
supplies the cross-component naming and contrast contract.

## Code and evidence

The canonical implementations are `provider-sdk/portalkit-vue/StatusBadge.vue`
and `provider-sdk/portalkit/faros-ui.css`. Verify with `make verify-portalkit`.

## Related guidance

Use the [color foundation](../foundations/colors.md) for semantic tones, the
[UI copy policy](../content/ui-copy.md) for status language, the [accessible
interaction policy](../accessibility/interaction.md), and the [review
checklist](../quality/review-checklist.md).
