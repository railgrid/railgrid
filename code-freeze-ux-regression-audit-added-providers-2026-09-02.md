# Code Freeze UX / Regression Audit — Newly Enabled Providers

- **Audit date:** 2026-09-02
- **Target:** `https://console.dev.kyrosos.com/ui/`
- **Authentication:** `dev-token`
- **Scope:** Providers enabled after the baseline audit: Edges, Agents, Databricks, and Kuery
- **Mode:** Audit only; no source, configuration, infrastructure, or provider data was changed

## Executive summary

**Overall release confidence:** Low

The four newly enabled providers are reachable and report `READY`. Healthy, read-only navigation produced no correlated HTTP failures, request failures, page errors, or console warnings. Their empty states are generally truthful, and cancel, back, reload, and deep-link behavior is substantially better than a typical pre-release surface.

Release confidence remains Low because a required Edges field becomes effectively unusable on a 390px viewport. The pass also found eight P2 defects across mobile navigation, touch actions, semantic tables, Databricks prerequisite handling, and Kuery theme and reload behavior.

### New findings by priority

| Priority | Count |
|---|---:|
| P0 | 0 |
| P1 | 1 |
| P2 | 8 |
| P3 | 0 |
| **Total** | **9** |

These counts contain only findings from the newly enabled provider pass. The previously reported P1 dashboard responsive collapse remains reproducible with the expanded seven-provider dashboard and is not counted a second time.

### Most concerning areas

1. Edges Service creation on mobile.
2. Provider section discovery and keyboard focus on narrow screens.
3. Kuery theme consistency and refresh persistence.
4. Agents touch actions and table semantics.
5. Databricks creation prerequisites and mobile collection hierarchy.

### Flows that could not be validated

- Populated Edges resources, successful agent enrollment, credential attachment, and workload deployment.
- Successful Agents creation, execution, approvals, deletion, streaming chat, or external connection redirects.
- Populated Databricks details, editing/deletion, and successful discovery/import results.
- Kuery graph traversal and populated inventory with an engaged Kubernetes edge.
- Quickstart, which remains absent from the authenticated catalog.

## Technical audit score

| Dimension | Score | Key finding |
|---|---:|---|
| Accessibility | 2/4 | 28px Agents actions, altered table-row semantics, and focused mobile tabs that remain clipped |
| Performance | 3/4 | Healthy journeys were quiet; load and stress behavior were not measured |
| Responsive design | 1/4 | Edges loses a required field value; several mobile layouts clip or severely squeeze content |
| Theming | 3/4 | Kuery's CodeMirror editor remains white in the dark product shell |
| Implementation integrity | 4/4 | Shared tokens and PortalKit conventions are broadly coherent; detector results were contextual false positives |
| **Total** | **13/20** | **Acceptable — significant work needed** |

### Implementation integrity verdict

**Pass.** The four providers largely present one coherent Faros product system: consistent provider headers, status treatment, resource tables, creation surfaces, tokens, and contained overflow.

The Impeccable detector reported the shared progress bar's `transition: width` in each vendored PortalKit copy and three left-border patterns in Agents. Contextual inspection did not promote these to findings: the width change communicates semantic progress, while the Agents borders distinguish timeline status, quotations, and tool-call state rather than serving as arbitrary decoration.

## Release blockers

### [P1] Edges Service Type becomes unreadable and impractical to select on mobile

**Category:** Possible Regression

**Area:** Edges → Services → Create service

**User flow:** Configure a service connector on an edge from a mobile viewport.

**Steps to reproduce:**

1. Authenticate with `dev-token`.
2. Open `/ui/providers/edges/create/service` at 390×844.
3. Inspect the `Type` field beside `Scheme` and `Port`.

**Expected behavior:** The required Type selector displays its selected service type and remains practical to operate.

**Actual behavior:** The Type selector collapses to 33×36px. Only its caret is visible; the selected type and available-value context disappear.

**Impact:** A primary Service-creation choice is effectively unusable on a supported narrow viewport. Users cannot confidently see or change what kind of service they are creating.

**Evidence:** [Mobile Service form](/tmp/faros-freeze-audit-added/edges/24-service-create-mobile.png), [geometry and control metrics](/tmp/faros-freeze-audit-added/edges/25-service-create-mobile-metrics.json)

**Reproducibility:** Always at 390×844

**Suggested priority:** P1

## Full findings

### [P2] Rightmost provider sections begin clipped and keyboard focus does not reveal them

