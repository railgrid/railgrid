---
{"schema":1,"id":"design.foundations.typography","title":"Violet Circuit typography","kind":"token","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Fonts are self-hosted in the host, while standalone fixed-dark Dex pages embed only their actual Instrument Sans and IBM Plex Mono faces; status documents use a self-contained system-font fallback."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/typography.md#typography","role":"design"},{"path":"portal/src/main.ts","role":"implementation"},{"path":"portal/src/assets/main.css","role":"implementation"},{"path":"hack/dex/web/static/main.css","role":"implementation"},{"path":"hack/dex/web/static/fonts","role":"implementation"},{"path":"provider-sdk/statuspage/statuspage.go","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing","evidence":"Checks source-level UI conformance; it does not establish rendered typography, readability, or adoption across every portal."},{"kind":"review","ref":"Typography role adoption and accessibility audit","status":"pending","evidence":"Synthetic fixtures illustrate several roles, but product-wide and assistive-technology review remain incomplete."}]},"relatedDocuments":[]}
---

# Typography

Fonts are self-hosted through `@fontsource` and imported in `portal/src/main.ts`.
The portal/provider roles are:

| Surface | Role | Face | Usage |
|---|---|---|---|
| Portal/provider | `font-sans` | Instrument Sans Variable | Body, UI copy, functional headings, KPI numerals |
| Portal/provider | `font-display` (`.type-display`) | Archivo Variable at `font-stretch: 125%` | RAILGRID wordmark and occasional promotional brand surfaces only |
| Portal/provider | `font-mono` | IBM Plex Mono | Identifiers, statuses, badges, table headers, timestamps, code |

Functional page titles—including create/edit forms, resource details, organization
selection, and operational dashboards—use Instrument Sans at normal width and
weight 600 (semibold). Establish hierarchy with size and spacing, not an expanded
face. Shared `.k-create-title` and `.k-resource-page__title` recipes own this
contract. Do not apply `font-display` or `.type-display` to functional headings
or dashboard numerals.

Dex auth is a standalone fixed-dark document. Its local stylesheet embeds only
Instrument Sans Variable (weight range 400–700) for sans copy and IBM Plex Mono
(400, 600, and 700) for technical labels and values. Dex does not embed or
declare Archivo, so the portal's `font-display` role does not apply there. No
other faces and no CDN fonts are allowed.

Standalone status documents rendered by `provider-sdk/statuspage` are a separate
fixed-dark exception. They use the CSS system-font stack (`ui-sans-serif`,
`system-ui`, and monospace fallbacks), with no embedded or external font assets;
this keeps the self-contained auth/callback document usable under its caller's
existing CSP. This exception does not change Dex's self-hosted Instrument Sans
and IBM Plex Mono contract.

## Text roles for review

The existing product uses the following roles. The examples below describe
current shared recipes and common sizes; they are not a new set of typography
utilities. Review them in both themes and product context before standardizing
any additional size, weight or line-height token.

| Role | Current use | Evidence |
|---|---|---|
| Resource detail title | Name the current resource or page. | Default `.k-resource-page__title`: Instrument Sans, 18px, 600, 1.25 line height. |
| Collection title | Name a collection or a collection section. | `ResourcePage`'s `collection` variant: Instrument Sans, 15px, 600, normal line height and letter spacing. Choose the heading level for its place in the page hierarchy. |
| Section title | Name a group of related content. | `.k-resource-section-card__title`: 14px, 600. |
| Body and supporting copy | Describe the task, context, or outcome. | Instrument Sans; most product body and table copy is 12–13px. |
| Field and button label | Name an action or input. | `.k-btn` uses 13px, 600; field labels follow their form context. |
| Helper and metadata | Supplement the control or record. | Usually 11–12px; use the accessible semantic text color on its actual surface. |
| Technical value | Show identifiers, timestamps and code. | IBM Plex Mono; use tabular numerals for aligned numbers. |
| Metric | Give a primary number emphasis. | `.k-kpi`: Instrument Sans, 26px, 600, tabular numerals. |
| Brand display | Wordmark or a deliberate brand surface. | Archivo Variable at 125% stretch; not a functional page title. |

The dense scale is explicit: `text-[9px]`–`text-[10px]` for eyebrows, section
labels, and badges (typically uppercase, tracked, weight 600; StatusBadge uses
quieter mixed-case text at weight 500); `text-[11px]` for nav
items, chips, and small labels; `text-[12px]`–`text-[13px]` for body, table
cells, and buttons; and `text-[14px]`–`text-[19px]` for headings. `.k-kpi` uses
26px Instrument Sans and `tabular-nums`. Numbers aligned in columns always use
`font-variant-numeric: tabular-nums`.

## Text color roles

Use `--color-text-primary` for resource names, headings, and important values;
`--color-text-secondary` for supporting explanations; and `--color-text-muted`
for labels and metadata that remain readable. Use `--color-accent` for links
and active choices. Success, warning, and danger colors communicate those
states alongside words or another non-color cue. Check text against its actual
surface in both themes; do not create a quieter role by lowering opacity. The
[typography review](http://127.0.0.1:5197/?entry=design.foundations.typography)
shows these existing tokens on raised surfaces in light and dark.
For accent text on the hover surface, use `--color-accent-hover`: the ordinary
accent does not meet the normal-text contrast floor on that surface. See the
[color pairing guidance](colors.md).

Review these roles together in the [deployment design example](http://127.0.0.1:5201/)
for titles, section headings, and summary values; the [agent run fixture](http://127.0.0.1:5200/)
for identifiers, step labels, and timestamps; and the [Edge Services product fixture](http://127.0.0.1:5199/product.html)
for table and control labels. The [Agents Connections fixture](http://127.0.0.1:5198/?provider=agents&route=%23%2Fconnections)
shows collection titles in context. The [Infrastructure instance fixture](http://127.0.0.1:5201/instance.html)
mounts the actual detail view; complete its sample read to see it. These use synthetic data and do not establish
product-wide adoption.
