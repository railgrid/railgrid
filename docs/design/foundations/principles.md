---
{"schema":1,"id":"design.foundations.principles","title":"Violet Circuit principles","kind":"principle","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The principles are enforced by shared tokens, recipes, the UI conformance gate, and an explicit fixed-dark Dex boundary."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/principles.md#principles","role":"design"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"hack/dex/web/static/main.css","role":"implementation"},{"path":"hack/verify-ui-conformance.mjs","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[]}
---

# Principles

Violet Circuit is the shared visual constitution for the host portal, provider
micro-frontends, PortalKit, and the Dex login page. Its one-sentence character
is near-black violet-tinted ground, hairline borders, sharp corners, dense
mono-heavy type, and one violet accent that glows only on things that are alive.

1. **Dark is the product.** The portal and provider themes use a dark base
   (`@theme` in `portal/src/assets/main.css`) with an `html.light` override;
   both are first-class and every supported component must hold up on both
   grounds. Dex auth is a separate fixed-dark standalone surface, documented in
   [theme mechanics](theming.md). Dark is the default and hard fallback in
   every degraded portal/provider path (JavaScript off, `matchMedia` missing,
   or storage errors).
2. **Sharp, not soft.** The [radius law](geometry.md) separates this system
   from template-grade SaaS. Never reintroduce a softer radius for one card.
3. **Glow means alive.** Light is a signal, not decoration. Only the active nav
   item, solid-accent primary buttons, focused inputs, and live dot may emit it.
   Everything else is flat; a decorative glow is off-system.
4. **Borders, not shadows.** Depth comes from 1px hairlines and surface steps,
   not drop shadows. Allowed shadows are the barely-there card lift (`0 1px
   2px`), modal/popover elevation, and sanctioned glows.
5. **Tokens or nothing.** Every color goes through `var(--color-*)`. A raw hex
   in a component is a bug, except for the [sanctioned exceptions](../quality/exceptions.md).
6. **Mono is the voice of the machine.** Identifiers, statuses, paths,
   timestamps, badges, table headers, and anything the system says about itself
   use IBM Plex Mono, usually small, often uppercase and letter-spaced.
