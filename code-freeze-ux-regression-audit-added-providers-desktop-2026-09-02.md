# Code Freeze UX / Regression Audit — Newly Enabled Providers, Desktop Priority

- **Audit date:** 2026-09-02
- **Target:** `https://console.dev.kyrosos.com/ui/`
- **Authentication:** `dev-token`
- **Scope:** Desktop-first reassessment of Edges, Agents, Databricks, and Kuery
- **Primary viewport:** 1440×900
- **Secondary viewport:** 1280×720 attempted but blocked by target availability
- **Mode:** Audit only; no source, configuration, infrastructure, or provider data was changed

## How this report supersedes the earlier addendum

This is the release-prioritization report for a desktop-oriented product. It supersedes [the earlier mobile-weighted provider addendum](/home/crwilhit/github/faros/code-freeze-ux-regression-audit-added-providers-2026-09-02.md) when deciding what matters before release.

The earlier report remains valid evidence for mobile and touch behavior, but those findings no longer affect the desktop release score. Five of its nine findings were exclusively mobile/touch concerns and are excluded here:

- Edges Service Type collapsing at 390px.
- Rightmost provider tabs clipping at 390px.
- Agents 28px touch actions.
- Databricks Tables header compression at 390px.
- Edges Marketplace Deploy touch-target sizing.

The previously reported mobile dashboard collapse is also excluded from desktop release prioritization.

## Evidence freshness and environment boundary

The desktop conclusions use the authenticated 1440×900 evidence captured in the immediately preceding provider pass on the same target and date, plus focused source inspection where behavior is viewport-independent.

A fresh 1440×900 and 1280×720 rerun was attempted after the user changed the priority. At approximately 16:01–16:06 UTC, Caddy returned `HTTP/2 502` with an empty body for `/ui/login`, `/ui/`, `/healthz`, and provider routes. Three independent browser workers stopped rather than loop. This is recorded as an external environment blocker, not a provider defect.

**Blocker evidence:** [Edges availability probe](/tmp/faros-freeze-audit-desktop/edges/00-live-blocker.json), [Data-provider availability probe](/tmp/faros-freeze-audit-desktop/data/target-availability-502.json), [Agents availability probe](/tmp/faros-freeze-audit-desktop/agents/desktop-pass-blocked.json)

## Executive summary

**Desktop release confidence:** Medium

The desktop-first assessment found no P0 or P1 issue in the newly enabled providers. Four P2 findings remain relevant at desktop widths: Kuery loses navigation context on refresh, Kuery's editor ignores the dark theme, Databricks presents an impossible primary action, and Agents replaces native table-row semantics with links. One Agents empty-state issue is retained as P3 polish.

Edges had no reproduced desktop defect in the completed 1440×900 pass. Its onboarding, Workloads, Services, create/cancel, reload, deep-link, history, keyboard, empty, and unavailable-detail flows behaved coherently without console or network errors.

Confidence is Medium rather than High because the live tenant had no populated Edges or Databricks resources, no engaged Kuery edge, and no live Agents activity. Successful and destructive workflows were intentionally excluded, and the target outage prevented the planned 1280×720 confirmation.

### Desktop findings by priority

| Priority | Count |
|---|---:|
| P0 | 0 |
| P1 | 0 |
| P2 | 4 |
| P3 | 1 |
| **Total** | **5** |

### Most concerning desktop areas

1. Kuery browser-location and reload behavior.
2. Databricks creation prerequisites and primary-action honesty.
3. Agents run-history accessibility semantics.
4. Kuery dark-theme completeness.

## Desktop technical audit score

| Dimension | Score | Key finding |
|---|---:|---|
| Accessibility | 3/4 | Agents run rows replace their native table-row role |
| Performance | 3/4 | Completed healthy journeys were quiet; load/stress behavior was not measured |
| Desktop layout | 3/4 | 1440×900 was coherent; the planned 1280×720 pass was externally blocked |
| Theming | 3/4 | Kuery's CodeMirror editor remains white in the dark product shell |
| Implementation integrity | 4/4 | Shared tokens and PortalKit patterns remain coherent; detector hits were contextual false positives |
| **Total** | **16/20** | **Good — address weak dimensions** |

### Implementation integrity verdict

**Pass.** The providers use consistent headers, status treatment, route-owned creation surfaces, tokens, and resource-table conventions. The Impeccable detector's shared progress-width transition represents semantic progress. Its Agents left-border matches distinguish timeline status, quotations, and tool-call state rather than arbitrary decoration, so they were not promoted to findings.

## Release blockers

None found in the desktop-priority provider scope.

## Full desktop findings

### [P2] Kuery loses the active view on reload and has no view-specific browser location

**Category:** Bug

**Area:** Kuery → Topology, Inventory, and Playground

**User flow:** Refresh, revisit, or share the current Kuery workspace.

**Steps to reproduce:**

