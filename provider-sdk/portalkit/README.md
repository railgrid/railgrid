# portalkit — shared portal UI primitives

Canonical source for shared PortalKit assets. The vanilla-TS kit is vendored into
the string-building Quickstart portal; Vue portals use the SFC kit in
`provider-sdk/portalkit-vue` plus shared plain assets where appropriate. Agents
is the temporary toast exception: its Vue portal still receives the frozen
framework-neutral `toast.ts` for its existing compatibility adapter instead of
the Vue toast files.

- `icons.ts` — inline SVG icon set (`ic(name)` returns an `<svg class="k-icon">`
  string). Use in HTML template literals instead of emoji.
- `form-select.ts` — framework-neutral single-select combobox for forms. Set
  `options`, `value`, and optional `placeholder`, `labelledby`, and
  `describedby` properties; it emits one bubbling `change` event whose `detail`
  is the selected value. Its viewport-positioned listbox is portalled to
  `document.body`.
- `resource-table-filter.ts` — framework-neutral finite-select resource facet.
  Set `label`, `allLabel`, `options`, and `value`; it emits the same bubbling
  string-valued `change` contract. Searchable resource-reference facets remain
  an explicit opt-in in the Vue `ResourceTableFilter.vue` counterpart.
- `tabs.ts` — framework-neutral tab class helpers for labeled provider-level
  route/section navigation. The shared `.k-tabs` recipe (including
  icon-plus-label tabs, optional square mono counts, active/hover/focus states,
  and narrow-host overflow) lives in the canonical
  `provider-sdk/portalkit/faros-ui.css`. The Vue
  counterpart is `provider-sdk/portalkit-vue/Tabs.vue`; it emits `select` and
  exposes `data-k-tab-id`, while routing remains caller-owned.
- `modal.ts` — promise-based `confirmModal()` / `alertModal()`, replacing native
  `window.confirm` / `window.alert` with an on-brand in-page dialog.
- `toast.ts` — frozen framework-neutral toast bus retained for Agents' existing
  compatibility adapter. It is not the Vue toast implementation and is copied
  only to Agents.
- `styles.ts` — the standalone handoff for the exact canonical
  `provider-sdk/portalkit/faros-ui.css` bytes.
- `FirstRunGuide.vue` — Vue first-use value, action, and ordered journey
  surface. Vanilla portals emit the same `k-first-run*` classes.
- `CreateGuidance.vue` — Vue prerequisites, live output summary, and next-step
  rail for route-owned forms. Vanilla portals emit the same
  `k-create-guidance*` classes.

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

The Vue portals (`agents`, `app-studio`, `code`, `databricks`, `edges`,
`infrastructure`, `kuery`, and the root `faros-portal`) use `lucide-vue-next`
for icons and the
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

### Vue PortalKit

The Vue toast standard consists of the canonical
`provider-sdk/portalkit-vue/toast.ts`, `ToastHost.vue`, and
`InlineNotification.vue`. The root portal mounts exactly one
`<ToastHost owner="primary" />`. A Vue provider may also mount
`<ToastHost owner="fallback" />` for standalone embeds; a primary host owns the
queue whenever it is present, leaving fallback hosts dormant until takeover.
Ownership takeover preserves the visible toast's remaining timer, action
busy/error state, and focused toast control.

The versioned document command transport crosses independently bundled Vue
portals. Toast copy is rendered as plain text; the active `ToastHost` owns
rendering, the priority queue, timers, and visual lifecycle. Only one toast is
visible at a time. Priority is `error > warning > info > ok`; only a strictly
higher-priority toast preempts the visible toast, which is requeued with its
remaining time.
`ok` lasts 5s and `info` 6s by default. Warnings and errors are persistent by
default, but an explicitly supplied finite numeric duration overrides that
default and clamps to at least 5s. Toasts with an action remain persistent
regardless of duration. A toast has plain text, at most one action, and an
explicit dismiss button. Render `source` only when the caller supplies it;
never infer a provenance label.

The host keeps separate pre-mounted polite and assertive live regions. With
`announcement: "auto"`, errors announce assertively and other kinds politely;
callers may explicitly choose `polite`, `assertive`, or `off`. Focus pauses a
timer just like hover or a hidden document, and resuming preserves remaining
time. Escape dismisses only when focus is within the toast host; focus returns
to the next toast or its recorded origin as the queue changes. The shared CSS
gives coarse-pointer action and dismiss controls 44×44px targets, and removes
toast transitions/spinners under reduced motion. The root portal owns
`--k-toast-bottom-offset`, combining navigation and terminal chrome; shared CSS
applies the safe-area insets.

Use `InlineNotification` beside the operation for contextual failures and
recovery. Do not emit a duplicate toast for the same contextual failure. The
root portal and App Studio use these Vue primitives. Agents remains the
temporary legacy exception even though its portal is Vue: its provider-local
subscription adapter continues to use the frozen framework-neutral bus. Agents
migration and adoption by providers with no current toast usage remain out of
scope.
