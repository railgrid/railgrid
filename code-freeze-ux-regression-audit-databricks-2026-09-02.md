# Code Freeze UX / Regression Audit — provider-databricks

- **Audit date:** 2026-09-02
- **Target:** `https://console.dev.kyrosos.com/ui/`
- **Authentication:** `dev-token`
- **Scope:** Databricks provider only, desktop-first
- **Viewports:** 1280×720, 1440×900, and 3840×2160
- **Mode:** Audit only; no source, configuration, infrastructure, or provider data was changed

## Executive summary

**Overall release confidence:** Medium

No P0 or P1 release blocker was found in the exercised Databricks surfaces. Two P2 issues were reproduced. The more direct workflow defect is that both Browse-registration routes invite users to continue even though the UI already knows that the required connection is absent. Error states are recoverable, but they expose transport and API exception names such as `HTTPError` and `GraphQLError` instead of concise, user-oriented explanations.

The provider remained visually stable at all three desktop resolutions. There was no page-level horizontal overflow, content clipping, or resolution-specific functional failure. At 3840×2160, the provider stays centered in the shared 1024px product content column. This leaves large margins but matches the product-wide `max-w-5xl` layout contract, so it is recorded as an observation rather than a Databricks defect.

Confidence remains Medium because the live workspace contained no Databricks connections, warehouses, or tables. Successful connection creation, live Databricks discovery, populated tables/details, review/results, filtering with data, and destructive actions could not be validated without changing tenant state or having real Databricks credentials.

### Findings by priority

| Priority | Count |
|---|---:|
| P0 | 0 |
| P1 | 0 |
| P2 | 2 |
| P3 | 0 |
| **Total** | **2** |

### Most concerning areas

1. Browse-registration prerequisite and primary-action honesty.
2. Error copy across collection, detail, and Browse initialization states.

## Resolution assessment

| Viewport | Result | Notes |
|---|---|---|
| 1280×720 | Pass with shared P2 findings | Collections, menus, manual forms, Browse wizards, validation, keyboard flow, and history remained usable. No clipping or page-level horizontal overflow. The two manual registration forms fit within the viewport; the table form ended at approximately y=693. |
| 1440×900 | Pass with shared P2 findings | Stable hierarchy and spacing; loading skeletons did not shift the page. Dark and light themes remained coherent. Controlled read failures exposed raw diagnostics but Retry recovered. |
| 3840×2160 | Pass with shared P2 findings | True 3840×2160 viewport. The provider remained 1024px wide at x=1436 and centered, with no stretching, clipping, or overflow. The fixed width uses about 27% of the viewport and leaves about 73% as horizontal margins; this matches the shared product layout contract. |

**Cross-resolution conclusion:** Neither finding is resolution-specific. Both reproduce at 1280×720, 1440×900, and 3840×2160.

## Desktop technical audit score

| Dimension | Score | Evidence |
|---|---:|---|
| Accessibility | 4/4 | Semantic tables, labelled scroll regions, explicit form labels, live status/error regions, visible keyboard focus, and no reproduced focus trap. |
| Performance and loading | 3/4 | Delayed reads produced stable skeletons and settled cleanly; load/stress performance was not measured. |
| Desktop layout | 4/4 | No clipping or page-level overflow at any requested resolution; the 4K max-width behavior is consistent with the shared layout. |
| Theming and visual consistency | 4/4 | Dark and light states remained coherent without visible layout shifts. |
| Task and error honesty | 3/4 | Prerequisite-invalid Browse actions remain enabled, and error surfaces expose raw diagnostic vocabulary. |
| **Total** | **18/20** | **Good, with two important recoverable UX issues.** |

## Release blockers

None found in the exercised Databricks scope.

## Full findings

### [P2] Browse remains enabled when the required Databricks connection is absent

**Category:** UX

**Area:** Databricks → Register warehouses/tables → Browse

**User flow:** Browse Databricks metadata in a newly enabled workspace before configuring a connection.

**Steps to reproduce:**

1. Open `/ui/providers/databricks/create/warehouse/browse` or `/ui/providers/databricks/create/table/browse`.
2. Wait for prerequisite reads to finish.
3. Observe the empty Connection field and `A connection is required` warning.
4. Activate the still-enabled primary `Browse` button.

**Expected behavior:** The primary action is disabled until the prerequisite is satisfied, or it advances the explicit prerequisite-recovery flow.