1. Open `/ui/providers/kuery`.
2. Select `Inventory` or `Playground`.
3. Reload the page.

**Expected behavior:** Reload preserves the selected view, and browser history or a stable URL identifies it.

**Actual behavior:** All three views keep the same `/ui/providers/kuery` URL. The selected view exists only as in-memory component state initialized to `topology`, so reload returns to Topology and discards the user's current context.

**Impact:** Desktop users lose their place on refresh and cannot bookmark, share, or reliably traverse browser history between Kuery's major work surfaces.

**Evidence:** [Authenticated Inventory state](/tmp/faros-freeze-audit-added/data/kuery-03-inventory.png), [state implementation](/home/crwilhit/github/faros/providers/kuery/portal/src/element.ts), [reload reproduction before state](/tmp/faros-freeze-audit-added/data/confirm-kuery-inventory-before-reload.json), [reload reproduction after state](/tmp/faros-freeze-audit-added/data/confirm-kuery-after-reload.json)

**Evidence boundary:** The view and URL were exercised at 1440×900; the explicit reload reproduction was captured at 390×844. Source inspection confirms the same viewport-independent in-memory state and default on desktop.

**Reproducibility:** Always in the completed reproduction

**Suggested priority:** P2

### [P2] Databricks Browse remains enabled when its required connection is absent

**Category:** UX

**Area:** Databricks → Warehouses → Browse registration

**User flow:** Browse Databricks warehouse metadata before configuring a connection.

**Steps to reproduce:**

1. Open `/ui/providers/databricks/create/warehouse/browse` in the empty workspace at 1440×900.
2. Observe `A connection is required` and the empty Connection field.
3. Select the enabled primary `Browse` action.

**Expected behavior:** Browse is unavailable until the prerequisite is satisfied, or the primary action advances the prerequisite recovery flow.

**Actual behavior:** Browse remains enabled with primary styling. Activating it leaves the wizard in place and adds a second error, `Select a connection.`

**Impact:** The desktop interface invites an action it already knows cannot succeed, adding avoidable error friction to an otherwise explicit prerequisite state.

**Evidence:** [Before Browse](/tmp/faros-freeze-audit-added/data/databricks-browse-no-connection-before.png), [after Browse](/tmp/faros-freeze-audit-added/data/databricks-browse-no-connection-after.png)

**Reproducibility:** Always in the empty workspace

**Suggested priority:** P2

### [P2] Agents Activity rows replace native table-row semantics with links

**Category:** Accessibility

**Area:** Agents → Activity and Agent detail → Runs

**User flow:** Navigate and understand run history with desktop assistive technology.

**Steps to reproduce:**

1. Render a populated Activity or Runs table at 1440×900.
2. Inspect the accessibility semantics of an interactive run row.

**Expected behavior:** The table retains row and cell relationships while providing an accessible activation mechanism.

**Actual behavior:** Each native `<tr>` is assigned `role="link"` and `tabindex="0"`. The link role replaces its native row role.

**Impact:** Screen-reader table navigation may stop exposing the run as a row associated with its Trigger, Input, Phase, Duration, Usage, and When columns.

**Evidence:** [Populated desktop Runs table](/tmp/faros-freeze-audit-added/agents/pop-02-agent-detail-runs.png), [row accessibility snapshot](/tmp/faros-freeze-audit-added/agents/pop-a11y-run-rows.json)

**Evidence boundary:** Populated rows came from deterministic GET interceptions rendered through the live provider bundle; no tenant data was created.

**Reproducibility:** Always across all four deterministic populated rows

**Suggested priority:** P2

### [P2] Kuery Playground editor ignores the dark theme

**Category:** Visual

**Area:** Kuery → Playground

**User flow:** Edit a QuerySpec in the default desktop dark theme.

**Steps to reproduce:**

1. Open `/ui/providers/kuery` at 1440×900 in dark theme.
2. Select `Playground`.
3. Inspect the CodeMirror editor.

**Expected behavior:** The editor participates in Violet Circuit dark theming while retaining readable syntax contrast.

**Actual behavior:** CodeMirror uses its white background, black base text, and red syntax styling inside the dark provider surface.

**Impact:** The editor is the dominant content area on desktop and appears to belong to a different application, making the provider's theme support visibly incomplete.

**Evidence:** [Desktop Playground](/tmp/faros-freeze-audit-added/data/kuery-07-playground.png), [computed editor colors](/tmp/faros-freeze-audit-added/data/kuery-mobile-playground-editor-theme.json)

**Evidence boundary:** The screenshot is desktop. Computed `rgb(255,255,255)` background and `rgb(0,0,0)` text were recorded separately at mobile width; CodeMirror loads the same unthemed stylesheet and configuration at both widths.

**Reproducibility:** Always in the completed dark-theme pass

**Suggested priority:** P2

### [P3] Agents empty collections provide terse status but no contextual next action

**Category:** UX

**Area:** Agents, Activity, Connections, and Models empty states

