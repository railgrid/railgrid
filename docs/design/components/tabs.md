---
{"schema":1,"id":"design.components.tabs","title":"PortalKit provider route tabs","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The vanilla helper and Vue component share the canonical k-tabs recipe; callers own routing."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/tabs.md#portalkit-provider-route-tabs","role":"design"},{"path":"provider-sdk/portalkit/tabs.ts","role":"implementation"},{"path":"provider-sdk/portalkit-vue/Tabs.vue","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"implements","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.foundations.colors","relation":"prerequisite","path":"docs/design/foundations/colors.md"},{"id":"design.accessibility.interaction","relation":"see-also","path":"docs/design/accessibility/interaction.md"},{"id":"design.quality.review-checklist","relation":"see-also","path":"docs/design/quality/review-checklist.md"}]}
---

# PortalKit provider route tabs

## Purpose

This labeled navigation is the provider-level route/section bar.
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

1. **Navigation container:** a labeled `nav` groups related sections.
2. **Section control:** each `type="button"` has a visible label and may
   include an icon or count.
3. **Active treatment:** primary navigation uses accent text and an underline;
   secondary navigation uses accent text on a subtle accent surface.

The active control exposes `aria-current="page"`. `Tabs.vue` emits `select`
and exposes each id as `data-k-tab-id`; the vanilla `tabs.ts` helper adds the
same class vocabulary. These are navigation buttons, not an ARIA tablist.

## Behavior

Routing and selected state remain caller-owned. Primary navigation uses an
accent underline and text for the active section. Secondary navigation uses
accent text on a subtle accent surface, with slightly smaller type. Idle tabs
use muted text on transparent. Primary tabs stay transparent on hover, with a
text-color change; inactive secondary tabs use `surface-hover` on hover, while
the selected secondary tab keeps its accent surface. Tabs have no glow or shadow. Use `variant="secondary"`
for the Vue component or `.k-tabs--secondary` for framework-neutral markup;
the existing `.k-tabs--compact` class retains the secondary treatment.

In `Tabs.vue`, each item's `active` value takes precedence over the component's
`active` ID, including when the item explicitly sets `active: false`. Prefer
one source of selection state: either pass the selected ID to the component
and omit `active` on every item, or set item flags and omit the component ID.
The component does not enforce a single selection. If inputs conflict, it can
render multiple `aria-current="page"` controls or none, so callers must keep
the state consistent with the current route. A disabled item is a native
disabled button and does not emit `select`, but it can still be marked active;
callers must avoid leaving an unavailable route as the selected item.

For a real route, callers may supply an item `href` using the host's scoped
navigation URL. The component renders a native link: an ordinary activation
emits `select` for the caller's router, while modified or middle clicks retain
browser link behavior. Items without an `href` remain buttons for local state;
disabled items remain disabled buttons. Edges uses this link mode when the host
provides its navigation base and falls back to buttons in a standalone context.

The framework-neutral helpers only return classes. Their callers must provide
the navigation label, button semantics, disabled behavior, `aria-current`, and
single-selection state themselves.

## Content

Use a concise route or section label. Icons and counts are optional; an icon
does not replace the label. Counts use square 3px mono tags.

## Layout and responsive behavior

Primary tabs use an underline and a 1px bottom hairline; secondary tabs use a
compact filled selection without a bottom rule. Narrow hosts keep the row
horizontal and allow overflow; coarse pointers get 44px targets.

## Accessibility

Use labeled `nav` markup, native links for route destinations or `type="button"`
for local choices, and
`aria-current="page"` on the active tab. The 2px focus-visible outline is
required; the [accessible interaction policy](../accessibility/interaction.md)
supplies the broader keyboard, focus, and naming contract.

## Code and evidence

Canonical implementations are `provider-sdk/portalkit/tabs.ts`,
`provider-sdk/portalkit-vue/Tabs.vue`, and
`provider-sdk/portalkit/railgrid-ui.css`. Compare both levels in the
[design book review](http://127.0.0.1:5197/?entry=design.components.tabs)
and the [App Studio models fixture](http://127.0.0.1:5198/?provider=app).
The fixture uses sample data. Verify distribution with `make verify-portalkit`.

## Related guidance

Compose route tabs with the [navigation and feedback pattern](../patterns/navigation-and-feedback.md),
use the [color foundation](../foundations/colors.md) for active/idle tones, and
follow the [accessible interaction policy](../accessibility/interaction.md) and
[review checklist](../quality/review-checklist.md).
