# portalkit — shared portal UI primitives

Canonical source for framework-neutral UI primitives shared across provider
portals. The vanilla-TS kit is vendored into the string-building portals
`agents`, `kuery`, and `quickstart`; Vue portals consume the shared plain assets
too, alongside the SFC kit in `provider-sdk/portalkit-vue`.

- `icons.ts` — inline SVG icon set (`ic(name)` returns an `<svg class="k-icon">`
  string). Use in HTML template literals instead of emoji.
- `tabs.ts` — framework-neutral tab class helpers for labeled provider-level
  route/section navigation. The shared `.k-tabs` recipe (including
  icon-plus-label tabs, optional square mono counts, active/hover/focus states,
  and narrow-host overflow) lives in the canonical
  `provider-sdk/portalkit/faros-ui.css`. The Vue
  counterpart is `provider-sdk/portalkit-vue/Tabs.vue`; it emits `select` and
  exposes `data-k-tab-id`, while routing remains caller-owned.
- `modal.ts` — promise-based `confirmModal()` / `alertModal()`, replacing native
  `window.confirm` / `window.alert` with an on-brand in-page dialog.
- `toast.ts` — framework-free toast bus. It emits the `.k-toast` recipe and
  uses the shared stylesheet rather than injecting a second visual language.
- `styles.ts` — the standalone handoff for the exact canonical
  `provider-sdk/portalkit/faros-ui.css` bytes.

## Why vendored, not imported

The portals have **no npm workspace** and each must build **self-contained**
(standalone Docker context, no parent `node_modules`). So the kit is **copied**
into each portal at `src/portalkit/` and committed, rather than imported across
package boundaries.

`faros-ui.css` is the canonical stylesheet and the host copy at
`portal/src/assets/faros-ui.css` plus each vendored `src/portalkit/faros-ui.css`
must remain byte-identical. `make verify-portalkit` checks the manifest, the
host copy, every portal copy, and unexpected files. Standalone helpers call
`ensureFarosUIStyles()`: a computed `:root` marker
`--faros-ui-canonical: 1` is accepted only when its `--faros-ui-version` is at
least the bundle's required version. Otherwise the exact vendored stylesheet
is appended under a versioned fallback ID with
`data-faros-ui-source="portalkit-fallback"`. Existing style elements are never
replaced, and a newer host stylesheet is never downgraded.

## Editing

Edit the files **here**, then run:

```
make sync-portalkit
```

which copies the vanilla kit into the string-building portals and the Vue SFC
kit into the Vue portals. It also copies the canonical `faros-ui.css` into every
vendored kit directory and verifies that no unexpected asset is present. CI can
run `make sync-portalkit && git diff --exit-code` to guard against drift.

The Vue portals (`app-studio`, `code`, `databricks`, `edges`, `infrastructure`,
and the root `faros-portal`) use `lucide-vue-next` for icons and the
`confirm.ts` + `ConfirmDialog.vue` pattern for modals. Agents, App Studio,
Code, Databricks, Edges, and Kuery use the provider-level tab bar;
Infrastructure and Quickstart have no equivalent provider-level bar. Vue
consumers can use the vendored `Tabs.vue` component; its `select` event reports
the selected id and leaves routing to the caller.

## Provider-level tabs

Use the shared `.k-tabs` recipe for labeled provider route/section navigation.
The standard is a 4px-radius tab with `padding: 7px 13px`, a 4px gap, muted
text on transparent at idle, `surface-hover` on hover, accent text on
`accent-subtle` when active, a 1px bottom hairline, and a 2px focus-visible
outline. Optional counts are 3px-radius mono tags. The row stays horizontal and
overflows on narrow hosts; tabs have no glow or shadow. Mark active tabs with
`aria-current="page"` and use `type="button"`. Detail/workbench tabsets are not
automatically provider-route tabs. Callers own route mapping and state.

## ResourceTable contract

`provider-sdk/portalkit-vue/ResourceTable.vue` keeps native `<table>` and `<tr>`
semantics. Interactive rows use `tabindex="0"`; Enter or Space emits `rowClick`.
Nested links, buttons, inputs, selects, summaries, and other explicit controls
remain independently interactive and do not activate the row. Do not assign
`role="button"` to a table row. The shared edit/delete row actions remain
keyboard-focusable and use the canonical confirmation dialog for destructive
actions.

## Toasts

The canonical `toast.ts` bus auto-dismisses `ok` after 4s, `info` after 6s, and
`error` after 9s; hover pauses and re-arms the full duration on leave, and every
card has an explicit dismiss button. Agents keeps its subscription-facing
adapter for existing Lit callers, but delegates DOM rendering, timers, actions,
and visible-item eviction to this bus and reconciles renderer removals.
