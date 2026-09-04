---
{"schema":1,"id":"design.foundations.colors","title":"Violet Circuit color tokens","kind":"token","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The host and standalone stylesheet authorities expose the shared token vocabulary and dark fallbacks; the conformance scanner records the one accepted text-muted migration value."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/colors.md#color-tokens","role":"design"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"},{"path":"hack/dex/web/static/main.css","role":"implementation"},{"path":"hack/verify-ui-conformance.mjs","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[]}
---

# Color tokens

Tokens are defined once in `portal/src/assets/main.css` (`@theme` is the dark
base and `html.light` is the override). They cascade into every light-DOM
provider. The light column below describes portal and provider themes; Dex is a
fixed-dark standalone surface and uses its local `--faros-*` dark namespace.
Standalone pages otherwise use the same values in their local fallback
namespace.

| Token | Dark (base) | Light (`html.light`) | Role |
|---|---|---|---|
| `--color-surface` | `#0a0b12` | `#f1f1f6` | Page ground |
| `--color-surface-raised` | `#111320` | `#ffffff` | Cards, tables, dock |
| `--color-surface-overlay` | `#171927` | `#eaeaf2` | Popovers, inputs, ghost-button background |
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
| `--color-success` | `#2fd6a0` | `#067246` | `-subtle` at 12% and `-border` at 30% |
| `--color-warning` | `#f0a63a` | `#8f5500` | `-subtle` at 12% |
| `--color-danger` | `#ff5d5d` | `#b22a32` | `-subtle`, `-hover` (`#ff7676` / `#9f202a`) |
| `--color-danger-surface`, `--color-surface-base`, `--color-text-error`, `--color-on-accent` | aliases (`danger-subtle`, `surface`, `danger`, `#0a0b12`) | aliases (`danger-subtle`, `surface`, `danger`, `#fff`) | Compatibility aliases so `var()` never falls through to a stale literal |

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
- Normal-size token text retains at least 4.5:1 contrast on every surface. The
  current worst cases are dark muted text on surface-hover (4.56:1), light
  muted text on surface-hover (4.62:1), and light semantic text on subtle
  fills (success 5.36:1, warning 5.47:1, danger 5.58:1).
