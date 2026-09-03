# Code-freeze UX/regression audit

Target: `https://console.dev.kyrosos.com/ui/` using `dev-token`.

No source, configuration, dependencies, tests, infrastructure, product data, or Git state were changed during the audit.

## Executive summary

- Overall release confidence: **Low**
- Findings: **P0: 0 · P1: 1 · P2: 9 · P3: 0**
- Release blocker: the dashboard is effectively unreadable at mobile widths.
- Most concerning patterns:
  - Responsive behavior still assumes a desktop-sized canvas.
  - Failed reads are sometimes misrepresented as authoritative empty states.
  - Destructive-action and error-announcement accessibility is inconsistent.
- Only App Studio, Code, and Infrastructure were registered in the target catalog. Five other provider journeys could not be validated.

### Technical audit health

| Dimension | Score | Key finding |
|---|---:|---|
| Accessibility | 2/4 | Lost focus, destructive default focus, missing live announcements, small touch targets |
| Performance | 3/4 | No user-facing performance failure or healthy-route console/network error observed |
| Responsive design | 1/4 | Dashboard breaks at narrow widths |
| Theming | 4/4 | Dark/light parity and reload persistence worked |
| Implementation integrity | 2/4 | Several unknown-versus-empty and state-truthfulness failures |
| **Total** | **12/20** | **Acceptable, but significant release work remains** |

## Release blockers

### [P1] Dashboard tiles collapse into unreadable columns

**Category:** Possible Regression

**Area:** Dashboard

**User flow:** Review provider summaries and open a provider on a narrow viewport.

**Steps to reproduce:**

1. Authenticate in a fresh 390×844 browser context.
2. Open `/ui/`.
3. Alternatively, navigate around at desktop width and then resize to 390px.

**Expected behavior:** Tiles stack into usable columns and respect the implemented 240px intended minimum.

**Actual behavior:** The 302px dashboard canvas renders the three populated tiles side-by-side at 56px each. Labels and resource controls are clipped or reduced to fragments. Fresh-session measurements remained undersized through 1024px: 108px at 600, 142px at 768, 175px at 900, and 206px at 1024.

**Impact:** The primary landing surface is unusable for reviewing or selecting provider resources on mobile.

**Evidence:** [Mobile screenshot](/tmp/faros-freeze-audit/console-route-roundtrip-desktop-to-mobile.png), [breakpoint metrics](/tmp/faros-freeze-audit/dashboard-breakpoints/metrics.json)

**Reproducibility:** Always

**Suggested priority:** P1

## Full findings

### [P2] Provider fetch failure also claims the catalog is empty

**Category:** Error Handling

**Area:** Providers catalog

**User flow:** Recover when the provider inventory cannot be loaded.

**Steps to reproduce:** Deterministically return 503 from `GET /api/providers`, then open `/ui/providers`.

**Expected behavior:** Show that inventory is unavailable or unknown, with a recovery path.

**Actual behavior:** `provider list failed: 503 Service Unavailable` appears alongside `No providers installed yet.`

**Impact:** Users may conclude that providers were removed or never installed when the system simply failed to read them.

**Evidence:** [Screenshot](/tmp/faros-freeze-audit/console-error-providers-clean.png)

**Reproducibility:** Always under the 503 probe

**Suggested priority:** P2

### [P2] Workspace fetch failure also claims the organization is empty

**Category:** Error Handling

**Area:** Settings → Workspaces

**User flow:** Inspect workspaces during an API failure.

**Steps to reproduce:** Deterministically return 503 from the workspace-list request, then open `/ui/settings/workspaces`.

**Expected behavior:** Preserve an unknown/unavailable state with Retry.

**Actual behavior:** The 503 error and Retry action are followed by `No workspaces in this organization yet.`

**Impact:** Operators can misinterpret a temporary read failure as missing workspace configuration.

**Evidence:** [Screenshot](/tmp/faros-freeze-audit/console-error-settings-clean.png)

**Reproducibility:** Always under the 503 probe

**Suggested priority:** P2

### [P2] Getting-started flow calls a populated workspace empty

**Category:** UX

**Area:** Dashboard onboarding

**User flow:** Reopen onboarding after configuring the workspace.

**Steps to reproduce:**

1. Open the populated dashboard containing App Studio, Code, and Infrastructure.
2. Select `Getting started`.