**Category:** Accessibility

**Area:** Agents, Databricks, and Kuery top-level provider navigation

**User flow:** Discover or select the last provider section on a narrow viewport.

**Steps to reproduce:**

1. Open each provider at 390×844.
2. Inspect the rightmost tab: Agents `Models`, Databricks `Tables`, or Kuery `Playground`.
3. Move keyboard focus to the clipped tab without activating it.

**Expected behavior:** All primary sections are visible, or the overflow affordance and focused item are brought fully into view.

**Actual behavior:** The rightmost tab begins outside the visible strip. Agents `Models` ends at x=453.7 in a strip ending at x=374; Databricks `Tables` ends at x=395.5; Kuery `Playground` ends at x=412.5. Calling focus leaves each strip at `scrollLeft=0`, so the focused control remains clipped. Activation eventually scrolls the active tab, but discovery and focus visibility fail first.

**Impact:** Touch users may not discover the last provider section, and keyboard users can focus a control whose label and focus indicator are not fully visible.

**Evidence:** [Cross-provider focus/geometry probe](/tmp/faros-freeze-audit-added/crosscut/tab-probe.json), [Agents initial mobile state](/tmp/faros-freeze-audit-added/crosscut/mobile-agents.png), [Kuery initial mobile state](/tmp/faros-freeze-audit-added/data/kuery-mobile-topology-geometry.png)

**Reproducibility:** Always at 390×844

**Suggested priority:** P2

### [P2] Agents resource actions remain 28×28px on touch layouts

**Category:** Accessibility

**Area:** Agents → Connections, Toolsets, and Models

**User flow:** Edit, test, configure inbound access, or delete a resource on a touch device.

**Steps to reproduce:**

1. Render populated Agents resources at 390×844 with touch emulation.
2. Open Connections.
3. Measure the icon actions for Edit, Delete, Test message, and Enable inbound chat.

**Expected behavior:** Frequent and destructive touch actions provide a sufficiently large target consistent with the shell's 44px mobile controls.

**Actual behavior:** The icon actions remain 28×28px. Multiple adjacent actions are separated by only 4px.

**Impact:** Mobile users are more likely to miss or activate the wrong compact action, including Delete.

**Evidence:** [Populated mobile Connections](/tmp/faros-freeze-audit-added/agents/pop-mobile-03-connections.png), [measured controls](/tmp/faros-freeze-audit-added/agents/pop-keyboard.json)

**Reproducibility:** Always in the deterministic populated-state probe

**Suggested priority:** P2

### [P2] Agents Activity rows replace native table-row semantics with links

**Category:** Accessibility

**Area:** Agents → Activity and Agent detail → Runs

**User flow:** Navigate and understand run history with a screen reader.

**Steps to reproduce:**

1. Render a populated Activity or Runs table.
2. Inspect the accessibility semantics of an interactive run row.

**Expected behavior:** The table retains row and cell relationships while providing an accessible activation mechanism.

**Actual behavior:** Each native `<tr>` is assigned `role="link"` and `tabindex="0"`. The link role replaces its native row role.

**Impact:** Screen-reader table navigation may no longer expose the rows as part of the table structure, making multi-column run data harder to understand.

**Evidence:** [Populated Runs table](/tmp/faros-freeze-audit-added/agents/pop-02-agent-detail-runs.png), [row accessibility snapshot](/tmp/faros-freeze-audit-added/agents/pop-a11y-run-rows.json)

**Reproducibility:** Always in the deterministic populated-state probe

**Suggested priority:** P2

### [P2] Databricks Browse appears actionable when its required connection is absent

**Category:** UX

**Area:** Databricks → Warehouses → Browse registration

**User flow:** Browse Databricks warehouse metadata before configuring a connection.

**Steps to reproduce:**

1. Open `/ui/providers/databricks/create/warehouse/browse` in the empty workspace.
2. Observe `A connection is required` and the empty Connection field.
3. Select the enabled primary `Browse` action.

**Expected behavior:** Browse is unavailable until the prerequisite is satisfied, or the primary action directly advances the prerequisite recovery flow.

**Actual behavior:** Browse remains enabled with primary styling. Activating it leaves the wizard in place and adds a second error, `Select a connection.`

**Impact:** The interface invites an action it already knows cannot succeed, adding avoidable error friction to an otherwise clear prerequisite state.

