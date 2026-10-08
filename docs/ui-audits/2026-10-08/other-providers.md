# Infrastructure, Kuery, and Quickstart UI audit

Durable browser measurements and check results are in [browser evidence](browser-evidence.json). References below to `/tmp/railgrid-ui-audit/` are supplemental machine-local logs and captures.

Scope: every provider-owned production file under the three enabled portals' `src/`, plus public fallback documents and brand icons. Vendored PortalKit and AgentKit are distribution outputs, reviewed at their canonical owner by the root agent. Databricks is absent locally; `/home/tarkin/github/providers` contains external provider sources but the root's live catalog confirmed none enabled. This report therefore covers Infrastructure, Kuery, and Quickstart.

Authority: `AGENTS.md`, `docs/design/README.md`; resource read/creation, fluid shell, controls, accessible interaction, typography, iconography, theming, provider integration, ResourceSectionCard, and quality review contracts. Impeccable craft-floor was read; the pinned Violet Circuit authority prevails. Metadata boundaries were respected: creation adoption is partial, graph accessibility remains explicitly unverified, and source/test conformance is distinct from rendered evidence.

## Coverage and changes

All 40 provider-owned production source files were inventoried. There are 10 Infrastructure Vue surfaces, 5 Kuery Vue surfaces plus its vanilla tile, and Quickstart's vanilla page/tile. Listed unchanged items were inspected and retained where the applicable existing contract is sound.

### Infrastructure

| File | Audit result and implementation |
| --- | --- |
| `src/App.vue` | Workspace/context loading and missing-workspace status are named. Added real user identity to the authority boundary. Replaced a getter returning a fresh array with actual Vue multi-source watchers: theme/context pushes no longer remount pages unless base/transport/tenant/user changes. Shared confirmation remains mounted once. |
| `src/DashboardTile.vue` | Retains useful populated or authoritative empty state through background reads and transient failure, with retry, disabled terminating rows, and mounted-generation fencing. Added userId/sub/email fencing, ignored token rotation under host transport, changed watcher to true multi-source comparison, and made pending status use warning. Removed local lowered-opacity informational copy. |
| `src/components/TemplateCard.vue` | Native button, existing k-card, semantic Lucide exposure icons, description, kind, and version retained. Added concise `Provision <template>` accessible action name. Card title and header containment now wrap safely; footer identifiers now use mono. |
| `src/components/ViewValue.vue` | Existing links, badge, empty value, code/copy and noninteractive deletion variant retained. Failed clipboard writes now produce a shared recovery toast. Long code and link values have explicit text spans with min-width/overflow containment; copy button does not shrink. Live mobile sweep exposed standalone URL field links below44px; local coarse/hybrid-pointer link targets now have44px minima. |
| `src/components/DynamicForm.vue` | Reviewed scalar string/number/integer, boolean, enum, scalar-array, complex-array, nested object, and map row rendering/validation. Existing persistent label/control/help order, native fieldsets, unique recursive IDs, hint/error association, focus-to-invalid, map insertion/removal focus, read-only omission and schema constraints retained. Map controls and remove actions use native/shared recipes. No additional local primitive needed. |
| `src/views/CatalogPage.vue` | Existing labeled category/cloud filters, LayoutSelector, grid/table views, named interactive rows, initial delayed loading, retry, authoritative empty and no-match recovery retained. Fluid layout and readable collection widths retained. Responsive skeleton now has a single minmax track on narrow screens. |
| `src/views/ProvisionPage.vue` | Existing route-owned k-create skeleton, constrained identity/schema inputs, persistent drafts, inline field validation and form errors, Cancel → Provision footer, guarded asynchronous submission, recovery routes and success toast retained. Canonical shared title typography is owned by root. |
| `src/views/InstanceListPage.vue` | Existing ResourceTable controls, cursor/client mode transitions, foreground-vs-background reads, template-defined columns, tombstones, conditions, named deletion confirmation, mutation error and toasts retained. Added shared FirstRunGuide for an authoritative unfiltered empty workspace, with one Browse templates primary action and choose/configure/follow-provisioning journey. Background failure retains this useful empty guide with retry; filtered emptiness stays in the table. |
| `src/views/InstanceDetailPage.vue` | Existing shared ResourceBackLink, ResourcePage, stat cards, section cards, conditions, child-resource table, overflow action menu and refresh state retained. Live detail metrics exposed duplicate Conditions headings; the shared ConditionsPanel now receives showTitle=false inside its named section card. The first detail screenshot also exposed a narrow Message column while Type consumed remaining width; the root corrected canonical ConditionsPanel to make Message the primary column. Template-owned detail sections and legacy values remain lossless. Detail labels/values stack on narrow screens; local refresh animation respects reduced motion. |
| `src/views/MissingCredentialsPage.vue` | Existing recovery back action, explicit missing prerequisite, placeholder-only kubectl command, and explanation retained. No credential values fetched/rendered by this page. Existing scoped code/pre styling remains selectable and overflow-contained. |
| `src/style.css` | Added min-width containment and wrapping to page/header/actions, template titles, errors and empty recovery rows; stacked detail fields on mobile; constrained long values; retained canonical colors and approved existing dark fallback literals; added reduced-motion handling and display-font page-title role. |
| `src/element.ts` | Thin reactive light-DOM Vue mounts and clean unmount behavior retained; added userId type parity with actual host auth context. |
| `src/main.ts` | Existing namespaced, single style injection and duplicate-registration protection retained. Infrastructure remains host-compiled utility integration as documented. |
| `src/types.ts` | Retained presentation/creation API types; added userId context field to support authority invalidation. |
| `src/routes.ts` | Existing provider-owned readable collection, detail and provision routes retained. |
| `src/api.ts` | UI data boundary uses host-owned fetch and tenant-scoped kube client; no credential values are fetched from Secrets for UI presentation. Existing invalidation, pagination and result-envelope boundaries retained. |
| `src/createValues.ts` | Existing writable-input extraction retained; computed/server-owned values do not become editable/submit fields. |
| `src/view.ts` | Existing single template-expression resolver and spec/status/meta presentation namespace retained; optional detail and list views remain lossless. |
| `src/refresh.ts` | Existing serialized foreground-priority/background coalescing, adaptive cadence and persistent local deletion markers retained. |
| `src/instanceListRequest.ts` | Existing query/page/filter authority checks retained; stale page reads cannot masquerade as current filters. |
| `public/index.html` | Sanctioned standalone fallback document retained; added real mobile viewport metadata and wrapping for long inline custom-element identifiers/links. |
| `public/icon.svg` | Existing square provider brand mark retained. |

