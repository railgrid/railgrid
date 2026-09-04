---
{"schema":1,"id":"design.components.tabs","title":"PortalKit provider route tabs","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The vanilla helper and Vue component share the canonical k-tabs recipe; callers own routing."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/tabs.md#portalkit-provider-route-tabs","role":"design"},{"path":"provider-sdk/portalkit/tabs.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/Tabs.vue","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"implements","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.foundations.colors","relation":"prerequisite","path":"docs/design/foundations/colors.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# PortalKit provider route tabs

## Purpose

This labeled icon-plus-label navigation is the provider-level route/section bar.
The vanilla helper and Vue component share the canonical `k-tabs` recipe;
callers own routing and selected state.

## Use when

Use this bar for provider-level routes or sections. It is used by Agents, App
Studio, Code, Databricks, Edges, and Kuery. Infrastructure and Quickstart have
no equivalent provider-level bar.

## Avoid when

Detail/workbench tabsets are not automatically route tabs. Do not use provider
route tabs to imply that a detail/workbench tabset follows this contract.

## Anatomy and variants

Markup is a labeled `nav` with optional count tags. Each tab is
`type="button"`; the active tab exposes `aria-current="page"`. `Tabs.vue`
emits `select` and exposes each id as `data-k-tab-id`; the vanilla `tabs.ts`
helper adds the same class vocabulary.

## Behavior

Routing and selected state remain caller-owned. Idle tabs use muted text on
transparent; hover uses `surface-hover`; active tabs use accent text on
`accent-subtle`. Tabs have no glow or shadow.

## Content

Use labeled icon-plus-label tabs. Counts are optional and use square 3px mono
tags; the label remains the route/section name rather than an icon-only cue.

## Layout and responsive behavior

Tabs use 4px controls, `padding: 7px 13px`, 4px gap, and a 1px bottom hairline.
Narrow hosts keep the row horizontal and allow overflow.

## Accessibility

Use labeled `nav` markup, `type="button"` tabs, and
`aria-current="page"` on the active tab. The 2px focus-visible outline is
required; the [accessible interaction policy](../accessibility/interaction.md)
supplies the broader keyboard, focus, and naming contract.

## Code and evidence

Canonical implementations are `provider-sdk/portalkit/tabs.ts`,
`provider-sdk/portalkit-vue/Tabs.vue`, and
`provider-sdk/portalkit/faros-ui.css`. Verify with `make verify-portalkit`.

## Related guidance

Compose route tabs with the [navigation and feedback pattern](../patterns/navigation-and-feedback.md),
use the [color foundation](../foundations/colors.md) for active/idle tones, and
follow the [accessible interaction policy](../accessibility/interaction.md) and
[review checklist](../quality/review-checklist.md).