**Evidence:** [Before Browse](/tmp/faros-freeze-audit-added/data/databricks-browse-no-connection-before.png), [after Browse](/tmp/faros-freeze-audit-added/data/databricks-browse-no-connection-after.png)

**Reproducibility:** Always in the empty workspace

**Suggested priority:** P2

### [P2] Databricks Tables actions squeeze explanatory copy into a 67px mobile column

**Category:** Visual

**Area:** Databricks → Tables

**User flow:** Understand the Tables collection and start an import on mobile.

**Steps to reproduce:**

1. Open `/ui/providers/databricks/tables` at 390×844.
2. Inspect the heading, description, Refresh action, and New table action.

**Expected behavior:** The collection description remains readable while both actions stay available.

**Actual behavior:** A 302px no-wrap header places 219px of actions beside the copy, leaving approximately 67px for the heading block. The description expands from one desktop line to seven short mobile lines and pushes the table downward.

**Impact:** The collection header becomes visually broken and unnecessarily tall, slowing scanning on the smallest viewport.

**Evidence:** [Confirmed mobile reproduction](/tmp/faros-freeze-audit-added/data/databricks-tables-mobile-heading-repro-confirm.png), [mobile geometry](/tmp/faros-freeze-audit-added/data/databricks-tables-mobile-heading-geometry.json), [desktop comparison](/tmp/faros-freeze-audit-added/data/databricks-tables-desktop-heading-geometry.json)

**Reproducibility:** Always at 390×844

**Suggested priority:** P2

### [P2] Kuery Playground editor ignores the dark theme

**Category:** Visual

**Area:** Kuery → Playground

**User flow:** Edit and run a QuerySpec in the default dark theme.

**Steps to reproduce:**

1. Open `/ui/providers/kuery` in dark theme.
2. Select `Playground`.
3. Inspect the CodeMirror editor.

**Expected behavior:** The editor participates in Violet Circuit dark theming while retaining readable syntax contrast.

**Actual behavior:** CodeMirror computes a white `rgb(255,255,255)` background and black `rgb(0,0,0)` base text inside the dark provider surface.

**Impact:** The editor dominates the screen with an unrelated light visual system and makes theme parity feel incomplete.

**Evidence:** [Desktop Playground](/tmp/faros-freeze-audit-added/data/kuery-07-playground.png), [computed mobile theme values](/tmp/faros-freeze-audit-added/data/kuery-mobile-playground-editor-theme.json)

**Reproducibility:** Always in dark theme

**Suggested priority:** P2

### [P2] Kuery loses the active view on reload

**Category:** Bug

**Area:** Kuery Topology, Inventory, and Playground

**User flow:** Refresh or share the current Kuery view.

**Steps to reproduce:**

1. Open `/ui/providers/kuery`.
2. Select `Inventory`.
3. Reload the page.

**Expected behavior:** Reload preserves the active view, and the current view has a stable browser location.

**Actual behavior:** The URL remains `/ui/providers/kuery` for every top-level view. Reload returns Inventory to the default Topology view and reruns the topology query.

**Impact:** Refresh, back/forward, and shareable-link workflows discard the user's current Kuery context.

**Evidence:** [Inventory before reload](/tmp/faros-freeze-audit-added/data/confirm-kuery-inventory-before-reload.png), [Topology after reload](/tmp/faros-freeze-audit-added/data/confirm-kuery-after-reload.png), [before state](/tmp/faros-freeze-audit-added/data/confirm-kuery-inventory-before-reload.json), [after state](/tmp/faros-freeze-audit-added/data/confirm-kuery-after-reload.json)

**Reproducibility:** Always

**Suggested priority:** P2

### [P2] Edges Marketplace Deploy controls are 28px high on touch layouts

**Category:** Accessibility

**Area:** Edges → Workloads → Marketplace

**User flow:** Deploy a marketplace workload from a touch device.

**Steps to reproduce:**

1. Open `/ui/providers/edges/workloads` at 390×844 with touch emulation.
2. Expand Marketplace.
3. Measure a workload's Deploy action.

**Expected behavior:** Primary per-workload actions have a touch target consistent with the provider's 44px mobile controls.

**Actual behavior:** Every Deploy control measures 82×28px.

**Impact:** Enabled deployment actions would be harder to tap accurately, especially in a long, vertically scrolling marketplace.

**Evidence:** [Mobile Marketplace](/tmp/faros-freeze-audit-added/edges/15-workloads-mobile.png), [touch metrics](/tmp/faros-freeze-audit-added/edges/16-mobile-metrics.json)

