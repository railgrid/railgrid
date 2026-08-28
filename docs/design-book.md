# Faros Design Book — "Violet Circuit"

The canonical reference for every pixel of faros UI: the host portal, all
provider micro-frontends, the portalkit, and the Dex login page. AGENTS.md §8
is the enforcement summary; this document is the full system with rationale.
When the two disagree, fix whichever is stale — they describe the same system.

**One sentence:** near-black violet-tinted ground, hairline borders, sharp
corners, dense mono-heavy type, and a single violet accent that *glows* only on
things that are alive.

---

## 1. Principles

1. **Dark is the product.** The base theme is dark (`@theme` in
   `portal/src/assets/main.css`); light is the `html.light` override. Both are
   first-class — every component must hold up on both grounds — but dark is the
   default and the hard fallback in every degraded path (JS off, `matchMedia`
   missing, storage errors).
2. **Sharp, not soft.** The radius law (§3) is what separates this system from
   template-grade SaaS. Never reintroduce a softer radius "just for this one
   card".
3. **Glow means alive.** Light is a signal, not a decoration. Only four things
   may emit it: the active nav item, solid-accent primary buttons, focused
   inputs, and the live dot. Everything else is flat. A glowing decorative
   element is off-system by definition.
4. **Borders, not shadows.** Depth comes from 1px hairlines and surface
   steps, not drop shadows. The only shadows are the barely-there card lift
   (`0 1px 2px`), modal/popover elevation, and the sanctioned glows.
5. **Tokens or nothing.** Every color goes through `var(--color-*)`. A raw hex
   in a component is a bug (exceptions in §8).
6. **Mono is the voice of the machine.** Identifiers, statuses, paths,
   timestamps, badges, table headers — anything the system says about itself —
   is IBM Plex Mono, usually small, often uppercase and letter-spaced.

## 2. Color tokens

Defined once in `portal/src/assets/main.css` (`@theme` = dark base,
`html.light` = override). They cascade into every provider portal through the
light DOM — providers reference the same variables and get theme switching for
free.

| Token | Dark (base) | Light (`html.light`) | Role |
|---|---|---|---|
| `--color-surface` | `#0a0b12` | `#f1f1f6` | Page ground |
| `--color-surface-raised` | `#111320` | `#ffffff` | Cards, tables, dock |
| `--color-surface-overlay` | `#171927` | `#eaeaf2` | Popovers, inputs, ghost-button bg |
| `--color-surface-hover` | `#1e2033` | `#e3e3ee` | Hover states |
| `--color-border-subtle` | `rgba(255,255,255,.07)` | `#e7e6f1` | Default hairline |
| `--color-border-default` | `rgba(255,255,255,.11)` | `#dfdeeb` | Stronger hairline (inputs, chrome) |
| `--color-accent` | `#8b6bff` | `#6b48e8` | THE violet. Actions, links, active state |
| `--color-accent-hover` | `#a18aff` | `#5a38d6` | Hover on solid accent |
| `--color-accent-subtle` | `rgba(139,107,255,.14)` | `rgba(107,72,232,.10)` | Tinted fills (active nav bg, focus ring) |
| `--color-accent-glow` | `rgba(139,107,255,.30)` | `rgba(107,72,232,.18)` | The ONLY glow source |
| `--color-text-primary` | `#e9e9f2` | `#14152a` | Headings, values |
| `--color-text-secondary` | `#8a8ca6` | `#565975` | Body, table cells |
| `--color-text-muted` | `#5d5f78` | `#8d8fa6` | Labels, hints, idle nav |
| `--color-success` | `#2fd6a0` | `#0c9c66` | + `-subtle` at 12% alpha (light: `#e5f6ef`), + `-border` at 30% |
| `--color-warning` | `#f0a63a` | `#c07508` | + `-subtle` (light: `#fdf2e0`) |
| `--color-danger` | `#ff5d5d` | `#d63a40` | + `-subtle` (light: `#fcebec`), + `-hover` (`#ff7676` / `#bf2f35`) |
| `--color-danger-surface`, `--color-surface-base`, `--color-text-error`, `--color-on-accent` | aliases | aliases | Compatibility aliases (= danger-subtle / surface / danger / `#fff`) so no `var()` ever falls through to a stale literal |

Rules:

- **Never** hardcode any of these values in a component; reference the var.
- Tints/opacity variants use `color-mix(in srgb, var(--color-accent) 30%, transparent)`,
  not baked-in translucent hexes.
- Fallbacks in hand-rolled stylesheets (`var(--color-accent, #8b6bff)`) must
  match the dark-base value exactly — a stale fallback silently forks the theme.
- The retired "Precision Flat" accents `#6d4fe0`, `#7c5bf5`, `#5a3fd4`,
  `#9b85f7`, `#5b3fd0` are **dead**. If one appears in a diff, it is a
  regression.
- Semantic color (success/warning/danger) is not the accent. Don't use the
  violet for status, and don't use green/red for actions.

## 3. Radius law

Cards, tables, modals, panels **6px** · controls (buttons, inputs, selects,
tabs) **4px** · badges/tags **3px, square** · true circles (dots, avatars,
spinners, toggle knobs) `50%`/`9999px` · **pills are banned.**

