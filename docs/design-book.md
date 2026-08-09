# Kedge Design Book — "Violet Circuit"

The canonical reference for every pixel of kedge UI: the host portal, all
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
vibe-studio, agents) may use 12–14px — speech is not chrome. Nothing else
qualifies.

## 4. Typography

Self-hosted via `@fontsource`, imported in `portal/src/main.ts`. No other
faces, no CDN fonts.

| Role | Face | Usage |
|---|---|---|
| `font-sans` | Instrument Sans Variable | Body, UI copy |
| `font-display` (`.type-display`) | Archivo Variable at `font-stretch: 125%` | Page titles, KPI numerals, the KEDGE wordmark |
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

`portal/src/assets/kedge-ui.css` is the component vocabulary. It cascades into
every light-DOM provider — **use these classes before writing any CSS**:

| Class | What it is |
|---|---|
| `.k-card` (+ `--flat`) | 6px surface-raised card, subtle hairline, `0 1px 2px` lift |
| `.k-table` | 6px table wrapper; mono 9–10px uppercase headers, 13px rows, accent-tint hover via `.is-interactive` |
| `.k-cell-mono` | Data-like cells (names, ids, timestamps) |
| `.k-badge` (+ `--success/--warning/--danger/--muted`, `__dot`) | **Square 3px mono tag**: 10px/600 uppercase, `0.06em`, `*-subtle` bg, `color-mix(currentColor 35%)` hairline |
| `.k-btn` (+ `--primary/--ghost/--danger`) | 4px control; primary = solid accent + glow; ghost = overlay bg + hairline; danger = danger-subtle tint, **no glow** |
| `.k-input` | 4px overlay-bg input; focus = accent border + 3px subtle ring + glow |
| `.k-eyebrow` / `.k-kpi` | Tracked uppercase label over an expanded tabular numeral |
| `.k-menu` / `.k-menu-item` (+ `--danger`, `.is-selected`, `.k-menu-sep`) | Dropdown/context menu panel + items; selection = accent-subtle, no glow |
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
  rows lift text to primary.
- **Modals / dialogs.** 6px, surface-raised, hairline, heavy elevation shadow
  allowed. The scrim derives from **surface** (`color-mix(surface 60%)`), never
  from text (a text-derived scrim inverts to white in dark). Use the portalkit
  `confirmDialog()` — never `window.confirm`.
- **Navigation.** Idle items are muted text on nothing; active = accent text on
  `accent-subtle` + nav glow (`0 0 14px`). Section headers are 9px mono
  uppercase with a trailing hairline rule.
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
| Dex auth pages are **dark-only** (`hack/dex/web/static/`) | Standalone pages with no theme toggle; they pin the dark palette via a local `--kedge-*` namespace whose values must track §2's dark column |

Anything not on this list follows the system.

## 9. Provider portals — how the system reaches them

Two integration modes, one look:

1. **Host-compiled** (infrastructure): `.vue/.ts` files are pulled into the
   host Tailwind scan via `@source` in `main.css`. Utilities, tokens and
   radius remap all come from the host. A new provider of this kind must be
   added to the `@source` list.
2. **Self-contained** (code, kuery, app-studio, edges, agents, databricks,
   vibe-studio, quickstart): ship their own namespaced CSS. Rules: colors only
   via `var(--color-*)` (cascades in), fallbacks = dark-base values, every
   selector namespaced under `kedge-provider-{name}`, radii written per the
   law (or `--radius-*` overrides repeated if they compile their own
   Tailwind), recipes mirror §5 exactly.

**Portalkit** (confirm dialogs, ResourceTable, StatusBadge, tenant helpers) is
canonical in `provider-sdk/portalkit` (vanilla TS) and
`provider-sdk/portalkit-vue` (SFC). Edit there, run `make sync-portalkit`;
never edit the vendored copies under `*/src/portalkit/` — CI's
`verify-portalkit` fails on drift.

## 10. Extended component specs

Audited Aug 2026, implemented as shared recipes where marked. **Do not
improvise these.** Implemented ones live in `kedge-ui.css` (§5) or the
portalkit; the rest are build-to-this specs for when a consumer appears.

### Tooltip — ✅ implemented as `[data-k-tip]` (kedge-ui.css)
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
- Behavior: auto-dismiss 5s (errors 8s, or sticky with an explicit ✕), pause
  on hover, `role="status"` (`role="alert"` for errors), entry = slide-up
  fade (`agents-toast-in`), exit = fade. Toasts never glow.

### Dropdown / context menu — ✅ implemented as `.k-menu` (kedge-ui.css)
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
If a custom one is ever justified (indeterminate states, dense tables):
14×14px, 3px radius (radio: circle), `border-default` 1px, checked =
`accent` fill + white 10px check, focus = the standard 3px `accent-subtle`
ring. Label: 12px `text-secondary`, gap 8px.

### Toggle switch — ✅ implemented as `.k-toggle` (kedge-ui.css)
Sharp: 3px track (`bg-accent` when `aria-checked="true"`, `border-default`
off), 2px `text-primary` knob, standard focus ring. SkillsWorkbench's inline
Tailwind toggle matches the same shape language.

### Progress bar — ✅ implemented as `.k-progress` (kedge-ui.css)
2px-radius `surface-overlay` track, semantic fill (`__bar` +
`--accent/--warning/--danger`), width transition. Not a pill.

### Avatar — ✅ implemented as `.k-avatar` (kedge-ui.css)
Mono-initials circle, 28px (or `--sm` 20px); presence = 6px `success` dot
with `.live-dot`. The mono email chip remains preferred for identity.

### `<kbd>` shortcut hint — ✅ implemented as `.k-kbd` (kedge-ui.css)
Mono 9px uppercase key-cap, 3px radius, `surface-overlay`, hairline with a
darker bottom edge. Combos are separate kbds joined by a `text-muted` "+".

### File dropzone — ✅ implemented as `.k-dropzone` (kedge-ui.css)
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
None exists — lists poll and truncate today. When needed:
- Prefer "Load more" (a `.k-btn--ghost`) or infinite scroll for streams.
- True pagination: 4px-radius ghost icon-buttons (‹ ›) + mono `tabular-nums`
  "12–24 of 96" label in `text-muted`; current page indicator uses
  `accent-subtle` bg + `accent` text like an active tab. No number soup —
  never render more than 5 page buttons.

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
- [ ] Works in BOTH themes (toggle it — don't trust the default).
- [ ] Uses `k-*` / portalkit primitives instead of re-derived markup.
- [ ] No per-page `max-w-*` wrapper (width is owned by `AppLayout`).
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
