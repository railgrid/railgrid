---
{"schema":1,"id":"design.foundations.typography","title":"Violet Circuit typography","kind":"token","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Fonts are self-hosted in the host, while standalone fixed-dark Dex pages embed only their actual Instrument Sans and IBM Plex Mono faces."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/typography.md#typography","role":"design"},{"path":"portal/src/main.ts","role":"implementation"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"hack/dex/web/static/main.css","role":"implementation"},{"path":"hack/dex/web/static/fonts","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[]}
---

# Typography

Fonts are self-hosted through `@fontsource` and imported in `portal/src/main.ts`.
The portal/provider roles are:

| Surface | Role | Face | Usage |
|---|---|---|---|
| Portal/provider | `font-sans` | Instrument Sans Variable | Body and UI copy |
| Portal/provider | `font-display` (`.type-display`) | Archivo Variable at `font-stretch: 125%` | Page titles, KPI numerals, FAROS wordmark |
| Portal/provider | `font-mono` | IBM Plex Mono | Identifiers, statuses, badges, table headers, timestamps, code |

Dex auth is a standalone fixed-dark document. Its local stylesheet embeds only
Instrument Sans Variable (weight range 400–700) for sans copy and IBM Plex Mono
(400, 600, and 700) for technical labels and values. Dex does not embed or
declare Archivo, so the portal's `font-display` role does not apply there. No
other faces and no CDN fonts are allowed.

The dense scale is explicit: `text-[9px]`–`text-[10px]` for eyebrows, section
labels, and badges (uppercase, tracked, weight 600); `text-[11px]` for nav
items, chips, and small labels; `text-[12px]`–`text-[13px]` for body, table
cells, and buttons; and `text-[14px]`–`text-[19px]` for headings. `.k-kpi` uses
26px display type and `tabular-nums`. Numbers aligned in columns always use
`font-variant-numeric: tabular-nums`.