Tailwind's radius scale is remapped globally in `main.css` so the utilities
land on-system without per-component edits:

| Utility | Compiles to |
|---|---|
| `rounded-xs` | 2px |
| `rounded-sm` | 3px (badge/tag) |
| `rounded-md` | 4px (control) |
| `rounded-lg` / `rounded-xl` | 6px (card) |
| `rounded-2xl` | 8px (rare: oversized hero tiles) |
| `rounded-3xl` | 12px (rare: login tile) |

Self-contained provider portals that compile their own Tailwind (app-studio)
must repeat the `--radius-*` overrides in their own `@theme`. Hand-rolled
stylesheets write the px values directly.

**Sanctioned soft exception:** conversational chat bubbles (app-studio,
agents) may use 12–14px — speech is not chrome. Nothing else
qualifies.

## 4. Typography

Self-hosted via `@fontsource`, imported in `portal/src/main.ts`. Dex renders as
a standalone document, so `hack/dex/web/static/fonts/` embeds the matching
`@fontsource` WOFF2 files and `main.css` declares them locally. No other faces,
no CDN fonts.

| Role | Face | Usage |
|---|---|---|
| `font-sans` | Instrument Sans Variable | Body, UI copy |
| `font-display` (`.type-display`) | Archivo Variable at `font-stretch: 125%` | Page titles, KPI numerals, the FAROS wordmark |
| `font-mono` | IBM Plex Mono | Identifiers, statuses, badges, table headers, timestamps, code |

Scale (explicit px — the UI is deliberately dense):

- `text-[9px]`–`text-[10px]`: eyebrows, section labels, badges — uppercase,
  `tracking-[0.15em]` (eyebrows) or `0.06em` (badges), weight 600.
- `text-[11px]`: nav items, chips, small labels.
- `text-[12px]`–`text-[13px]`: body, table cells, buttons.
- `text-[14px]`–`text-[19px]`: headings; KPIs use `.k-kpi` (26px display,
  `tabular-nums`).

Numbers that align in columns always get `font-variant-numeric: tabular-nums`.

## 5. The recipes (`k-*` classes)

`provider-sdk/portalkit/faros-ui.css` is the canonical component vocabulary. The
host copy at `portal/src/assets/faros-ui.css` and the copies vendored under each
portal's `src/portalkit/` are exact sync outputs — `make sync-portalkit` writes
them and `make verify-portalkit` rejects drift or unexpected files. The
stylesheet cascades into every light-DOM provider — **use these classes before
writing any CSS**:

| Class | What it is |
|---|---|
| `.k-card` (+ `--flat`) | 6px surface-raised card, subtle hairline, `0 1px 2px` lift |
| `.k-table` | 6px table wrapper; mono 9–10px uppercase headers, 13px rows, accent-tint hover via `.is-interactive` |
| `.k-cell-mono` | Data-like cells (names, ids, timestamps) |
| `.k-badge` (+ `--success/--warning/--danger/--muted`, `__dot`) | **Square 3px mono tag**: 10px/600 uppercase, `0.06em`, `*-subtle` bg, `color-mix(currentColor 35%)` hairline |
| `.k-btn` (+ `--primary/--ghost/--text/--danger`) | 4px control; primary = solid accent + glow; ghost = overlay bg + hairline; text = transparent, borderless inline action; danger = danger-subtle tint, **no glow** |
| `.k-back-action` | Intrinsic-width, start-aligned borderless link modifier for `.k-btn`; 12px/500 accent link with a 6px icon gap, accent-hover underline, and no control surface |
| `.k-input` | 4px overlay-bg input; focus = accent border + 3px subtle ring + glow |
| `.k-eyebrow` / `.k-kpi` | Tracked uppercase label over an expanded tabular numeral |
| `.k-menu` / `.k-menu-item` (+ `--danger`, `.is-selected`, `.k-menu-sep`) | Dropdown/context menu panel + items; selection = accent-subtle, no glow |
| `.k-layout-selector` (+ `__trigger`, `__menu`, `__item`) | Controlled grid/list presentation menu; compact icon trigger, radio semantics, no glow |
| `.k-kbd` | Shortcut key-cap: mono 9px uppercase, 3px, darker bottom edge |
| `[data-k-tip="…"]` | CSS-only tooltip: 300ms delay, shows on hover AND focus, 260px max |
| `.k-progress` / `.k-progress__bar` (+ `--accent/--warning/--danger`) | 2px-radius track, semantic fill |
| `.k-toggle` / `.k-toggle__knob` | Sharp switch: 3px track (`aria-checked` drives state), 2px `text-primary` knob |
| `.k-avatar` (+ `--sm`) | Mono-initials circle, 28/20px; presence = `.live-dot` success dot |
| `.k-dropzone` (+ `.is-dragover`, `.is-error`) | Dashed drop target; accent tint on drag-over, no glow |

For toasts, use `portalkit/toast.ts` (`toast('ok' | 'error' | 'info', message,
action?)`) — a framework-free bus + bottom-right stack with auto-dismiss,
hover-pause and `aria-live`; vendored into every portal via
`make sync-portalkit`.

Signature utilities in `main.css`: `.contour-grid` (+ `-fade`) wavy-line hero
texture (login, empty states — sparingly), `.island` floating dock card,
`.live-dot` opacity pulse (providers depend on it — never delete),
`.shimmer` skeletons, `.stagger-item` entry animation, `.type-display`.

