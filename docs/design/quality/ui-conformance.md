---
{"schema":1,"id":"design.quality.ui-conformance","title":"Provider UI conformance contract","kind":"policy","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The dependency-free scanner and focused tests enforce the shared UI vocabulary and exact exception registry."},"appliesTo":["portal","provider-portals","portalkit","dex"],"owner":"design-system","canonicalSource":[{"path":"docs/design/quality/ui-conformance.md#provider-ui-conformance-contract","role":"design"},{"path":"hack/verify-ui-conformance.mjs","role":"implementation"},{"path":"hack/verify-ui-conformance.test.mjs","role":"implementation"},{"path":"hack/ui-conformance.config.json","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"command","ref":"make verify-ui-conformance","status":"passing"}]},"relatedDocuments":[]}
---

# Provider UI conformance contract

`make verify-ui-conformance` runs focused fixture tests and the dependency-free
Node scanner. The stylesheet authority is `provider-sdk/portalkit/railgrid-ui.css`.
The host copy at `portal/src/assets/railgrid-ui.css` and vendored `src/portalkit/`
copies are checked separately by `make verify-portalkit`. The manifest rejects
unregistered canonical files and copy checks reject missing, stale, or
unexpected assets.

Standalone bundles use the shared `styles.ts` handoff. The computed
`--railgrid-ui-canonical: 1` marker preserves a host stylesheet only when its
`--railgrid-ui-core-version` is compatible with the current `RAILGRID_UI_CORE_VERSION` contract. A
stale or unversioned host remains untouched while canonical CSS imported
through Vite's `?inline` loader is appended under a versioned fallback ID.
Vite may minify that runtime fallback; the authored stylesheet and synced
source copies remain byte-identical. Existing style elements are never
replaced, and newer host CSS always wins.

The scanner covers canonical `provider-sdk/portalkit` and
`provider-sdk/portalkit-vue` roots plus host and `providers/*/portal/src` source.
Generated `dist`, dependency `node_modules`, test files, and byte-synced
provider copies are excluded. Repeatable `--canonical-root` and
`--provider-root` flags, or matching comma-separated environment variables,
support isolated checkouts without weakening provider scanning.

Diagnostics cover legacy pre-k hooks, provider CSS redeclarations of `.k-*`,
native dialogs, Unicode/emoji used as icon content, unknown `--color-*` tokens,
raw colors, pill/soft radii, and provider-local common-widget selectors. Arrow
notation in prose and keyboard hints is not an icon violation; icon context,
edge affordance, icon-ish class/ARIA context, or CSS/DOM content assignment is
required. Common-widget matching is intentionally narrow: page-composition names
such as `header`, `form`, `field`, and `list` are not enough, while a focused
primitive name with a visibly restated widget recipe is actionable. Output is
sorted by repository-relative path, line, column, and rule.

The `shared-tabs` rule flags a literal `k-tabs` class on a native Vue `nav`.
Review dynamically bound classes when checking section navigation. Use
`Tabs.vue` so hrefs, selection, counts, and accessibility stay shared.
Vanilla markup and the canonical component remain valid. Local `.spin` or
`.spinner` animation recipes use `k-spin`, including its reduced-motion policy.
The `css-block-balance` rule checks authored CSS and CSS SFC style blocks even
when the bundle imports them as raw strings. It ignores comments, quoted and
escaped braces, and URL payloads. This is a block-balance guard, not a complete
CSS parser or a preprocessor validator.

The feedback check in the same target rejects retired native Vue notification
classes, including `k-error` and `k-stale`, and hand-built error, warning or
success notifications with a literal `k-inline-notification` recipe. Use
PortalKit `InlineNotification` for those outcomes. Loading spinners, the
multi-state configuration save indicator, and field-specific validation keep
their existing contracts. The audited legacy backlog is empty.

The only suppression mechanism is the structured
[`hack/ui-conformance-exceptions.json`](../../../hack/ui-conformance-exceptions.json)
registry. Each exact exception references `design.quality.exceptions`; the
scanner resolves that ID and checks its source locator. See the
[exception policy](exceptions.md).

The durable acceptance rule is focused tests pass and the scanner reports zero
diagnostics. A previous run covered host marker preservation, App Studio accent
text, Code recipes, native table rows with nested controls, and Kuery's narrow
inventory scroll; those are evidence examples, not a debt baseline. Source
validation is not a substitute for browser testing.