**Actual behavior:** `Browse` retains enabled primary styling. Activating it leaves the user on Source and adds a second error, `Select a connection.` No discovery request is started.

**Impact:** The interface invites an action it already knows cannot succeed. The issue affects both Browse entry points and adds avoidable error friction to the first-run path.

**Evidence:**

- 1280×720 warehouse: [before](/tmp/faros-freeze-audit-databricks/functional/1280x720-dbx-22-warehouse-browse-before.png), [after](/tmp/faros-freeze-audit-databricks/functional/1280x720-dbx-23-warehouse-browse-after.png)
- 1440×900 warehouse: [before](/tmp/faros-freeze-audit-databricks/functional/1440x900-dbx-22-warehouse-browse-before.png), [after](/tmp/faros-freeze-audit-databricks/functional/1440x900-dbx-23-warehouse-browse-after.png)
- 3840×2160 warehouse: [before](/tmp/faros-freeze-audit-databricks/functional/3840x2160-dbx-22-warehouse-browse-before.png), [after](/tmp/faros-freeze-audit-databricks/functional/3840x2160-dbx-23-warehouse-browse-after.png)
- 1280×720 table: [before](/tmp/faros-freeze-audit-databricks/functional/1280x720-dbx-26-table-browse-before.png), [after](/tmp/faros-freeze-audit-databricks/functional/1280x720-dbx-27-table-browse-after.png)
- 1440×900 table: [before](/tmp/faros-freeze-audit-databricks/functional/1440x900-dbx-26-table-browse-before.png), [after](/tmp/faros-freeze-audit-databricks/functional/1440x900-dbx-27-table-browse-after.png)
- 3840×2160 table: [before](/tmp/faros-freeze-audit-databricks/functional/3840x2160-dbx-26-table-browse-before.png), [after](/tmp/faros-freeze-audit-databricks/functional/3840x2160-dbx-27-table-browse-after.png)
- Source confirmation: [ResourceImportWizard.vue](/home/crwilhit/github/faros/providers/databricks/portal/src/ResourceImportWizard.vue:497) disables Browse only during initialization/submission, while [the click handler](/home/crwilhit/github/faros/providers/databricks/portal/src/ResourceImportWizard.vue:305) validates the missing connection afterward.

**Reproducibility:** Always, for both warehouse and table Browse routes at all three resolutions

**Suggested priority:** P2

### [P2] Error surfaces expose raw transport and GraphQL diagnostics

**Category:** Error Handling

**Area:** Databricks collections, missing-resource details, and Browse initialization

**User flow:** Understand and recover when a Databricks resource cannot be loaded.

**Steps to reproduce:**

1. Open a missing detail such as `/ui/providers/databricks/connections/no-such-connection`, or force a Databricks GraphQL read to return an error.
2. Inspect the visible error message.
3. Use Retry after restoring the read path.

**Expected behavior:** The message explains the user-relevant state—such as “Connection not found” or “Databricks resources could not be loaded”—and gives a clear recovery action without exposing protocol internals.

**Actual behavior:** Missing live details show strings such as `GraphQLError: connections.databricks.faros.sh "no-such-connection" not found`. Controlled 503 reads show `HTTPError: {"error":"Databricks audit injected read failure"}` in collection alerts and repeat that raw payload for each Browse prerequisite.

**Impact:** Users see implementation vocabulary and raw response bodies rather than a concise explanation of what happened. Retry works, and detail pages retain a Back link, so the failure is recoverable; the issue is still systemic across the provider's read surfaces.

**Evidence:**

- Live missing detail: [connection](/tmp/faros-freeze-audit-databricks/state/missing-connection-detail.png), [warehouse](/tmp/faros-freeze-audit-databricks/state/missing-warehouse-detail.png), [table](/tmp/faros-freeze-audit-databricks/state/missing-table-detail.png)
- Controlled collection failure: [Connections](/tmp/faros-freeze-audit-databricks/state/error-connections-list.png)
- Controlled Browse initialization failure: [warehouse wizard](/tmp/faros-freeze-audit-databricks/state/error-warehouse-browse-init.png), [table wizard](/tmp/faros-freeze-audit-databricks/state/error-table-browse-init.png)
- Recovery: [collection after Retry](/tmp/faros-freeze-audit-databricks/state/recovery-connections-after.png), [Browse after both Retry actions](/tmp/faros-freeze-audit-databricks/state/recovery-browse-after.png)
- Source confirmation: [api.ts](/home/crwilhit/github/faros/providers/databricks/portal/src/api.ts:359) maps failed HTTP reads to `HTTPError`, [the GraphQL branch](/home/crwilhit/github/faros/providers/databricks/portal/src/api.ts:372) maps response errors to `GraphQLError`, and [ConnectionDetailView.vue](/home/crwilhit/github/faros/providers/databricks/portal/src/views/ConnectionDetailView.vue:112) renders the reason with the message.

