---
{"schema":1,"id":"design.foundations.iconography","title":"Violet Circuit iconography","kind":"recipe","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Vue portals use Lucide and vanilla portals use the CSP-safe inline subset in PortalKit."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/iconography.md#iconography","role":"design"},{"path":"provider-sdk/portalkit/icons.ts","role":"implementation"},{"path":"portal/src/lib/categoryIcons.ts","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[]}
---

# Iconography

One family is used everywhere: Vue portals import from
[`lucide-vue-next`](https://lucide.dev), while vanilla-TS portals use `ic(name)`
from `provider-sdk/portalkit/icons.ts`, a hand-inlined CSP-safe subset of
Lucide-style stroke paths rendered at `1em` in `currentColor`. Extend the
canonical subset and run `make sync-portalkit`.

Never use Unicode glyphs or emoji as icons. Platform symbol fonts vary in
presentation, weight, and optical size; Lucide is consistent and inherits
color. Prefer a thin geometric mark over a literal pictogram: `Hexagon` for
brand, `Diamond`, `Zap`/`Activity`, `Sparkles` for AI, `Command`, `Target`, and
`Boxes` are the sanctioned abstract vocabulary. `Cloud`, `Server`, and
`Database` are fine for real objects.

| Context | Size | Stroke |
|---|---|---|
| Standard rows, buttons, table actions | `h-4 w-4` (16px) | `1.75` |
| Dense rows, sub-nav, chips | `h-3.5 w-3.5` (14px) | `1.75`–`2` |
| Micro eyebrows, badge glyphs, tiny marks | `h-3 w-3` and below | `2`–`2.5` |
| Large decorative empty states, hero tiles, nav brand | 20px+ | `1.25`–`1.5` |

Icons inherit row/button `currentColor` and never set their own color except
semantic status. Icons never glow; glow belongs to the active row or button.

| Meaning | Icon |
|---|---|
| Loading / in-flight | `Loader2` + `animate-spin` (the only spinner) |
| Success / inline confirm | `CheckCircle` / `Check` |
| Failure / dismiss | `XCircle` / `X` |
| Warning / degraded | `AlertTriangle` |
| Error detail | `AlertCircle` |
| Create / add | `Plus` |
| Delete | `Trash2` |
| Empty state | `Inbox` |
| Pending / time | `Clock` |
| Refresh / retry | `RefreshCw` |
| Provider without logo | `Puzzle` |
| AI / assistant | `Sparkles` |
| Brand | `Hexagon` |

Providers may ship a square `icon.svg` and declare
`iconURL: "/ui/providers/<name>/icon.svg"`; host navigation renders it at 14px
with `object-contain`. Registered categories map to Lucide through
`portal/src/lib/categoryIcons.ts`, and providers without a logo fall back to
`Puzzle`. Full-color logos are allowed only inside sanctioned third-party
brand tiles.