## 6. Component patterns

- **Buttons.** One solid-accent primary per view, and it glows
  (`0 0 16px var(--color-accent-glow)`, 22px on hover). Everything else is
  ghost (overlay bg + hairline) or text-level. Danger actions use the
  danger-subtle tint, or solid danger inside confirm dialogs — never glowing.
- **Badges / status.** Always the square mono tag. Status dots are 5–6px
  circles in `currentColor`; a "live" state layers `.live-dot`. Tones:
  ready/active/connected → success; pending/provisioning/running → warning;
  failed/terminating/disconnected → danger; unknown → muted.
- **Inputs.** Overlay bg, default-border hairline, 4px. Focus is the only
  state change: accent border + subtle ring + glow. No floating labels; labels
  are eyebrows above the field.
- **Tables.** `.k-table`. Headers speak mono-uppercase; cells 13px secondary;
  identifier columns `.k-cell-mono`; row hover = 4% accent tint, interactive
  rows lift text to primary. Vue resource tables use
  `portalkit/ResourceTableEditButton.vue` and
  `portalkit/ResourceTableDeleteButton.vue` for compact row actions that reveal
  on row hover or keyboard focus (and remain visible on touch). Give every action
  a resource-specific accessible label. Use
  `portalkit/ResourceTableActionButton.vue` for other compact row actions:
  callers supply the Lucide icon and accessible label, and may provide a busy
  label/state, disabled state, and one of the sanctioned `neutral`, `accent`,
  `warning`, or `danger` tones. It inherits the same hover/focus/touch visibility
  and event-isolation contract as the edit/delete shortcuts. Keep
  `ResourceTableEditButton` and `ResourceTableDeleteButton` as the preferred
  semantic shortcuts for edit/delete; destructive confirmation remains
  caller-owned via `confirmDialog({ danger: true })`. The primary resource name
  uses a text-level
  `.k-table-resource-link` action (accent, regular weight, transparent at rest
  and hover), cross-resource references use ordinary cell text, external URLs
  use a concise linked action plus `ExternalLink` icon, resource IDs and fully
  qualified names use ordinary cell text, finite non-status enum values use a
  muted square `.k-badge`, and secondary counts use muted text. Operational and
  lifecycle state use `StatusBadge` with semantic tone; keep verbose provider
  feedback in its title and accessible name rather than as a second visible
  status line. Reserve mono for genuinely technical table content such as schema
  column names and types; providers must not restyle these properties locally.
  `ResourceTable` keeps native table-row
  semantics: interactive `<tr>` elements are focusable with `tabindex="0"`,
  Enter/Space activates the row, and nested links, buttons, inputs, selects,
  summaries, and other explicit controls do not activate the row. Do not turn a
  row into `role="button"`. Labeled actions remain appropriate on detail pages.
  `ResourceTable` has exactly two blessed configurations:
  - **Queryable** (default): the current resource-list contract. Search, filters,
    and client/server pagination remain independently configured; configured
    controls have matching initial-loading skeletons, filters apply to the
    authoritative result set, and query/filter/page changes reset to page one.
    Every existing `ResourceTable` remains Queryable until it is explicitly
    reviewed for conversion; never infer Simple from omitted control props.
  - **Simple** (`variant="simple"`): explicit opt-in for a short, bounded,
    contextual list. It has no search, filters, pagination, controlled query,
    filter values, page, cursor, or page metadata. Loading begins directly with
    the table skeleton. Empty/error, native row semantics, nested-control
    isolation, and row-action accessibility are identical to Queryable.
- **Modals / dialogs.** 6px, surface-raised, hairline, heavy elevation shadow
  allowed. The scrim derives from **surface** (`color-mix(surface 60%)`), never
  from text (a text-derived scrim inverts to white in dark). Use the portalkit
  `confirmDialog()` — never `window.confirm`.
- **Navigation.** Shell/sidebar idle items are muted text on nothing; an active
  shell nav item is accent text on `accent-subtle` + nav glow (`0 0 14px`).
  Provider-level route/section tabs are the separate PortalKit pattern in §10:
  they never glow or shadow. Section headers are 9px mono uppercase with a
  trailing hairline rule.
- **Sidebar rail.** The vertical dock is a **56px icon rail by default** —
  labels are a click away (toggle at the top, state persisted per browser),
  not a permanent tax on the canvas. Collapsed rows are icon-only, centered,
  with a native `title` tooltip; category groups collapse to hairline rules;
  sub-nav children, the tenant chip and the theme switch appear only when
  expanded. The expanded state is the 192px labeled column.
- **Chat bubbles.** Sanctioned 12–14px soft radius, surface-overlay for the
  counterpart, accent-subtle for the user. Bubbles never glow.
- **Empty states.** Contour-grid texture + eyebrow + one-line explanation +
  one primary action. An empty screen is an invitation, not an apology.
- **Toggles / checkboxes / radios.** Native checkboxes and radios inherit
  `accent-color: var(--color-accent)` from `body` — never restyle them with a
  raw blue. Custom toggle switches are **sharp**: 3px track
  (`bg-accent` on / `bg-border-default` off), 2px `bg-text-primary` knob —
  not the iOS pill.
