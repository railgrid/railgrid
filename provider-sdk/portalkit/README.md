# portalkit — shared portal UI primitives

Canonical source for framework-neutral UI primitives shared across provider
portals. The vanilla-TS kit is vendored into the string-building portals
`agents`, `kuery`, and `quickstart`; Vue portals consume the shared plain assets
too, alongside the SFC kit in `provider-sdk/portalkit-vue`.

- `icons.ts` — inline SVG icon set (`ic(name)` returns an `<svg>` string). Use in
  HTML template literals instead of emoji.
- `tabs.css` + `tabs.ts` — framework-neutral tab recipe and class helpers for
  labeled provider-level route/section navigation. The recipe includes
  icon-plus-label tabs, optional square 3px-radius mono counts,
  active/hover/focus states, and horizontal overflow for narrow hosts. The
  Vue counterpart is `provider-sdk/portalkit-vue/Tabs.vue`; it emits `select`
  and exposes `data-pk-tab-id`, while routing remains caller-owned.
- `modal.ts` — promise-based `confirmModal()` / `alertModal()`, replacing native
  `window.confirm` / `window.alert` with an on-brand in-page dialog.

## Why vendored, not imported

The portals have **no npm workspace** and each must build **self-contained**
(standalone Docker context, no parent `node_modules`). So the kit is **copied**
into each portal at `src/portalkit/` and committed, rather than imported across
package boundaries.

## Editing

Edit the files **here**, then run:

```
make sync-portalkit
```

which copies the vanilla kit into the string-building portals and the Vue SFC
kit into the Vue portals. The shared plain tab assets are also copied into the
Vue portals. CI can run `make sync-portalkit && git diff --exit-code` to guard
against drift.

The Vue portals (`app-studio`, `code`, `databricks`, `edges`, `infrastructure`,
and the root `faros-portal`) use `lucide-vue-next` for icons and the
`confirm.ts` + `ConfirmDialog.vue` pattern for modals. Agents, App Studio,
Code, Databricks, and Edges use the provider-level tab bar; Infrastructure,
Kuery, and Quickstart have no equivalent provider-level bar. Vue consumers can
use the vendored `Tabs.vue` component; its `select` event reports the selected
id and leaves routing to the caller.

## Provider-level tabs

Use the shared tab assets for labeled provider route/section navigation. The
standard is a 4px-radius tab with `padding: 7px 13px`, a 4px gap, muted text on
transparent at idle, `surface-hover` on hover, accent text on
`accent-subtle` when active, a 1px bottom hairline, and a 2px focus-visible
outline. Optional counts are 3px-radius mono tags. The row stays horizontal and
overflows on narrow hosts; tabs have no glow or shadow. Mark active tabs with
`aria-current="page"` and use `type="button"`. Detail/workbench tabsets are not
automatically provider-route tabs. Callers own route mapping and state.
