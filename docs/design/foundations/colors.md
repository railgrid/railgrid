---
{"schema":1,"id":"design.foundations.colors","title":"Color and materials","kind":"token","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The host and standalone stylesheet authorities expose the shared token vocabulary and dark fallbacks; the conformance scanner records the one accepted text-muted migration value. Material role mapping is an informative review proposal, not a shared material API or proof of product-wide adoption."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/colors.md#color-tokens","role":"design"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"provider-sdk/portalkit/railgrid-ui.css","role":"implementation"},{"path":"hack/dex/web/static/main.css","role":"implementation"},{"path":"hack/verify-ui-conformance.mjs","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"},{"kind":"review","ref":"Materials role and product adoption review","status":"pending"}]},"relatedDocuments":[{"id":"design.foundations.geometry","relation":"related"},{"id":"design.components.resource-section-card","relation":"see-also"},{"id":"design.components.menu","relation":"see-also"},{"id":"design.components.confirm-dialog","relation":"see-also"}]}
---

# Color and materials

## Color tokens

Tokens are defined once in `portal/src/assets/main.css` (`@theme` is the dark
base and `html.light` is the override). They cascade into every light-DOM
provider. The light column below describes portal and provider themes; Dex is a
fixed-dark standalone surface and uses its local `--railgrid-*` dark namespace.
Standalone pages otherwise use the same values in their local fallback
namespace.

| Token | Dark (base) | Light (`html.light`) | Role |
|---|---|---|---|
| `--color-surface` | `#0a0b12` | `#f1f1f6` | Page ground |
| `--color-surface-raised` | `#111320` | `#ffffff` | Cards, tables, dock |
| `--color-surface-overlay` | `#171927` | `#eaeaf2` | Inset content, tooltips, disabled fields, and component-specific overlays |
| `--color-surface-hover` | `#1e2033` | `#e3e3ee` | Hover states |
| `--color-border-subtle` | `rgba(255,255,255,.07)` | `#e7e6f1` | Default hairline |
| `--color-border-default` | `rgba(255,255,255,.11)` | `#dfdeeb` | Stronger hairline (inputs, chrome) |
| `--color-accent` | `#8b6bff` | `#6b48e8` | The violet: actions, links, active state |
| `--color-accent-hover` | `#a18aff` | `#5a38d6` | Hover on solid accent |
| `--color-accent-subtle` | `rgba(139,107,255,.14)` | `rgba(107,72,232,.10)` | Tinted fills (active nav, focus ring) |
| `--color-accent-glow` | `rgba(139,107,255,.30)` | `rgba(107,72,232,.18)` | The only glow source |
| `--color-text-primary` | `#e9e9f2` | `#14152a` | Headings, values |
| `--color-text-secondary` | `#8a8ca6` | `#565975` | Body, table cells |
| `--color-text-muted` | `#8587a1` | `#60637b` | Labels, hints, idle nav; AA on every surface |
| `--color-success` | `#2fd6a0` | `#067246` | Success text and state |
| `--color-warning` | `#f0a63a` | `#8f5500` | Warning text and state |
| `--color-danger` | `#ff5d5d` | `#b22a32` | Danger text and state; `-hover` is `#ff7676` / `#9f202a` |
| `--color-toggle-knob`, `--color-toggle-knob-border` | `#ffffff`, `#dedee6` | `#ffffff`, `#dedee6` | Theme-stable light switch knob and its subtle outline |
| `--color-danger-surface`, `--color-surface-base`, `--color-text-error`, `--color-on-accent` | aliases (`danger-subtle`, `surface`, `danger`, `#0a0b12`) | aliases (`danger-subtle`, `surface`, `danger`, `#fff`) | Compatibility aliases so `var()` never falls through to a stale literal |

Semantic subtle fills are theme-specific: dark uses 12% translucent success,
warning, and danger; light uses opaque `#e5f6ef`, `#fdf2e0`, and `#fcebec`,
respectively. `--color-success-border` is `rgba(47,214,160,.30)` in dark and
`rgba(12,156,102,.30)` in light. Use these tokens rather than deriving them
from the semantic text colors.

Rules:

- Never hardcode these values in a component; reference the variable.
- Tints and opacity variants use `color-mix(in srgb, var(--color-accent) 30%,
  transparent)`, not baked-in translucent hexes.
- New fallbacks in hand-rolled stylesheets (`var(--color-accent, #8b6bff)`)
  match the current dark-base value exactly; a stale fallback silently forks
  the theme. Existing provider fallbacks for `--color-text-muted` may retain
  the accepted migration value `#5d5f78`, while the current token is
  `#8587a1`; the older literal is not a second design token and must not be
  introduced in new code.
- Retired “Precision Flat” accents `#6d4fe0`, `#7c5bf5`, `#5a3fd4`, `#9b85f7`,
  and `#5b3fd0` are dead. Their appearance in a diff is a regression.
- Semantic success, warning, and danger colors are not the accent. Do not use
  violet for status or green/red for actions.
- Normal-size text needs at least 4.5:1 contrast against its actual surface.
  Primary, secondary, muted, success, warning, and danger text meet that floor
  on the four opaque surface tokens in both themes. Accent text meets it on
  page, raised, and overlay surfaces, but not on the hover surface (4.31:1 in
  dark and 4.44:1 in light). Use `--color-accent-hover` for text on
  `--color-surface-hover`; it meets the floor in both themes. These pairings
  do not establish contrast for translucent fills, images, or local overrides;
  check their composited result separately. The current worst muted cases are
  dark on surface-hover (4.56:1) and light on surface-hover (4.62:1).

## Materials and surface roles

This page groups existing surface tokens and recipes into roles for review. The
names below are descriptive, not new CSS classes or proof that every consumer
uses the same material. Review light and dark together before making the role
mapping normative.

| Role | Purpose | Current implementation evidence |
| --- | --- | --- |
| Page | The workspace behind the content. | `--color-surface`. |
| Raised section | A bounded card or table on the page. | `.k-card`, `.k-table` and `--color-surface-raised`; individual shared components control their own border and spacing. |
| Inset | Technical or supporting content within a surface. | `--color-surface-overlay`; the technical preformatted block has its own border and padding. |
| Hover | A temporary interaction state, not an independent elevation. | `--color-surface-hover` and component-owned hover rules. |
| Menu | A floating set of actions or choices. | `.k-menu`: raised surface, subtle border, 6px radius, theme-specific shadow and menu layer. |
| Tooltip | Short floating information attached to a control. | `.k-tooltip-layer`: overlay surface, subtle border, 4px radius and a tooltip layer. |
| Dialog | An interrupting decision above a scrim. | `.k-modal` and `.k-modal-overlay`: raised surface, border, 6px radius and dialog layer. |

Use spacing and dividers to group related content; do not add a card solely to
wrap another card. Use the existing shared recipe rather than reconstructing
one from individual tokens. Floating surfaces also need their correct focus,
keyboard and dismissal behavior; material is only the visual layer.

The hover token is not the default for text inputs or secondary buttons: those
currently match their containing surface. See [control patterns](../patterns/controls.md)
and [shared recipes](../implementation/recipes.md). The design-book comparisons are CSS specimens;
the linked product fixtures show actual component compositions.
The dialog specimen shows the shared scrim over a synthetic collection; it is
static and does not demonstrate modal interaction or focus behavior.

For context, the [Edge Services design example](http://127.0.0.1:5199/) places a
`ResourceTable` on the page surface and lets reviewers open an `ActionMenu` and
its simulated delete confirmation. The [deployment design example](http://127.0.0.1:5201/)
contrasts raised `ResourceSectionCard` sections with the divided Recent events
section and its table. Compare these studies with the actual portal views in the
[Edge Services product fixture](http://127.0.0.1:5199/product.html) and the
[Infrastructure instance fixture](http://127.0.0.1:5201/instance.html) (complete
the sample read to see the instance). All use synthetic data.

### Current material details

These are observations of the shared recipes, rather than a promise that every
local surface uses them. Shadows are theme-specific where indicated.

| Role | Border and shape | Shadow and layer |
| --- | --- | --- |
| Page | No enclosing border or radius. | Base document stacking context. |
| Card | Subtle border, 6px radius. | 0 1px 2px; `.k-card--flat` removes the shadow. |
| Table | Subtle border, 6px radius. | No shared wrapper shadow; content scrolls horizontally inside the border. |
| Menu | Subtle border, 6px radius, 4px inner padding. | Dark: 0 6px 18px; light: 0 12px 34px. Menu layer defaults to 1000. |
| Tooltip | Subtle border, 4px radius. | 0 4px 12px; fixed layer 1100. |
| Dialog | Default border, 6px radius, over a scrim. | Two shadows; modal layer defaults to 2147483000. |
| Fullscreen | Uses its own component surface and geometry. | Fullscreen layer defaults to 2000; there is not yet one shared workbench material. |

These numbers apply within their stacking contexts; a local stacking context
can still clip or cover a floating surface. Toasts default to 2147483001.
Anchoring, dismissal and focus belong to each interaction contract, not the
material alone. Review a proposed shared sheet material when a real consumer
and its behavior are available.