- **Progress bars.** 2px (`rounded-xs`) track in `surface-overlay`, semantic
  fill. Not pills.
- **Modal scrims.** `bg-surface/60` (Tailwind) or
  `color-mix(in srgb, var(--color-surface) 60%, transparent)` (CSS) —
  surface-derived so it stays dark-on-dark / light-on-light. Never `bg-black/*`
  and never text-derived.
- **Skeletons.** `.shimmer` blocks in the exact geometry of the loaded state.
- **Motion.** `stagger-in` on entry, `live-pulse` on live dots, 120–200ms
  eases on hover/focus. Nothing else moves. Respect `prefers-reduced-motion`.

## 7. Theming mechanics

- Exactly one of `html.dark` / `html.light` is always set. Pre-paint script in
  `portal/index.html` (unset preference → **dark**, not system); runtime store
  in `portal/src/stores/theme.ts` (`dark → light → system` cycle).
- No Tailwind `dark:` variant anywhere — theming is pure CSS-variable flips.
  If you ever need the variant, read the warning comment in `main.css` first.
- Never use `@media (prefers-color-scheme)` in portal styles — it fights the
  class toggle. (Standalone dev-harness pages under `providers/*/portal/public/`
  are the only exception; they have no toggle.)
- New tokens are added to BOTH the `@theme` base and the `html.light` block, and
  documented in §2. A token that exists in one theme only is a bug.

## 8. Sanctioned exceptions

| Exception | Why |
|---|---|
| Chat bubbles at 12–14px | Conversational voice, not chrome |
| Terminal canvas colors (`TerminalDock.vue` pins the dark palette) | Terminals are always dark; strip reads as one intentional dark surface |
| App preview iframes (white bg) | The user's app owns its own canvas |
| Third-party brand icon tiles (Google/GitHub/etc. on the Dex page) | Brand guidelines beat ours inside a 20px tile |
| Kuery graph `RELATION_COLORS` | Semantic edge palette, not UI chrome |
| Decorative blurred accent orbs (`blur-[140px]` circles on login/404) | Ambient ground texture, below the glow rule's radar |
| Dex auth pages are **dark-only** (`hack/dex/web/static/`) | Standalone pages with no theme toggle; they pin the dark palette via a local `--faros-*` namespace whose values must track §2's dark column |

Anything not on this list follows the system.

## 9. Provider portals — how the system reaches them

Two integration modes, one look:

1. **Host-compiled** (infrastructure): `.vue/.ts` files are pulled into the
   host Tailwind scan via `@source` in `main.css`. Utilities, tokens and
   radius remap all come from the host. A new provider of this kind must be
   added to the `@source` list.
2. **Self-contained** (code, kuery, app-studio, edges, agents, databricks,
   quickstart): ship their own namespaced CSS. Rules: colors only
   via `var(--color-*)` (cascades in), fallbacks = dark-base values, every
   selector namespaced under `faros-provider-{name}`, radii written per the
   law (or `--radius-*` overrides repeated if they compile their own
   Tailwind), recipes mirror §5 exactly.

**Portalkit** (confirm dialogs, ResourceTable, StatusBadge, tenant helpers) is
canonical in `provider-sdk/portalkit` (vanilla TS) and
`provider-sdk/portalkit-vue` (SFC). The shared provider-tab recipe is in the
canonical `provider-sdk/portalkit/faros-ui.css`; `tabs.ts` and
`provider-sdk/portalkit-vue/Tabs.vue` provide class/component helpers. Edit
canonical files, then run `make sync-portalkit`. Never edit the vendored copies
under `*/src/portalkit/` — CI's `verify-portalkit` fails on drift.

Standalone bundles call `ensureFarosUIStyles()` before rendering. It first
accepts a host stylesheet whose computed `:root` marker
`--faros-ui-canonical: 1` is present, or an existing `#k-faros-ui` style. If
neither exists, it appends the exact vendored stylesheet as a
`data-faros-ui-source="portalkit-fallback"` fallback. It never replaces or
mutates an existing style element, so a provider's older fallback cannot
overwrite the host's canonical CSS.

## 10. Extended component specs

Audited Aug 2026, implemented as shared recipes where marked. **Do not
improvise these.** Implemented ones live in `faros-ui.css` (§5) or the
portalkit; the rest are build-to-this specs for when a consumer appears.

### Tooltip — ✅ implemented as `[data-k-tip]` (faros-ui.css)
Native `title=` remains acceptable for plain icon labels; `data-k-tip` is the
styled variant.
- Geometry: 4px radius, `padding: 4px 8px`, `max-width: 260px`, offset 6px
  from anchor, no arrow (hairline box, not a speech bubble).
- Surface: `surface-overlay` bg, `border-subtle` hairline,
  `0 4px 12px rgba(0,0,0,.35)` elevation (light: `.10`).
- Type: 11px `text-primary`. Never more than two lines — longer content is a
  popover.
- Behavior: 300ms show delay, 0ms hide; shows on focus as well as hover;
  never glows.