**Evidence boundary:** Missing-resource errors came from the healthy live API. The 503 examples were deterministic browser interception of Databricks GraphQL reads, used only to exercise error UX; they do not claim that the live service was failing. Recovery was verified by removing the interception and selecting Retry.

**Reproducibility:** Always in the live missing-detail checks and controlled read-failure checks

**Suggested priority:** P2

## UX quality observations

- Collections clearly distinguish Connections, Warehouses, and Tables and expose consistent empty states, search/filter controls, and create entry points.
- Manual registration routes make prerequisites explicit and keep `Register` disabled when required connection or warehouse data is absent.
- Invalid connection input (`BAD NAME!!!`) remained client-side, showed validation feedback, and emitted no non-auth mutation request.
- Cancel and repeated tab navigation remained coherent at all three resolutions. Browser Back/Forward settled correctly at 1280×720 and 1440×900; direct links and reload also remained coherent.
- Loading reads produced stable table skeletons. No misleading empty state appeared while loading.
- Collection and Browse Retry controls recovered when the read path was restored.
- Tables retain semantic table structure and labelled scroll regions. Keyboard focus was visible, and no focus trap was reproduced.
- Dark and light themes remained visually coherent.
- At 4K, the 1024px centered column creates pronounced margins. This is consistent with the shared AppLayout standard and caused no functional loss, so it is not counted as a provider-specific defect. A populated, high-column-count table would be needed to judge whether Databricks merits a sanctioned full-bleed exception.

## Areas tested

- [x] Authentication with `dev-token`
- [x] Provider readiness and direct entry
- [x] Connections, Warehouses, and Tables collections
- [x] Empty and prerequisite states
- [x] Create menus and manual registration routes
- [x] Warehouse and table Browse routes
- [x] Safe invalid connection input
- [x] Cancel at all resolutions; browser Back/Forward at 1280×720 and 1440×900; reload and direct links
- [x] Repeated section navigation
- [x] Missing connection, warehouse, and table details
- [x] Delayed loading state
- [x] Controlled collection and Browse initialization failures
- [x] Retry recovery
- [x] Keyboard traversal and visible focus
- [x] Dark and light themes
- [x] 1280×720, 1440×900, and true 3840×2160 geometry/overflow checks
- [x] Console, failed-request, and HTTP-error correlation during healthy journeys

## Areas not tested

- Successful connection creation because the audit could not mutate tenant state.
- Live Databricks credential validation or credential-specific backend failures.
- Populated collection rows, filters with results, pagination, row activation, and populated detail actions because the tenant had zero Databricks resources.
- Live catalog/schema/table discovery because no configured connection or credentials were available.
- Browse tree expansion, multi-selection, Review, registration Results, partial failure, conflict, and retry-failed behavior.
- Successful manual warehouse or table registration.
- Edit, credential rotation, refresh of a real table, and deletion confirmations/results.
- Load, stress, and large-result performance.
- A settled browser Forward result at 3840×2160; the final capture remained in the host's `Refreshing provider access` / `Loading workspace…` transition, so it was not counted as a pass or a provider defect.

## Top 5 things to address before releasing

Only two reproduced issues qualify as defects; the remaining items are validation gaps that materially limit release confidence.

1. Prevent warehouse and table Browse from presenting an enabled impossible primary action.
2. Replace raw `HTTPError`/`GraphQLError` diagnostics and response bodies with user-oriented error copy while retaining Retry.
3. Validate successful connection creation and live credential rejection/recovery in a disposable test workspace.
4. Validate populated collection filtering, pagination, row/detail navigation, edit, and deletion flows at all three desktop resolutions.
5. Exercise real Browse discovery through Review and Results, including conflicts, partial failures, and retry-failed behavior.

## Evidence index

- Functional journeys: `/tmp/faros-freeze-audit-databricks/functional/`
- Visual/accessibility pass: `/tmp/faros-freeze-audit-databricks/visual/`
- Loading, error, recovery, and independent resolution sweep: `/tmp/faros-freeze-audit-databricks/state/`
