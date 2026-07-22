# portalkit — shared portal UI primitives

Canonical source for UI primitives shared across the **string-building
(vanilla-TS) provider portals**: `agents`, `kuery`, `quickstart`.

- `icons.ts` — inline SVG icon set (`ic(name)` returns an `<svg>` string). Use in
  HTML template literals instead of emoji.
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

which copies them into every vanilla-TS portal's `src/portalkit/`. CI can run
`make sync-portalkit && git diff --exit-code` to guard against drift.

The Vue portals (`app-studio`, `code`, `databricks`, `edges`, `infrastructure`,
and the root `kedge-portal`) use `lucide-vue-next` for icons and the
`confirm.ts` + `ConfirmDialog.vue` pattern for modals — they do not consume this
kit.
