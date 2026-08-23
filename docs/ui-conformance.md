# Provider UI conformance

`make verify-ui-conformance` runs the dependency-free Node scanner in
`hack/verify-ui-conformance.mjs` and then its focused fixture tests. The scanner
has no debt baseline: every diagnostic is a migration failure until the source
is repaired.

The stylesheet authority is `provider-sdk/portalkit/faros-ui.css`. The host copy
at `portal/src/assets/faros-ui.css` and the byte-synced `src/portalkit/` copies
are checked separately by `make verify-portalkit`; its manifest canary rejects
an unmanifested canonical file and its copy checks reject missing, stale, or
unexpected assets. Standalone bundles use the shared `styles.ts` handoff: the
computed `:root` marker `--faros-ui-canonical: 1` preserves a host stylesheet;
only when no marker or existing `#k-faros-ui` style is present is the exact
vendored CSS appended as a fallback. Existing style elements are not replaced.

It scans the canonical `provider-sdk/portalkit` and
`provider-sdk/portalkit-vue` roots plus `providers/*/portal/src`. Generated
`dist`, dependency `node_modules`, test files, and byte-synced provider
`src/portalkit` copies are excluded. Roots can be replaced for an isolated
checkout with repeatable `--canonical-root` / `--provider-root` flags or the
`FAROS_UI_CONFORMANCE_CANONICAL_ROOTS` and
`FAROS_UI_CONFORMANCE_PROVIDER_ROOTS` comma-separated environment variables.

The gate reports legacy pre-k hooks (current tabs expose `data-k-tab-id`),
provider CSS redeclarations of `.k-*`,
native dialogs, Unicode/emoji used as icon content, unknown `--color-*` tokens,
raw colors, pill/soft radii, and provider-local common-widget selectors. Arrow
notation in ordinary prose (`A → B`, keyboard hints, and explanatory copy) is
not an icon diagnostic; an icon-only element, an edge affordance in a
button/link, an icon-ish class/ARIA context, or a CSS/DOM content assignment is
required for symbol matching. The common-widget rule is intentionally narrow:
page-composition names such as `header`, `form`, `field`, and `list` are not
enough, while a focused primitive name with a visibly restated widget recipe
is actionable. Output is sorted by repository-relative path, line, column, and
rule.

The only suppression mechanism is the structured
`hack/ui-conformance-exceptions.json` registry. Each entry must name a single
rule, exact repository-relative path, line, column, and source substring, plus
a `design-book §...` reference and reason. The scanner validates that the
locator still matches the current source; debt, temporary, legacy, and baseline
wording is rejected. The checked-in entries are limited to the exact Kuery
graph palette/canvas fallbacks sanctioned by design-book §8; true circles are
recognized only when their dimensions prove a circle. Do not add a count
baseline or a broad path exemption.

The verified run for this contract passed all 8 focused scanner tests, scanned
181 source files, and reported zero violations. Those counts describe that run,
not a debt baseline; the durable acceptance rule is that the focused tests pass
and the scanner reports no diagnostics. Browser proofs also covered host marker
preservation, App Studio accent text, Code recipes, native table rows with
nested controls, and Kuery's narrow inventory scroll.