### Toast / snackbar — ✅ implemented as `portalkit/toast.ts`
Framework-free bus + renderer, vendored into every portal. The agents
provider's lit host (`providers/agents/portal/src/ui/toast.ts`) predates it
and renders the identical recipe; it can migrate opportunistically. Contract:
- Geometry: 6px radius card, bottom-right stack, `gap: 8px`, max 3 visible.
- Surface: `surface-raised`, `border-default` hairline,
  `0 12px 34px rgba(0,0,0,.4)` elevation. Tone is carried by the leading
  **icon** in the semantic color (success / danger / info = accent); the error
  variant additionally turns the card border `danger`. No tinted backgrounds.
- Type: 13px `text-primary` message; optional 10px mono uppercase eyebrow for
  the source ("EDGES", "BUILD").
- Behavior: auto-dismiss `ok` in 4s, `info` in 6s, and `error` in 9s; pause on
  hover and re-arm with the full duration on leave. Every card has an explicit
  dismiss button; the host uses `role="status"` (`role="alert"` for errors).
  Entry is a slide-up fade (`k-toast-in`); toasts never glow. The Agents
  adapter delegates DOM, timers, actions, and the visible-item cap to this
  canonical bus while retaining its subscription API and reconciling renderer
  removals.

### Dropdown / context menu — ✅ implemented as `.k-menu` (faros-ui.css)
App-studio's `PreviewActionsMenu` / `ResponseModePicker` /
`ApprovalModePicker` follow the same geometry with local Tailwind classes.
- Panel: 6px radius, `surface-raised`, `border-subtle`, `shadow-2xl`-class
  elevation, `padding: 4px` (agents popover: `0 12px 34px rgba(0,0,0,.4)`).
