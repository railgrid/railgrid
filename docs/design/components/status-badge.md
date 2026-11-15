---
{"schema":1,"id":"design.components.status-badge","title":"StatusBadge","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"StatusBadge is the shared square mono status tag; state-to-tone mapping remains caller-owned but vocabulary is fixed."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/status-badge.md#statusbadge","role":"design"},{"path":"provider-sdk/portalkit-vue/StatusBadge.vue","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"StatusBadge review fixture contrast","status":"passing","evidence":"Computed foreground and composited background contrast in the local review specimen was at least 4.92:1 in light and 4.96:1 in dark. The dark calculation used the review canvas surface. This does not establish contrast on every product surface or screen-reader behavior."},{"kind":"review","ref":"Product-wide status and assistive-technology audit","status":"pending"}]},"relatedDocuments":[{"id":"design.foundations.colors","relation":"prerequisite","path":"docs/design/foundations/colors.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# StatusBadge

## Purpose

`StatusBadge` is the shared square mono status tag for operational and lifecycle
state. State-to-tone mapping remains caller-owned, but the status vocabulary is
fixed.

## Use when

Use `StatusBadge` for operational and lifecycle status. The component infers
success for ready, active, and connected; warning for pending, provisioning,
and running; danger for failed, terminating, and disconnected; and muted for
unknown values. A caller may supply an explicit tone for domain-specific
states. `connected=false` overrides the tone to danger, but does not change
the visible status text; the caller must also communicate disconnection in
the label or nearby text.

## Avoid when

Finite non-status labels use a muted square `.k-badge.k-badge--label`, not
`StatusBadge`. This treatment preserves the supplied casing and omits the
status dot and visible border. Put detailed provider feedback in nearby text or an associated
details view when it is needed to understand or resolve a state. Badges do not
glow.

## Anatomy and variants

1. **Label:** the caller's concise status text, shown in its original case.
2. **Dot:** a decorative 5px circle; it pulses only for in-progress states.
3. **Surface:** a subtle semantic background with 3px corners and no visible
   border. Mono text uses less weight than a general-purpose `.k-badge`.

The inferred tone variants are:

| Tone | Recognized status values |
| --- | --- |
| Success | Ready, Succeeded, Committed, Active, Connected, Loaded |
| Warning | Scheduling, Queued, Building, Pending, Provisioning, Running, Retrying, Status unavailable, Loading, Starting, Loaded unverified |
| Danger | Terminating, Failed, Error, Repository missing, Connection missing, Disconnected, Unreachable, Needs attention |
| Muted | Other values, including Unknown |

Matching is case-insensitive. A caller may explicitly choose a tone, and
`connected=false` overrides it to danger without changing the label.

## Behavior

Scheduling, queued, building, pending, provisioning, running, retrying,
loading, and starting pulse their dot unless disconnected or danger-toned.
Status unavailable and loaded unverified are warning-toned but do not pulse.
Ready, failed, disconnected, and other settled states remain still.
Reduced-motion preferences disable the pulse. Badges do not glow. The caller
chooses the lifecycle state and may supply a semantic tone.

## Content

Use a concise status value as the visible tag. If connectivity and lifecycle
phase differ, make both understandable in text rather than signaling
disconnection only with color. Keep finite non-status enum values as muted
square `.k-badge.k-badge--label` tags.

## Layout and responsive behavior

Use the square 3px-radius tag and a 5–6px live-state circle. Do not turn the
badge into a pill or add decorative glow.

## Accessibility

The visible text conveys the status; the decorative dot is hidden from
assistive technology. This component does not provide a dedicated details or
accessible-name prop. Put relevant explanation in visible context or an
associated disclosure, and do not rely on color alone. The [accessible
interaction policy](../accessibility/interaction.md) supplies the
cross-component naming and contrast contract.

## Code and evidence

The canonical implementations are `provider-sdk/portalkit-vue/StatusBadge.vue`
and `provider-sdk/portalkit/railgrid-ui.css`. The Edges detail page uses the
badge for connection status and retains a disconnected edge's reported phase
in its visible label. The [Edge detail fixture](http://127.0.0.1:5199/detail.html)
mounts that product component with simulated connection data. Verify shared
copies with `make verify-portalkit`.

## Related guidance

Use the [color foundation](../foundations/colors.md) for semantic tones, the
[UI copy policy](../content/ui-copy.md) for status language, the [accessible
interaction policy](../accessibility/interaction.md), and the [review
checklist](../quality/review-checklist.md).
