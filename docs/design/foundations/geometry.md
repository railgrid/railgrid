---
{"schema":1,"id":"design.foundations.geometry","title":"Violet Circuit radius and motion geometry","kind":"token","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Tailwind radius aliases, shared CSS recipes, and named motion affordances implement the sharp geometry law."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/geometry.md#radius-and-motion-geometry","role":"design"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[]}
---

# Radius and motion geometry

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

Motion is similarly constrained to named, state-bearing recipes: `.stagger-item`
applies the `stagger-in` entry animation, `.live-dot` applies `live-pulse` to
live status, and existing controls use short 120–200ms hover/focus/state
eases. The `.k-progress__bar` width transition (300ms) is a sanctioned progress
update; modal, toast, and loading feedback retain their component-owned entry
recipes. Add no decorative motion, and make every new animation respect
`prefers-reduced-motion`.