### Kuery

| File | Audit result and implementation |
| --- | --- |
| `src/App.vue` | Both initial and retained-snapshot Retry actions now expose disabled/aria-busy state and Retrying… copy during the foreground request. Shared Tabs, mounted tab preservation/lazy secondary views, workspace loading/error/retry and existing FirstRunGuide retained. User-scoped keys now change when same-workspace account changes through corrected request identity. |
| `src/components/InventoryView.vue` | Existing ResourceTable paging, exact filters, no-engaged-edge recovery, incomplete-pagination recovery, named native rows and retained snapshots inspected. Kind and Namespace now have persistent visible labels. Explicit query/filter/page/retry reads use foreground mode for immediate user feedback. |
| `src/components/TopologyView.vue` | Existing shared FormSelect controls, graph/list toggle, named graph region, scoped pan/zoom/fullscreen keyboard controls, list resource buttons, cancellation, recoverable expansion/layout failures and bounded result copy retained. Rich graph accessibility remains an acknowledged boundary, not declared globally verified. |
| `src/components/ImpactView.vue` | Existing ResourcePage/ResourceBackLink, relation metadata legend, graph/list alternatives, related-object buttons, retry and truncation bounds retained. Legend CSS now wraps long relation descriptions safely. |
| `src/components/PlaygroundView.vue` | Existing example FormSelect, labeled CodeMirror editor, JSON validation, fallback editor, named run action and live results retained. Fallback textarea now uses shared k-input, and results expose aria-busy during query execution. Closed API/access details remain secondary technical content. |
| `src/dashboard-tile.ts` | Existing theme-neutral tile recipes, mounted-generation identity checks and saved-view navigation retained. Added authoritative loaded state: transient failures preserve counts and saved-view rows; initial and stale failures now expose Retry. Benign unavailable-binding paths remain empty rather than alarming. |
| `src/style.css` | Added shell/panel min-width containment, persistent filter label styling, wrapping error/recovery banners and legend rows. Inventory table uses the full available canvas; playground split uses minmax(0, 1fr) tracks. Shared tokens and sanctioned graph palette exception retained. |
| `src/request-context.ts` | Found mounted scope and request identity omitted user even though scratch SavedView is user-owned. Added canonical host userId/sub/email identity to both; token refresh retains mounted scope. Existing email-based scratch-view naming stays stable. |
| `src/element.ts` | Existing thin reactive light-DOM mount/cleanup retained; host userId type added. |
| `src/main.ts` | Existing canonical styles handoff and namespaced local styles retained; classic-script custom-element/tile registrations remain unchanged. |
| `src/graph.ts` | Reviewed graph theme extraction, semantic relation palette/direction, non-color line styles, list derivation, keyboard mappings, resize/cleanup and bounded layout/expansion contracts. Existing authorized literal palette exception retained. Existing contrast source tests are evidence for those contracts only. |
| `src/playground.ts` | Existing vendored CodeMirror loading, schema vocabulary, bounded local hints, meaningful examples, screen-reader editor label and teardown retained. No schema value or user query is fetched outside its existing explicit request path. |
| `src/kuery.ts` | Existing context-derived API access, row naming, age formatting and domain recovery text retained. |
| `src/api.ts` | Existing query run envelope and inventory page/cursor mapping retained; UI uses published kube verb paths rather than ad hoc provider REST. |
| `src/savedviews.ts` | Existing user-specific scratch view and kube reads retained; user-facing naming compatibility preserved by the identity fix. |
| `src/inventory-pager.ts` | Existing opaque cursor history, exact facet normalization and stale page request guards retained. |
| `public/index.html` | Sanctioned standalone fallback retained; added mobile viewport metadata and long identifier/link wrapping. |
| `public/icon.svg` | Existing square provider brand mark retained. |

