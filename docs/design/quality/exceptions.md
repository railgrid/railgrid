---
{"schema":1,"id":"design.quality.exceptions","title":"Sanctioned design exceptions","kind":"policy","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The exception registry is checked by verify-ui-conformance and contains only exact, source-located exceptions."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/quality/exceptions.md#sanctioned-design-exceptions","role":"design"},{"path":"hack/ui-conformance-exceptions.json","role":"implementation"},{"path":"hack/verify-ui-conformance.mjs","role":"implementation"},{"path":"hack/dex/web/static/main.css","role":"implementation"},{"path":"hack/dex/web/themes/light/styles.css","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[{"id":"design.quality.ui-conformance","relation":"see-also","path":"docs/design/quality/ui-conformance.md"}]}
---

# Sanctioned design exceptions

Anything not named here follows the Violet Circuit system. These are product or
technical boundaries, not a debt baseline:

| Exception | Boundary |
|---|---|
| Chat bubbles at 12–14px | Conversational voice, not chrome |
| Terminal canvas colors in `TerminalDock.vue` | Terminals are always dark and read as one intentional dark surface |
| App preview iframe white background | The user's app owns its canvas |
| Third-party brand icon tiles (Google, GitHub, and similar on Dex) | Brand guidelines inside a 20px tile take precedence |
| Kuery graph `RELATION_METADATA` colors | Semantic edge palette, not UI chrome |
| Decorative blurred accent orbs (`blur-[140px]` circles on login/404) | Ambient ground texture below the glow rule's radar |
| Dex auth pages under `hack/dex/web/static/` | Fixed-dark standalone pages pin a local `--faros-*` namespace; Dex's light theme hook is retained only for its URL contract and contains no light palette |

The checked-in registry is `hack/ui-conformance-exceptions.json`. Every entry
names one rule, exact repository-relative path, line, column, and source
substring. Its `reference` is the stable catalog ID
`design.quality.exceptions`; `verify-ui-conformance` resolves that ID through
the design catalog, validates the locator against the current source, and
rejects debt, temporary, legacy, or baseline wording. It does not accept a
count baseline or broad path exemption.

The graph's categorical palette remains deliberately explicit because Cytoscape
cannot read CSS variables. Its metadata now supplies theme-specific colors and
non-color line styles, with an automated light-surface contrast floor; the
remaining both-theme, color-blind, browser, and assistive-technology review is
tracked as a [known oddity](known-oddities.md). The exception records the
boundary, not a promise that the palette is universally accessible.
