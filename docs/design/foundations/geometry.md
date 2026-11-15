---
{"schema":1,"id":"design.foundations.geometry","title":"Spacing and shape","kind":"token","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Tailwind radius aliases, shared CSS recipes, and named motion affordances implement the sharp geometry law. Spacing values are informative observations, not an enforced token scale or a claim of universal adoption."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/geometry.md#radius-and-motion-geometry","role":"design"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"},{"path":"portal/src/components/AppLayout.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing","evidence":"Checks coded conventions; it does not establish rendered geometry or reduced-motion behavior across products."},{"kind":"review","ref":"Product-wide radius and motion review","status":"pending"},{"kind":"review","ref":"Spacing role and cross-product adoption review","status":"pending"}]},"relatedDocuments":[{"id":"design.patterns.fluid-shell","relation":"related"},{"id":"design.patterns.resource-creation","relation":"related"},{"id":"design.components.resource-section-card","relation":"see-also"}]}
---

# Spacing and shape

## Radius and motion geometry

Cards, tables, modals, and panels use 6px. Controls (buttons, inputs, selects,
and tabs) use 4px. Badges and tags are square 3px. True circles (dots, avatars,
spinners, and toggle knobs) use `50%`/`9999px`. Pills are banned.

Tailwind's radius scale is remapped globally in `main.css`:

| Utility | Compiles to |
|---|---|
| `rounded-xs` | 2px |
| `rounded-sm` | 3px (badge/tag) |
| `rounded-md` | 4px (control) |
| `rounded-lg` / `rounded-xl` | 6px (card) |
| `rounded-2xl` | 8px (rare oversized hero tile) |
| `rounded-3xl` | 12px (rare login tile) |

Self-contained portals that compile their own Tailwind must repeat the
`--radius-*` overrides in their `@theme`; hand-rolled stylesheets write the px
values directly. The only soft exception is conversational chat bubbles in
App Studio and Agents, which may use 12–14px because speech is not chrome.

Motion has a separate [review and guidance](motion.md), including the limits of the current reduced-motion implementation.

## Spacing and layout

This page groups observed dimensions for review. These are current recipe values, not a new enforced spacing-token scale: different components have different needs, and individual providers may still diverge. Use the actual component and page pattern as the authority for a specific surface.

### Current shared geometry

| Context | Current recipe | Design reason |
| --- | --- | --- |
| Ordinary shell | `px-4 py-5 sm:px-8` on the AppLayout content column. | Keep a shared page gutter while allowing ordinary pages to use the available width. |
| Resource page | 18px vertical gap; collection variant uses 16px. | Separate heading, summary, and body without centering the whole page. |
| Section card | 24px padding and 20px internal gap; divided sections have their own recipe. | Separate a distinct section's heading and content. |
| Create form | 18px body padding, 14px field gap; action footer uses 14px × 18px padding and an 8px action gap. | Keep the form readable and its actions together. |
| Guided form | 24px gap between form and guidance when side by side. | Give the task fields priority; the shared recipe changes the composition at smaller container widths. |
| Table | Default headers and cells use 12px × 20px padding; other table variants and breakpoints can differ. | Make rows scannable while allowing the table to own horizontal overflow. |
| Button | 8px × 14px padding and 8px icon/label gap for the base recipe. | Keep related action content together. |

### Composition rules

The shell owns page gutters. Lists and tables can use the fluid content width; a local form or prose region may use a readable measure without adding a competing page-wide container. Group closely related controls more tightly than separate sections. Prefer spacing and dividers to redundant nested cards. At narrow widths, keep controls reachable and preserve the task's reading order rather than shrinking type to fit.

Full-bleed workbenches are an explicit separate shell contract: they own their viewport and scrolling. See [fluid page composition](../patterns/fluid-shell.md) and [resource creation](../patterns/resource-creation.md).

### Review and evidence

The design-book example mounts the shared resource page, section, and input recipes. Compare it with the linked Edge Services, agent run, and deployment fixtures, and inspect the [shell implementation](../../../portal/src/components/AppLayout.vue) for actual outer gutters. The examples use sample data; they do not establish a product-wide spacing audit.
