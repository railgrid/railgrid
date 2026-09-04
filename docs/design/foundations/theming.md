---
{"schema":1,"id":"design.foundations.theming","title":"Theme and degraded-path mechanics","kind":"policy","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The pre-paint bootstrap, runtime theme store, standalone fallback styles, and fixed-dark Dex stylesheet preserve the dark-first contract."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/theming.md#theme-and-degraded-path-mechanics","role":"design"},{"path":"portal/index.html","role":"implementation"},{"path":"portal/src/stores/theme.ts","role":"implementation"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"hack/dex/web/static/main.css","role":"implementation"},{"path":"hack/dex/web/themes/dark/styles.css","role":"implementation"},{"path":"hack/dex/web/themes/light/styles.css","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[{"id":"design.foundations.colors","relation":"prerequisite","path":"docs/design/foundations/colors.md"},{"id":"design.foundations.provider-integration","relation":"see-also","path":"docs/design/foundations/provider-integration.md"}]}
---

# Theme and degraded-path mechanics

Exactly one of `html.dark` or `html.light` is always set for the portal and
light-DOM provider surfaces. The pre-paint script in `portal/index.html`
defaults to dark when preference is unset, and the runtime store in
`portal/src/stores/theme.ts` cycles `dark → light → system`. No Tailwind `dark:`
variant is used; theming is pure CSS-variable flipping. If a variant seems
necessary, inspect the warning in `main.css` first.

Dex auth is a fixed-dark standalone exception, not a participant in the
portal theme toggle. `hack/dex/web/static/main.css` sets `color-scheme: dark`
and the dark `--faros-*` palette. Dex's dark and light theme hook files remain
available for its URL contract, but the light hook contains no alternate
palette; do not document or expose a Dex light mode.

Portal styles never use `@media (prefers-color-scheme)` because it fights the
class toggle. Standalone dev-harness pages under `providers/*/portal/public/`
are the only exception because they have no toggle. New tokens are added to
both the dark `@theme` block and the `html.light` block and then documented in
[color tokens](colors.md); a token present in only one theme is a bug.