### Quickstart

| File | Audit result and implementation |
| --- | --- |
| `src/element.ts` | Removed independently managed Greeting creation from the resting collection. New host-routed `create/greeting` uses shared k-create header/surface/body/actions with one back action, persistent labels/hints, inline error, busy state and Cancel → Create greeting footer. Collection/authoritative first-use offers New greeting; first-use uses canonical vanilla k-first-run markup and ordered journey. Draft survives cancel/return and failed writes; success confirms via shared toast and returns to collection. Page wrapper is fluid; input focus/caret survives read rerender. Replaced implementation-centric copy with task copy, named per-object Greet actions, used ghost secondary actions, added full-message title, and real userId authority fencing. Existing concurrent per-row results, late-response fences, workspace isolation and host-owned kube transport retained. |
| `src/tile.ts` | Added explicit initial loading/failure/retry, authoritative snapshot retention across transient failures, mounted-generation tenant/user fencing and unmount invalidation. Uses shared poller/semantic tile classes; pending row marker uses warning. Benign unbound-provider paths remain useful empty state. |
| `src/style.css` | Removed capped/centered two-panel page grid; collection uses the host fluid column while canonical creation component bounds fields. Danger error token replaces undefined text-error token. Shared status is visually hidden without geometry movement. Identifiers/timestamps use mono and tabular numerals; heading uses display font. Namespaced card/row/field styling retained. |
| `src/main.ts` | Existing shared canonical styles handoff and namespaced injection retained; both elements are registered once. |
| `public/index.html` | Sanctioned standalone fallback retained; added mobile viewport metadata and long identifier/link wrapping. |
| `public/icon.svg` | Existing square provider brand mark retained. |

## Validation

- `make build-infrastructure-provider-portal`: final full portal Vitest suite (123 tests across 23 files), vue-tsc and Vite passed, including the FirstRunGuide addition and final link-target/Conditions heading composition changes.
- `make build-kuery-provider-portal`: final Vite and CodeMirror vendoring passed. `make test-kuery-provider-portal` passed all 52 tests plus vue-tsc, including the corrected user-aware request identity and busy Retry contracts.
- `make build-quickstart-provider-portal`: final Vite build passed. Quickstart npm suite passed all 17 tests and TypeScript passed, including meaningful new route separation, failed/cancelled draft preservation, success navigation, real host userId invalidation, stale tile snapshot recovery and late-response isolation tests.
- Infrastructure mounted tile regression covers token/theme context pushes retaining the authoritative empty snapshot and account change fencing old reads.
- `git diff --check` on owned portals passed.
- Repository design-document, shared-copy and UI conformance gates passed; authenticated browser coverage is recorded below. Source review alone does not establish a comprehensive screen-reader, contrast, or all-state browser audit.

## Final live browser confirmation

