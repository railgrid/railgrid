# Provider UI audit October 8 2026

The audit covers all seven providers enabled in the local Tilt workspace:
Agents, App Studio, Code, Edges, Infrastructure, Kuery and Quickstart. The
screenshot’s model connection was already a component, but its ground-colored
surface erased the card boundary. Both model collections now use independent
raised cards with the shared hairline, 6px corners and compact responsive grid.
Final Models captures: [light theme](models-light.png) and
[dark theme](models-dark.png).

The review follows the active [Railgrid design book](../../design/README.md),
including its declared exceptions. It covers every provider-owned Vue component,
Quickstart’s vanilla page and tile, Kuery’s vanilla tile, their supporting visual
and state modules, and the canonical shared kits. Vendored copies are outputs;
all shared changes were made canonically and distributed with
`make sync-portalkit`.

## Component coverage

| Provider | Owned Vue components | Source audit and principal fixes |
| --- | ---: | --- |
| Agents | 27 | [Complete inventory](agents-app-studio.md#agents-complete-local-component-coverage): raised model cards, page/KPI typography, badge glyphs, field names/help, chart alternatives, narrow layouts, touchable disclosures, consistent control corners and token-renewal snapshot retention. |
| App Studio | 34 | [Complete inventory](agents-app-studio.md#app-studio-complete-local-component-coverage): styled menu overlay ownership, panel/control corners, labels, clarification state, touch targets, retained projects/conversations on renewal, busy model retry, typed-key clearing on endpoint change. |
| Code | 9 | [Complete inventory](edges-code.md#code-component-coverage): responsive headers/errors, technical font role, useful dashboard snapshots, caller fencing, retained route views and creation drafts across renewal. |
| Edges | 11 | [Complete inventory](edges-code.md#edges-component-coverage): shared wizard creation hierarchy/footer, responsive fields, persistent short labels, recoverable prerequisite reads, restrained section actions and useful dashboard snapshots. |
| Infrastructure | 10 | [Complete inventory](other-providers.md#infrastructure): wrapping/stacked values and headers, clipboard recovery, user-aware context watches, empty first-use guide, touchable value links and one Conditions heading. |
| Kuery | 5 | [Complete inventory](other-providers.md#kuery): persistent filter labels, foreground query feedback, busy retries, fluid layouts and useful user-scoped saved-view summaries. |
| Quickstart | Vanilla page and tile | [Complete inventory](other-providers.md#quickstart): dedicated Greeting creation route, persistent failed/cancelled draft, fluid collection, task-facing copy, semantic recovery and snapshot retention. |

The [shared inventory](shared-components.md) covers all **22 PortalKit Vue
components and 27 AgentKit Vue components**, plus vanilla controls and visual
recipes. Together the source review covers **145 Vue components** and all
provider-owned vanilla surfaces. Unchanged components were inspected as well.

## Shared corrections

- Raised model surfaces in both themes; model icons retain decorative semantics.
- Archivo at 125% for page titles; fixed creation typography, 14px back arrows
  and the documented heading focus ring; mono condition timestamps.
- Stable authoritative empty tables during background refresh; useful error
  retention, immediate retry feedback and serialized activation.
- Searchable popup Tab follows source order; cached panes close their popups.
- Table/filter controls honor coarse and hybrid input. Workbench search uses
  a 44px touch target and 4px control corners.
- Dashboard secondary text uses the full muted token.
- Parent-owned Conditions sections can suppress the duplicate inner heading;
  controller messages receive the available table width.

Core stylesheet/loader markers advance together from 31 to 32; AgentKit from
9 to 10. All copies and compatibility tests were updated together.

## Validation

All seven provider production builds and typechecks passed. Relevant regression
suites passed: Agents 646 tests, Code 148, Edges 186, Infrastructure 123, Kuery
52 and Quickstart 17. App Studio’s full suite and focused correction reruns are
recorded in its component report; final model/context/retry regressions and
production budget passed without increasing its ceilings. The host portal
build and full suite also passed.

Shared copy parity, AgentKit loading/conversation contracts, UI conformance and
design-document checks passed. The host suite passed all 364 tests. The source detector
scanned 81 changed canonical/provider UI files with no findings; its narrow
image-comment false-positive triage is documented in the App Studio report.

The authenticated live sweep attempted **36 URLs × two themes × two widths
(1440px and 390px)**. It included populated model/connection/edge/service and
instance details, empty collections, prerequisites and creation pages. Four
captures were interrupted by Tilt/Vite rebuilds. Focused confirmation covers
those pages, actual labelled touch areas, menus and keyboard focus, model-card
appearance, Infrastructure details, Kuery tabs and Quickstart draft navigation.
Agents’ legacy creation routes use its own hash navigation; direct host create
URLs returned the collection, so actual creation surfaces are checked through
their navigation buttons rather than counted as direct URL coverage.
The route evidence is summarized in [browser evidence](browser-evidence.json).

The repaired `make test-model-form-visual` fixture mounts the real providers and
passes **16 captures**, 360 individual checks and 56 checks across eight
shared geometry comparisons. [Fixture details](model-forms.md) record the scope. It checks
fonts, title focus, control geometry, labels/help, stacking, overflow and runtime
errors, and verifies that provider/endpoint edits clear typed keys with zero
writes. Full unmasked PNG differences remain observational: provider-specific
guidance and verification notices intentionally differ. The fixture makes no
paid model request and validates no real credential.

## Verification limits

The complete component inventory is a source audit, supplemented by automated
behavior checks and the stated live coverage. It does not claim every
conditional branch was exercised against real data. Active approvals, streams,
runner credentials, deployment convergence, invited members, chart output and
similar states need their corresponding resources. Kuery’s richer graph keeps
its existing assistive-technology/color-blind validation boundary; list
alternatives remain available. This is not a blanket WCAG certification.

The audit changes no provider API, permission claim, publishing configuration
or production deployment. Interaction checks use navigation and local drafts,
with no enrollment, provisioning, publishing or paid model requests.
Kuery had no connected Kubernetes edge: its Inventory and Playground
buttons retained the setup guide, so data-view and Impact rendering remain
unavailable in this cluster.
