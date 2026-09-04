---
{"schema":1,"id":"design.components.resource-page","title":"ResourcePage read shell","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"ResourcePage owns the title hierarchy, read-state shell, action slot, and loading semantics while callers own action order, resource content, and fetches."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/resource-page.md#resourcepage-read-shell","role":"design"},{"path":"provider-sdk/portalkit-vue/ResourcePage.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"implements","path":"docs/design/patterns/resource-reads.md"},{"id":"design.foundations.typography","relation":"prerequisite","path":"docs/design/foundations/typography.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"see-also","path":"docs/design/content/ui-copy.md"},{"id":"design.components.conditions-panel","relation":"see-also","path":"docs/design/components/conditions-panel.md"},{"id":"design.components.resource-stat-cards","relation":"see-also","path":"docs/design/components/resource-stat-cards.md"}]}
---

# ResourcePage read shell

## Purpose

`ResourcePage` owns the title hierarchy and read-state shell; callers own
navigation and resource-specific content and fetching. The shell provides one
consistent detail-page boundary for resource reads while leaving resource
content to the caller.

## Use when

Use `ResourcePage` for a resource detail/read surface that needs the shared
title hierarchy, header actions, and read-state behavior. The only
resource-type prop is `kind`; section cards retain their independent
`eyebrow`.

## Avoid when

Do not use the shell to encode resource-specific content or fetch ownership.
Callers must not add title-size overrides, and header-side content is reserved
for actions only.

## Anatomy and variants

The header source order is fixed: title, optional resource `kind`, caller
`#meta`, optional `#status`, then optional subtitle, with PortalKit-owned dot
separators. The shell may render caller-provided `#loading` visuals; otherwise
it supplies the three-bar skeleton fallback. The `#actions` slot is caller-owned
and preserves caller order. A caller may choose a provider-specific primary
action, `Refresh`, and an overflow menu containing `Delete`, but `ResourcePage`
does not synthesize, reorder, or own those controls.

## Behavior

The shell owns polite status and live-region semantics. A successful snapshot
remains visible through later refresh failures, which show a stale/error notice
and `Retry`; the caller owns fetching and receives the retry event. Initial
failures expose the same retry path. Read serialization, authority fencing, and
cancellation follow the [resource-read pattern](../patterns/resource-reads.md).

## Content

Use the resource title and optional `kind`, metadata, status, and subtitle in
the prescribed order. Primary header anchors with an accent background retain
the `text-on-accent`/`--color-on-accent` contrast token in normal and hover
states; changing the background to `accent-hover` must not reduce readable
contrast. Solid accent actions use `--color-on-accent`: near-black text on the
bright dark-theme violet and white text on the light-theme violet. Host and
standalone provider fallbacks share this token so normal-size labels remain
readable.

## Layout and responsive behavior

The responsive title runs from 24px to 32px (22px on mobile), wraps long
identifiers safely, and uses tight tracking and leading. Actions remain
reachable at 44×44px for coarse and hybrid pointers.

## Accessibility

Keep the prescribed source order and expose the shell's polite status and
live-region behavior for loading and read failures. Preserve readable
`text-on-accent`/`--color-on-accent` contrast in both normal and hover states;
the [accessible interaction policy](../accessibility/interaction.md) supplies
the cross-component keyboard, focus, and naming contract.

## Code and evidence

The canonical implementation is
`provider-sdk/portalkit-vue/ResourcePage.vue`. Verify the shared implementation
with `make verify-portalkit`.

## Related guidance

Pair the shell with the [resource reads pattern](../patterns/resource-reads.md),
the [typography foundation](../foundations/typography.md), the [accessible
interaction policy](../accessibility/interaction.md), and the [UI copy
policy](../content/ui-copy.md).