The complete first root sweep attempted 36 URLs in both themes at 1440px/390px. Its owned-provider metrics found no horizontal page overflow, unnamed controls or page errors; a development reload interrupted one Infrastructure route, which the final pass completed.

The final targeted pass has 49 records and 45 screenshots in `/tmp/railgrid-ui-audit/live-other-confirmation/results.json` and its screenshot directory:

- Infrastructure: all five routes (catalog, Browser provision, populated Instance detail, Instances collection, missing credentials) in both themes at 1440px/390px. All 20 route captures have zero uncontained horizontal overflow, zero unnamed controls and zero page errors. Long readable tables retain their named horizontal scroll affordance. The detail route has exactly one Conditions heading.
- Infrastructure touch targets: standalone URL links in detail and list now meet 44px minima on coarse input. The only remaining small-control heuristic flags are 14×14px native checkbox glyphs. Their associated `.k-checkbox-hit` labels are 236.41×44px, so the clickable target meets the contract.
- Infrastructure Conditions width: the fresh final served bundle is Ready and its `serving.ui.mainJSIntegrity` exactly matches local `dist/main.js` SHA384. One extra desktop detail confirmation shows Type 145px and Message 699.59px, with normal readable single-line controller descriptions. The raw earlier screenshot that exposed the narrow Message column is retained. Final proof: `infrastructure-instance-detail-column-confirmed-light-1440.png`.
- Kuery: real Topology, Inventory and Playground navigation buttons were selected in every theme/width combination (12 screenshots). This workspace has no connected Kubernetes edge, so each retains the named setup guide. The 8 secondary data-view cases are therefore explicitly unavailable; the 4 Impact cases have no inspectable inventory row. This confirms the prerequisite UI and navigation, not populated graph/editor/Impact rendering.
- Quickstart: collection, New greeting, and a retained local draft were captured in all four theme/width combinations (12 screenshots). Cancel returns to a resting collection with no creation form; returning through New greeting preserves both name and message in all four actual host-navigation cases. No create or Greet action was performed.

All 45 screenshots have zero uncontained page overflow, zero unnamed controls and zero page errors. The final script exits0. Representative captured surfaces were visually reviewed in both themes and widths, including the wrapping provision fields, populated tables, long values, single Conditions section, named scroll affordances, missing-credentials recovery, setup journey and retained draft. The full screenshot set remains available for review. One failed preliminary attempt happened during a Tilt provider reload; a later test-selector error was corrected to the canonical nav buttons and completed frames were retained while remaining cases resumed.

## Model form fixture and confirmation

The additional authorized model fixture work repaired a malformed provider selector and replaced retired REST mocks with bound Studio/ModelCredential Kubernetes resources and current declared paths. The runner preserves all full unmasked screenshots/diff images and exact pixel metrics as observational evidence while enforcing independent design-book contracts; provider-owned capabilities and credential/test guidance remain faithful.

Final `make test-model-form-visual` passes: 16 real-source captures (both providers × OpenAI/custom × light/dark × 1440px/390px), 360 individual contracts and 56 shared-geometry checks all pass. Real fonts load; there are no console/page errors or horizontal overflow. Create roots use 14px/21px typography; headings use Archivo 125%; focused headings use the token 2px ring; back arrows are 14px; inputs are 40px desktop/44px mobile. New drafts and disabled verification actions, labeled fields/help, section hierarchy, paired control alignment, and local provider/endpoint credential clearing are verified. An actual App Studio endpoint-change key-retention defect was found and corrected by its owner, then confirmed by the final pass. No saved-resource writes occur.

Eight complete unmasked PNG comparisons differ 13.73–25.63% because legitimate adapter-specific content changes lower section/footer height. Their exact shared header/control geometry passes; no pixel tolerance, masking, copy normalization or fixture style override was introduced. Evidence: `/tmp/railgrid-ui-audit/model-forms/models-form-visual-regression.json` and `/tmp/railgrid-ui-audit/model-forms.md`.

## Remaining verification boundaries

Every provider-owned production source file is covered above. Browser evidence is bounded to available live data and the model fixture; it does not establish comprehensive screen-reader compliance, all schema combinations, all data/error states, or live credential validation. Kuery's populated topology/inventory/editor/Impact and previously documented graph assistive-technology/color-blind boundary remain explicitly unverified in the live workspace. Existing tests and source review cover their applicable contracts. Legacy Infrastructure template-owned value presentation remains lossless; credentials are referenced as setup prerequisites and no UI Secret-value-reading path was introduced.