- Items: 4px radius (`rounded-md`), `padding: 6px 8px`, 12px
  `text-secondary`; hover = `surface-overlay` bg + `text-primary`; active/
  selected = `accent-subtle` bg + `accent` text, NO glow (menus aren't nav).
- Destructive items: `danger` text, `danger-subtle` hover bg, separated by a
  hairline `border-subtle` divider.
- Keyboard: arrows + Home/End, Escape closes, focus returns to the trigger.

### Layout selector — ✅ implemented as `portalkit-vue/LayoutSelector.vue`

Use the shared selector when the same resource collection has grid and list
presentations. It is a controlled component (`modelValue` plus
`update:modelValue`) with exactly two stable values: `grid` and `list`. The
optional persistence helper validates stored values, defaults to `grid`, and
treats unavailable or failing browser storage as a non-fatal preference miss.

- Trigger: compact current-layout icon plus chevron, with `aria-haspopup`,
  `aria-expanded`, `aria-controls`, and an accessible name that includes the
  current mode. Focus uses a crisp accent outline; it never glows.
- Menu: visible mono-uppercase Layout label and `role="menu"`; Grid and List
  are `role="menuitemradio"` with `aria-checked`. Selection uses the standard
  `accent-subtle` menu state and no glow.
- Keyboard: click, Enter, and Space select; closed ArrowDown/ArrowUp opens on
  the first/last item; open arrows wrap; Home/End jump; Escape closes and
  restores trigger focus. Tab closes after normal focus movement without a
  focus trap. Pointer or focus movement outside closes the menu.

### Provider route tabs — ✅ implemented as PortalKit `Tabs`

This is the labeled provider-level route/section navigation used by Agents, App
Studio, Code, Databricks, Edges, and Kuery. The canonical recipe is in
`provider-sdk/portalkit/faros-ui.css`; `tabs.ts` and
`provider-sdk/portalkit-vue/Tabs.vue` provide helpers, and
`make sync-portalkit` vendors the exact assets into the portals. Infrastructure
and Quickstart have no equivalent provider-level bar.

- Markup: a labeled nav with an icon + label and an optional count. Counts are
  square 3px-radius mono tags; tabs use the 4px control radius.
- Geometry: `padding: 7px 13px`, 4px gap, and a 1px bottom hairline. Narrow
  hosts keep the row horizontal and allow overflow.
- States: idle is muted text on transparent; hover is `surface-hover`; active
  is accent text on `accent-subtle`; focus-visible is a 2px outline. Tabs have
  no glow or shadow.
- Semantics: each tab is `type="button"` and the active tab exposes
  `aria-current="page"`. Routing remains caller-owned; Vue `Tabs` emits
  `select` and exposes each id as `data-k-tab-id`.

Detail/workbench tabsets are not automatically provider-route tabs; apply this
spec when the tabset is the provider-level route/section bar.

### Resource instance pages — ✅ implemented with PortalKit

`ResourcePage`, `ResourceStatCards`, and `ResourceSectionCard` form the shared
composition for provider resource instance screens. The caller owns navigation
and resource-specific content:

- Keep the backlink as a caller-owned hyperlink before and outside the
  `ResourcePage` shell. Detail routes hide the provider-level collection tabs;
  the backlink is the single return affordance for the resource list.
- `ResourcePage` owns the title hierarchy and read-state shell. Its canonical
  PortalKit title is responsive from 24px to 32px, with tight tracking and
  leading, safe wrapping for long identifiers, and a 22px mobile size. Header
  content has one fixed order: title → optional resource `kind` →
  caller-provided context (`#meta`) → optional status (`#status`) → optional
  subtitle, with PortalKit-owned dot separators between metadata items. The
  header-side region contains actions only and follows that stack in source
  order.
  ResourcePage exposes `kind` as its only resource-type prop; section cards may
  continue to use their independent `eyebrow` label. Callers must not add
  provider-local title-size overrides. Header actions use one stable order:
  provider-specific primary action, `Refresh`, then an overflow menu containing
  `Delete`.
- Use `ResourceStatCards` for provider-defined, meaningful facts with
  provider-chosen icons. Default density keeps the existing card geometry
  unchanged. The `density="compact"` option is opt-in for fact-heavy summaries.
  Both densities use the same responsive grid: three columns, then two, then
  one at narrower widths. These facts are not a universal resource field
  schema.
- Put product-facing content first in vertically stacked `ResourceSectionCard`
  cards. Providers own each card's content and optional actions. Section action
  buttons may use a leading Lucide icon with a visible label; icon-only actions
  are not the default. Technical details are optional rather than a mandatory
  final card. Providers may promote Conditions or health into an always-visible
  product-facing section and omit raw configuration, metadata, or YAML when it
  adds end-user noise. When technical details are shown, keep them closed by
  default and limited to sanitized configuration, health, metadata, or a
  read-only object snapshot; credentials, tokens, and other secrets do not
  belong there.
- `ResourceSectionCard` also supports a headerless body, which lets legacy or
  template-driven groups keep their existing content without inventing a
  second container. The shared card owns border-box containment (`width: 100%`,
  `min-width: 0`); wide tables keep their controls and card width stable while
  the table canvas scrolls internally.
- Primary header anchors that use an accent background retain the
  `text-on-accent` contrast token in both normal and hover states; changing the
  background to `accent-hover` must not reduce readable contrast.
- The read contract distinguishes first-load loading and error states from a
  later refresh failure. A successful snapshot remains visible when a later
  read fails, with a stale/error notice and `Retry`; `ResourcePage` emits retry
  and the caller owns the fetch. Initial failures expose the same retry path.

#### Resource reads and background refresh

Resource pages, resource tables, and dashboard resource summaries share the
`ResourceReadState` contract from PortalKit. `refreshMode` is either
`foreground` or `background`. A first read may use a skeleton or pending state,
but once a populated snapshot—or an authoritative empty snapshot—exists, keep
it visible through every later background read and transient failure. A
background read must not replace an empty or no-match body, spin or disable
header actions, or otherwise disturb the useful surface. An out-of-flow
`aria-busy` indicator or live status is appropriate when it helps communicate
that a refresh is running.

User `Refresh`, `Retry`, query, filter, and page actions are foreground reads:
show immediate feedback even when the request is queued behind another read.
Reads serialize. A timer request does not invalidate a useful active read; at
most one follow-up is coalesced, with foreground priority. Explicit authority
or resource/tenant/user identity invalidation fences stale results. Token
rotation alone is not an identity change. Reset snapshots only when the
tenant, user, or resource identity changes. Stop or unmount must cancel the
timer and queued work.

Use a slower cadence for stable resources (current providers use about 30s) and
a faster, provider-appropriate cadence for unsettled or error states. Keep
read-state ownership in the canonical PortalKit `ResourcePage`,
`ResourceTable`, and `page-state.ts` surfaces; edit canonical sources first,
then run `make sync-portalkit` so vendored portal copies stay synchronized.

Adoption is intentionally lossless. Before moving a resource to this
composition, inventory every legacy field, custom workflow/action/editor/table,
read or mutation state (including stale and deleting states), and sensitive-data
boundary. The shared layout must not flatten or discard provider-specific
content; providers remain responsible for choosing meaningful stat cards,
sections, editors, tables, and actions. Secrets and credential values are never
rendered, even when a provider exposes a credential reference or edit workflow.

Current consumers cover Code Repository and Connection; Edge and Edge Service;
Databricks Connection, Warehouse, and Table; Infrastructure Application
Template Instance; and MCP Access. These nine consumers inherit the canonical
title hierarchy. They are adoption examples, not a universal field schema.
The canonical Vue sources live under `provider-sdk/portalkit-vue`; edit them
there and run `make sync-portalkit` to update provider copies.

### Resource creation

Use a focused, route-owned flow when creating an independently managed resource
or when creation requires prerequisites, sensitive input, multiple meaningful
decisions, or follow-up progress.

Use a dialog, drawer, or inline control for compact additions whose meaning
depends on the current parent. Do not insert substantial creation forms into
collection pages where they reflow or compete with the collection.

Choose the surface based on the user's task—not field count, API shape, or
implementation convenience. Use the operation's truthful domain verb, such as
**Connect**, **Provision**, or **Deploy**.

Use readable, provider-owned creation routes. Avoid collisions between action
routes and valid resource identifiers; prefer `/create/<resource-type>` when
existing detail routes use `/<collection>/:name`.

Route-owned creation uses one back action, one page title and description, one
principal form surface, and a right-aligned **Cancel → primary action** footer.
Simple forms are constrained; dense provisioning forms may use the full content
column. Wizards keep this page skeleton and place progress inside it rather than
retaining dialog chrome.

After creation, navigate to the resource when it owns status or recovery;
otherwise return to the collection with the result clearly visible.

This is the target standard. Existing creation flows will adopt it
incrementally.

### Select / combobox
- Closed control: exactly `.k-input` (4px, overlay bg, focus ring + glow) with
  a 3.5px chevron in `text-muted`. Native `<select>` popups cannot be styled —
  that's fine; the OS popup is sanctioned. `accent-color` themes what it can.
- If search/multi-select is ever needed, build a combobox as: `.k-input`
  trigger + the dropdown-menu panel above + `.k-badge`-style tags for selected
  values. Never a third visual language.

### Checkbox / radio
Native inputs + `accent-color: var(--color-accent)` (inherited from `body`) is
the system default — keep it; don't hand-draw controls for standard forms.
For a dense native checkbox that sits inside a composite control, use the
canonical `.k-checkbox` reset in `provider-sdk/portalkit/faros-ui.css`: 14×14px,
zero min dimensions, padding, and margin, native accent color, and no ordinary
focus shadow. Its `:focus-visible` treatment is only the compact 3px
`accent-subtle` ring — never `accent-glow`. Keep the composite row (for example
the `treeitem`) as the keyboard focus owner; a visually present checkbox may be
`tabindex="-1"`/`aria-hidden="true"` and route pointer activation back to that
row. Label: 12px `text-secondary`, gap 8px.

### Toggle switch — ✅ implemented as `.k-toggle` (faros-ui.css)
Sharp: 3px track (`bg-accent` when `aria-checked="true"`, `border-default`
off), 2px `text-primary` knob, standard focus ring. SkillsWorkbench's inline
Tailwind toggle matches the same shape language.

### Progress bar — ✅ implemented as `.k-progress` (faros-ui.css)
2px-radius `surface-overlay` track, semantic fill (`__bar` +
`--accent/--warning/--danger`), width transition. Not a pill.

### Avatar — ✅ implemented as `.k-avatar` (faros-ui.css)
Mono-initials circle, 28px (or `--sm` 20px); presence = 6px `success` dot
with `.live-dot`. The mono email chip remains preferred for identity.

### `<kbd>` shortcut hint — ✅ implemented as `.k-kbd` (faros-ui.css)
Mono 9px uppercase key-cap, 3px radius, `surface-overlay`, hairline with a
darker bottom edge. Combos are separate kbds joined by a `text-muted` "+".

### File dropzone — ✅ implemented as `.k-dropzone` (faros-ui.css)
Dashed hairline, verb-first copy ("Drop a file, or browse"); `.is-dragover` =
accent dashed border + `accent-subtle` tint (a target, not an action — no
glow); `.is-error` = danger tones. Progress uses `.k-progress`.

### Slider (range input)
None exist yet. When needed: native `<input type="range">` with
`accent-color` as the baseline; custom variant is a 2px `surface-overlay`
track (`rounded-xs`), `accent` filled portion, 12×12px square 2px-radius
`text-primary` thumb (matches the toggle knob), focus = standard ring. Value
readouts are mono `tabular-nums`.

### Pagination
`portalkit/ResourceTable.vue` owns filtering and true pagination for bounded
resource lists. Opt in with `searchable`, `filters`, `paginated`, and
`page-size`; it searches and filters the complete loaded row set before slicing
the visible page. A filter or page-size change returns to page one, while polling
retains the current page when it remains valid. The shared presentation is:
- 4px-radius ghost icon-buttons (‹ ›) + mono `tabular-nums` "12–24 of 96" label
  in `text-muted`.
- Current page indicator in `accent-subtle` with `accent` text. No number soup.
- Search plus compact categorical selects above the table; one `Clear filters`
  action appears only when a filter is active.
- For wide tables, only the table canvas scrolls horizontally. Search/filter
  controls and the pagination footer remain pinned to the full card width.
- Prefer "Load more" (a `.k-btn--ghost`) or infinite scroll for streams such as
  activity/event feeds; do not force page navigation onto an append-only flow.

For a cursor-backed resource list, set `pagination-mode="server"` and control
the table with `page`, `page-size`, `query`, `filter-values`, `cursor`, and
`page-info`. Handle the typed `change` event to fetch the supplied page. Server
mode renders the supplied rows as-is; it does not apply local search, filters,
or slicing. Cursor values are opaque, and `page-info` should expose only the
next-page state the backend actually returned—never an exact total inferred
from a remaining-item count.

### Date / time picker
None exists — all dates are read-only mono output via `portal/src/utils/time.ts`
(keep it that way; timestamps DISPLAY in mono `tabular-nums`, relative + title
absolute). For input, use native `<input type="date/datetime-local">` styled
as `.k-input`; do not build a custom calendar. If a range is ever needed, two
inputs joined by an en-dash, not a popover calendar.

### Command palette (⌘K)
The topbar advertises ⌘K; if/when implemented: centered 560px panel, 6px
radius, `surface-raised`, hairline, heavy elevation, `surface/60` scrim; the
input is a borderless 14px `.k-input` variant with a mono ⌘K kbd at the right;
results are dropdown-menu items with a 10px mono uppercase group eyebrow.
This is the one surface allowed to feel "bigger" than the rest of the chrome —
but still no gradients, still square-ish, still one accent.

### Still-open oddities
- **Kuery graph relation palette** (`graph.ts` `RELATION_COLORS`) is a
  hand-picked categorical set — sanctioned as data-viz, but unvalidated for
  contrast/colorblind safety in both themes; revisit deliberately.
- **Edges' `<select>`** keeps `appearance: auto` on purpose (native popup UX);
  its closed control must still carry `.svc-input`/`.k-input` styling.

## 11. Iconography

One family, everywhere: **Lucide**. Vue portals import from
[`lucide-vue-next`](https://lucide.dev); vanilla-TS portals use `ic('name')`
from `portalkit/icons.ts` — a hand-inlined, CSP-safe subset of Lucide-style
stroke paths that renders at `1em` in `currentColor` (canonical in
`provider-sdk/portalkit`; extend it there and run `make sync-portalkit`).

**Never Unicode glyphs, never emoji.** Characters like `⚙ ☁ ✦ ⚠` look like
quiet monochrome icons on macOS but carry emoji presentation variants — on
Windows/Android they render as full-color emoji, and their weight/optical size
is whatever the platform's symbol font decides. Lucide renders identically
everywhere and inherits color like text. (The design-exploration mocks used
glyphs as placeholders; that is not a license.)

### Taste: abstract over literal

Prefer the thin, geometric, slightly abstract mark over the literal pictogram —
the machine speaks in symbols, not clip-art. The sanctioned nav/brand
vocabulary: `Hexagon` (brand), `Diamond`, `Zap`/`Activity`, `Sparkles` (AI),
`Command`, `Target`, `Boxes`. A literal object icon (`Cloud`, `Server`,
`Database`) is fine when it names a real thing; reach for the geometric one
when the concept is abstract.

### Stroke & size law

Stroke width compensates optically for size — small icons need heavier
strokes to hold their weight, large decorative ones need lighter:

| Context | Size | Stroke |
|---|---|---|
| Standard UI rows, buttons, table actions | `h-4 w-4` (16px) | `1.75` (the default) |
| Dense rows, sub-nav, chips | `h-3.5 w-3.5` (14px) | `1.75`–`2` |
| Micro: category eyebrows, badge glyphs, tiny brand marks | `h-3 w-3` and below | `2`–`2.5` |
| Large decorative: empty states, hero tiles, nav-rail brand | 20px+ | `1.25`–`1.5` |

Icons inherit `currentColor` from their row/button — an icon never sets its
own color except the semantic status set below, and an icon never glows (glow
belongs to the active row or button, per §1.3).

### Semantic vocabulary

Don't improvise synonyms — these pairings are load-bearing across every
portal:

| Meaning | Icon |
|---|---|
| Loading / in-flight | `Loader2` + `animate-spin` (the only spinner) |
| Success outcome | `CheckCircle` · inline confirm `Check` |
| Failure outcome | `XCircle` · inline dismiss/cancel `X` |
| Warning / degraded | `AlertTriangle` |
| Error detail / info-error | `AlertCircle` |
| Create / add | `Plus` |
| Delete | `Trash2` |
| Empty state | `Inbox` |
| Pending / time | `Clock` |
| Refresh / retry | `RefreshCw` |
| Provider (no logo) | `Puzzle` |
| AI / assistant | `Sparkles` |
| Brand | `Hexagon` |

### Provider identity icons

Providers ship a square `icon.svg` in their portal and declare
`iconURL: "/ui/providers/<name>/icon.svg"` in `manifest.yaml`; the hub serves
it through the UI proxy and the host nav renders it at 14px
(`object-contain`). Registered *categories* resolve to a Lucide component
name via `portal/src/lib/categoryIcons.ts`; providers without a logo fall
back to `Puzzle`. Logos should read at 14px on both grounds — prefer
stroke-style marks in a single color; full-color brand logos are sanctioned
only per §8 (third-party brand tiles).

## 12. Review checklist

Before merging any UI change:

- [ ] No raw hex/rgb outside §8 exceptions; no dead Precision-Flat accents.
- [ ] No new `border-radius` outside {2,3,4,6,8,12px, circles}; no pills.
- [ ] Badges are square mono tags; status maps to the §6 tone table.
- [ ] Exactly the sanctioned things glow; danger never glows.
- [ ] Provider-level route/section tabs use PortalKit `Tabs`: caller-owned
      routing, `aria-current="page"`, and no tab glow/shadow.
- [ ] Works in BOTH themes (toggle it — don't trust the default).
- [ ] Uses `k-*` / portalkit primitives instead of re-derived markup.
- [ ] No per-page `max-w-*` wrapper (width is owned by `AppLayout`).
- [ ] The creation surface matches the task: focused and route-owned for
      independently managed resources; contextual for compact, parent-dependent
      additions. Route-owned flows use the canonical creation skeleton, and
      substantial forms do not reflow collection pages.
- [ ] Mono for identifiers; tabular-nums for aligned digits.
- [ ] Icons are Lucide (or portalkit `ic()`) per §11 — no emoji, no Unicode
      glyph icons; stroke/size on the law; only status icons carry color.
- [ ] `prefers-reduced-motion` respected for any new animation.

---

*History: adopted Aug 2026, replacing "Precision Flat" (12/8px radii, pill
badges, light-default, quiet `#6d4fe0`/`#7c5bf5` accents). The exploration
that led here — four candidate directions mocked in both themes — lives in the
team's design-book artifact; option A "Violet Circuit" won for keeping the
brand while killing the softness.*