**Expected behavior:** Generic onboarding or copy acknowledging that the workspace is already configured.

**Actual behavior:** The wizard states, `This workspace is empty.`

**Impact:** The product contradicts visible resource state and makes users question whether they are operating in the correct workspace.

**Evidence:** [Screenshot](/tmp/faros-freeze-audit/console-dashboard-guide-open.png)

**Reproducibility:** Always

**Suggested priority:** P2

### [P2] Sidebar expansion squeezes mobile content and drops keyboard focus

**Category:** Accessibility

**Area:** Global navigation shell

**User flow:** Expand the navigation rail on mobile or by keyboard.

**Steps to reproduce:**

1. Open any shell page at 390×844.
2. Activate `Expand sidebar`.

**Expected behavior:** Navigation remains usable without collapsing the working canvas, and focus stays on the equivalent `Collapse sidebar` control.

**Actual behavior:** The sidebar consumes 192px, leaving only 198px for the application. Focus moves to `BODY`, forcing keyboard users to rediscover their position.

**Impact:** Content becomes severely cramped, while keyboard users lose navigation context.

**Evidence:** [Expanded mobile shell](/tmp/faros-freeze-audit/mobile-sidebar/expanded.png), [focus evidence](/tmp/faros-freeze-audit/shell/shell.json)

**Reproducibility:** Always

**Suggested priority:** P2

### [P2] MCP deletion confirmation initially focuses Delete

**Category:** Accessibility

**Area:** MCP Access

**User flow:** Review deletion of an MCP server.

**Steps to reproduce:**

1. Open the delete action for the `default` MCP server.
2. Inspect initial focus.

**Expected behavior:** Initial focus lands on Cancel, or opening the confirmation cannot make a single Enter key immediately destructive.

**Actual behavior:** `Delete` receives initial focus.

**Impact:** A user operating by keyboard can unintentionally confirm deletion immediately after opening the dialog.

**Evidence:** [Screenshot](/tmp/faros-freeze-audit/console-mcp-delete-confirm-final.png), [focus snapshot](/tmp/faros-freeze-audit/console-mcp-delete-confirm-final.json)

**Reproducibility:** Always

**Suggested priority:** P2

### [P2] Visible errors are not consistently announced

**Category:** Accessibility

**Area:** Login and Providers

**User flow:** Recover from invalid authentication or provider-loading failures.

**Steps to reproduce:**

1. Submit an invalid token.
2. Separately render the provider-list 503 state.
3. Inspect alert/live-region semantics.

**Expected behavior:** Visible errors are exposed through an alert or appropriate live region.

**Actual behavior:** `token login failed: 401` and the provider failure have no alert/live-region semantics.

**Impact:** Screen-reader users may receive no notification that the attempted action failed.

**Evidence:** [Invalid-token screenshot](/tmp/faros-freeze-audit/console-login-invalid-token.png), [semantic snapshot](/tmp/faros-freeze-audit/console-login-invalid-token.json)

**Reproducibility:** Always

**Suggested priority:** P2

### [P2] Code not-found pages mislabel the provider as unavailable

**Category:** Error Handling

**Area:** Code connection and repository details

**User flow:** Open an obsolete or mistyped deep link.

**Steps to reproduce:**

1. Open `/ui/providers/code/connections/no-such-connection`, or
2. Open `/ui/providers/code/repositories/no-such-repo`.

**Expected behavior:** A concise resource-not-found state while retaining Code’s visible `READY` context.

**Actual behavior:** `NotFound` is combined with `Provider unavailable`, `Type unavailable`, and `UNAVAILABLE`.

**Impact:** Users cannot tell whether the resource is missing, the resource type is unsupported, or the entire provider is down.

**Evidence:** [Connection screenshot](/tmp/faros-freeze-audit/providers/code-code_connections_no-such-connection.png), [repository screenshot](/tmp/faros-freeze-audit/providers/code-code_repositories_no-such-repo.png)

**Reproducibility:** Always

**Suggested priority:** P2

### [P2] Ready Infrastructure instance reports zero child resources without explanation

**Category:** UX

**Area:** Infrastructure instance detail

**User flow:** Confirm that the `app-studio-browser` instance is fully provisioned.

**Steps to reproduce:**