**User flow:** Understand what to do in a newly enabled Agents workspace.

**Steps to reproduce:**

1. Open each Agents provider section in the empty live tenant at 1440×900.
2. Inspect the empty collection card and nearby actions.

**Expected behavior:** Empty states explain the prerequisite or offer the most relevant next action in context.

**Actual behavior:** The cards are terse icon-plus-text states. Agents, Connections, and Models rely on a separate header action; Activity provides no direct next step from its empty state.

**Impact:** First-time users must infer that they should leave Activity and create/configure an Agent before activity can exist. Existing header actions keep this recoverable.

**Evidence:** [Agents empty state](/tmp/faros-freeze-audit-added/agents/desktop-02-agents-entry.png), [Activity empty state](/tmp/faros-freeze-audit-added/agents/desktop-04-activity.png), [Connections empty state](/tmp/faros-freeze-audit-added/agents/desktop-05-connections.png), [Models empty state](/tmp/faros-freeze-audit-added/agents/desktop-06-models.png)

**Reproducibility:** Always in the empty workspace

**Suggested priority:** P3

## Desktop UX quality observations

- Edges, Agents, Databricks, and Kuery reported `READY` throughout the completed authenticated desktop pass.
- Healthy desktop journeys produced no correlated page errors, failed requests, HTTP errors, or console warnings.
- Edges' desktop experience was clean in the available empty workspace: the first-edge wizard, Workloads, Services, safe creation entry, cancellation, reload, history, and unavailable details all remained understandable.
- Agents' route-owned forms, invalid deep links, cancellation, and keyboard activation behaved predictably. Wide populated tables stayed within local scroll regions.
- Databricks clearly distinguishes Connections, Warehouses, and Tables. Manual forms and invalid-name validation prevented unintended registration work.
- Kuery's Topology and Inventory empty states truthfully explain that no Kubernetes edge is engaged.
- Across the four providers, the desktop visual system is cohesive. The Kuery editor is the single prominent theme break.

## Areas tested with authenticated desktop evidence

- [x] Provider catalog availability and `READY` state
- [x] Edges first-edge wizard, Workloads, Services, manual/marketplace creation entry, validation, cancel/back, missing details, reload, history, and keyboard interaction
- [x] Agents empty collections, creation entry, invalid input, cancel/back, deep links, reload, and keyboard traversal
- [x] Agents deterministic populated details, Runs, Activity filters, Connections, Models, pagination, and accessibility semantics
- [x] Databricks Connections, Warehouses, Tables, manual creation, Browse prerequisites, invalid input, and cancellation
- [x] Kuery Topology, Inventory, Playground, no-match query, API disclosure, and dark-theme rendering
- [x] Desktop visual inspection at 1440×900
- [x] Implementation-integrity detector with contextual review

## Areas not tested or not freshly repeated

- **1280×720:** planned and attempted, but the target returned HTTP 502 before authentication.
- **Fresh second 1440×900 capture:** blocked by the same target outage. The report uses the completed immediately preceding authenticated pass.
- **Edges populated flows:** no Edge, Service, or Workload existed, so populated details, credentials, enrollment completion, and workload reconciliation remain unverified.
- **Agents live mutations:** no Agent was created, run, approved, denied, deleted, or sent a chat message. Populated checks used deterministic GET fixtures.
- **Databricks populated flows:** no Connection, Warehouse, or Table existed, so successful discovery/import, detail editing, and deletion remain unverified.
- **Kuery populated flows:** no Kubernetes edge was engaged, so graph nodes, relationship expansion, impact traversal, and populated rows remain unverified.
- **Quickstart:** it is still absent from the authenticated provider catalog.
- **External consequences:** OAuth redirects, cluster connection, deployment, credential mutation, and destructive actions were intentionally excluded.
- **Full assistive-technology and performance sessions:** DOM/keyboard semantics were inspected, but no screen-reader, load, or stress test was run.

## Desktop priorities before release

1. Preserve Kuery's selected major view in browser location and reload behavior.
2. Make Databricks creation actions agree with already-known prerequisites.
3. Preserve native table semantics in Agents run history.
4. Complete Kuery's dark-theme treatment for its primary editor.

## Deprioritized mobile appendix

The five mobile/touch findings from the earlier addendum remain documented, but they are not counted or ranked here. No claim is made that they were fixed; this report only reflects the user's stated release priorities.

## Heavy Route worker usage

| Worker role | Calls |
|---|---:|
| explorer | 3 |
| executor_luna | 0 |
| executor_sol | 0 |
| tester | 0 |
| doc-writer | 0 |

Three existing explorer workers were reused for the desktop-priority reassessment. All three independently encountered the target's HTTP 502 state and returned bounded evidence rather than treating it as a provider defect.

Desktop-priority evidence is under `/tmp/faros-freeze-audit-desktop/`; the completed authenticated 1440×900 evidence remains under `/tmp/faros-freeze-audit-added/`.
