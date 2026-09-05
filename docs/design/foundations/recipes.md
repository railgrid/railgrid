---
{"schema":1,"id":"design.foundations.recipes","title":"Shared k-* recipes","kind":"recipe","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The canonical stylesheet is copied to the host and each vendored PortalKit bundle."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/foundations/recipes.md#shared-k-recipes","role":"design"},{"path":"provider-sdk/portalkit/faros-ui.css","role":"implementation"},{"path":"hack/sync-portalkit.sh","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[]}
---

# Shared k recipes

`provider-sdk/portalkit/faros-ui.css` is the canonical component vocabulary.
`portal/src/assets/faros-ui.css` and copies under each portal's `src/portalkit/`
are exact sync outputs. `make sync-portalkit` writes them and
`make verify-portalkit` rejects drift or unexpected files. Use these classes
before writing local CSS.

| Class | Contract |
|---|---|
| `.k-card` (`--flat`) | 6px `surface-raised` card, subtle hairline, `0 1px 2px` lift |
| `.k-table` | 6px table wrapper; mono 9–10px uppercase headers, 13px rows, accent-tint hover through `.is-interactive` |
| `.k-cell-mono` | Data-like names, IDs, and timestamps |
| `.k-badge` (`--success/--warning/--danger/--muted`, `__dot`) | Square 3px mono tag, 10px/600 uppercase, `0.06em`, subtle semantic background and `color-mix` hairline |
| `.k-btn` (`--primary/--ghost/--text/--danger`) | 4px control; primary is solid accent plus glow, ghost overlay plus hairline, text transparent, danger tinted and never glowing; all variants reach 44×44px for coarse and hybrid pointers |
| `.k-dashboard-action` | Compact text action inside a dashboard tile; remains visually quiet while reaching 44×44px for coarse and hybrid pointers |
| `.k-spin` | Canonical 0.8s linear loading rotation; becomes static under reduced motion and never replaces status text or the affected region's busy state |
| `.k-back-action` | Intrinsic-width, start-aligned borderless link; 12px/500 accent, 6px icon gap, hover underline, no control surface |
| `.k-input` | 4px overlay input; focus is accent border, 3px subtle ring, and glow |
| `.k-form-select` (`__trigger`, `__panel`, `__option`) | Accessible PortalKit single-select combobox using input/menu recipes |
| `.k-eyebrow` / `.k-kpi` | Tracked uppercase label over expanded tabular numeral |
| `.k-menu` / `.k-menu-item` | Dropdown/context menu; selected is subtle accent and never glows |
| `.k-layout-selector` | Controlled grid/list presentation menu with radio semantics and no glow |
| `.k-kbd` | 9px mono uppercase shortcut key-cap, 3px, darker bottom edge |
| `[data-k-tip="…"]` | CSS tooltip with 300ms delay, hover/focus, 260px max |
| `.k-progress` / `__bar` | 2px-radius track and semantic fill |
| `.k-toggle` / `__knob` | Sharp 3px switch and 2px text-primary knob |
| `.k-avatar` (`--sm`) | Mono-initials circle, 28/20px; presence uses `.live-dot` |
| `.k-dropzone` | Dashed drop target; drag-over accent tint, no glow |

Toasts use the [canonical toast contract](../components/toast.md). Signature
host utilities are `.contour-grid`/`-fade` (sparingly for login and empty
states), `.island` (floating dock card), `.live-dot` (provider-dependent
opacity pulse; never delete), `.shimmer` (skeletons), `.stagger-item`, and
`.type-display`.