1. Open the instance detail.
2. Reload the page.

**Expected behavior:** Ready state and resource inventory are mutually understandable, or the UI explains why reported child resources are unnecessary.

**Actual behavior:** `READY` and `ResourcesReady=True` coexist with `CHILD RESOURCES 0 — Reported by controller` and no explanation.

**Impact:** Operators cannot confidently determine whether reconciliation is complete or inventory reporting is broken.

**Evidence:** [Instance detail](/tmp/faros-freeze-audit/providers/infrastructure-instance-detail-browser.png)

**Reproducibility:** Always in repeated loads

**Suggested priority:** P2

### [P2] App Studio composer uses undersized touch controls

**Category:** Accessibility

**Area:** App Studio project conversation

**User flow:** Attach files, select response settings, or send a message on mobile.

**Steps to reproduce:** Open `/ui/providers/app-studio/cat-care-app` at 390×844.

**Expected behavior:** Primary touch controls remain at least 44×44px.

**Actual behavior:** `Add files` measures 28×28px, `Send` 32×32px, and response/approval/model controls are 32px high.

**Impact:** Frequent conversation controls are harder to activate accurately on touch devices.

**Evidence:** [Screenshot](/tmp/faros-freeze-audit/ai/console-dev-kyrosos/responsive-detail.png)

**Reproducibility:** Always

**Suggested priority:** P2

## UX quality observations

- Dark and light themes remained coherent across the host, App Studio, Code, Infrastructure, Providers, and MCP. Theme selection persisted across reload.
- Healthy routes produced no correlated page errors, failed requests, or HTTP responses ≥400.
- App Studio’s deep links, browser history, preview, model validation, and delayed publishing-state resolution behaved truthfully.
- Code and Infrastructure preserved cancellation/back behavior and rejected invalid creation input without mutations.
- Wide tables used contained horizontal scrolling rather than expanding the entire page.
- The recurring weakness is state semantics: unavailable data, missing resources, provider readiness, and empty inventories are not always kept distinct.
- The mechanical design detector only flagged the shared progress bar’s width transition; contextual review found that to be a semantic progress animation, not a recorded defect.

## Areas tested

- [x] Valid and invalid token login
- [x] Dashboard, onboarding entry, provider summaries, responsive breakpoints
- [x] Sidebar, workspace switcher, account menu, help and CLI dialogs
- [x] Providers Catalog, Self-Hosting entry, search and category filters
- [x] Workspace and organization settings
- [x] MCP collection, create validation, missing detail, reload/history, deletion confirmation
- [x] App Studio collection/detail/create entry, models, composer, preview, publishing, integrations, settings, skills, code and instances
- [x] Code connections, repositories, packages, details, invalid creation, cancel/back/reload
- [x] Infrastructure templates, filters, instances, details, invalid provisioning, cancel/back/reload
- [x] Dark/light themes
- [x] Desktop and 390px mobile rendering
- [x] Deterministic provider/workspace 503 states

## Areas not tested

- **Agents, Databricks, Edges, Kuery, Quickstart:** absent from the authenticated target catalog. Direct routes correctly reported them unavailable.
- **Kuery:** local Tilt additionally reports a runtime error caused by API discovery/cache initialization failure; no Kuery UI was reachable.
- **Self-Hosting completion:** no eligible connected cluster existed, and its connect-cluster CTA led to the unavailable Edges provider.
- **Successful destructive or externally consequential mutations:** no MCP deletion, GitHub repository creation, workload provisioning, publishing, or agent execution was performed on the shared dev console.
- **Multiple-workspace switching:** only one workspace was available.
- **Full screen-reader validation and load/stress performance testing:** keyboard and DOM semantics were inspected, but no assistive-technology session or stress test was run.

## Top five before release

1. Dashboard responsive collapse.
2. Provider failure being represented as an empty catalog.
3. Workspace failure being represented as an empty organization.
4. MCP deletion defaulting focus to the destructive action.
5. Infrastructure `Ready` state versus unexplained zero child-resource inventory.

## Heavy Route worker usage

| Worker role | Calls |
|---|---:|
| explorer | 3 |
| executor_luna | 0 |
| executor_sol | 0 |
| tester | 0 |
| doc-writer | 0 |

All screenshots and logs are under `/tmp/faros-freeze-audit/`.