**Reproducibility:** Always at 390×844

**Suggested priority:** P2

## UX quality observations

- Edges, Agents, Databricks, and Kuery all reported `READY` and rendered without correlated console, page, request, or HTTP failures during healthy navigation.
- Empty states were generally truthful. Edges and Databricks explained missing prerequisites, Agents identified empty resource collections, and Kuery correctly reported that no Kubernetes edge was engaged.
- Edges creation and detail routes preserved cancel, back, reload, and browser-history behavior in the tested non-mutating flows.
- Agents route-owned forms and deep-link errors preserved hierarchy and recovery; its wide tables stayed inside local horizontal scrollers.
- Databricks consistently distinguished Connections, Warehouses, and Tables and rejected an invalid connection name before registration work.
- Kuery's bounded no-match Inventory query produced a truthful empty result without an error.
- The main recurring weakness is narrow-screen compression: tabs, controls, and heading/action layouts retain desktop assumptions inside the roughly 302px provider canvas.
- Agents empty cards do not carry an inline invitation action, but equivalent New actions remain visible in the page header; this was retained as an observation rather than inflated into a P3 issue.

## Areas tested

- [x] Authenticated catalog verification and enabled-provider delta
- [x] Edges first-edge automatic wizard
- [x] Edges Workloads and Services empty collections
- [x] Edges manual/marketplace creation entry, validation, cancel, back, missing details, reload, and history
- [x] Agents empty live collections
- [x] Agents deterministic populated Activity, Connections, Toolsets, Models, detail, configuration, run trace, filters, and pagination
- [x] Agents create-form entry, invalid input, cancel, deep links, reload, keyboard focus, touch sizing, and internal table scrolling
- [x] Databricks Connections, Warehouses, and Tables empty collections
- [x] Databricks manual creation and Browse registration prerequisite states
- [x] Databricks invalid input, cancel, and responsive collection headers
- [x] Kuery Topology, Inventory, and Playground
- [x] Kuery no-match filter query, API disclosure, reload, theme rendering, and mobile tab geometry
- [x] Desktop 1440×900/1000 and mobile 390×844 rendering
- [x] Cross-provider keyboard focus, touch-target, and overflow measurements
- [x] Impeccable implementation-integrity detector with contextual review

## Areas not tested

- **Edges populated flows:** no Edge, Service, or Workload existed. Populated detail composition, row actions, pagination, credentials, join completion, and reconciliation were unavailable.
- **Agents live mutations:** no agent was created, executed, approved, denied, deleted, or sent a chat message. Populated-state accessibility checks used deterministic intercepted fixtures rather than tenant mutations.
- **Databricks populated flows:** no Connection, Warehouse, or Table existed. Successful discovery, registration results, detail editing, and deletion were unavailable.
- **Kuery populated flows:** no Kubernetes edge was engaged, so graph nodes, expansion, relationship impact, and populated Inventory rows were unavailable.
- **Quickstart:** it is still not present in the authenticated provider catalog.
- **External side effects:** OAuth redirects, repository changes, cluster connection, deployment, credential mutation, and destructive actions were intentionally excluded.
- **Assistive technology and performance:** keyboard and DOM semantics were inspected, but no full screen-reader or load/stress session was run.

## Top five before release

1. Restore a usable Edges Service Type field on mobile.
2. Keep all provider tabs discoverable and fully visible when focused on narrow screens.
3. Preserve Kuery's active view across reload and browser navigation.
4. Bring Kuery's Playground editor into the active theme.
5. Correct the mobile interaction surface for Agents row actions and Edges Marketplace deployment.

## Existing baseline risk reconfirmed

The baseline audit's P1 dashboard responsive collapse is still present. With seven cards enabled, each dashboard card compresses into a narrow vertical strip at 390px, hiding nearly all provider names, metrics, and recent-resource content. Evidence: [expanded-provider mobile dashboard](/tmp/faros-freeze-audit-added/crosscut/mobile-dashboard.png). This remains part of the original report and is not duplicated in the new counts above.

## Heavy Route worker usage

| Worker role | Calls |
|---|---:|
| explorer | 4 |
| executor_luna | 0 |
| executor_sol | 0 |
| tester | 0 |
| doc-writer | 0 |

Three explorer workers performed the initial provider audits; one explorer received a bounded follow-up to independently confirm the Databricks mobile header defect.

All evidence is under `/tmp/faros-freeze-audit-added/`.
