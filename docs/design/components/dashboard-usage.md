---
{"schema":1,"id":"design.components.dashboard-usage","title":"Dashboard plan usage","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Host dashboard component; compact meter presentation reuses the existing usage calculations and canonical progress primitive. This is not a distributed PortalKit component."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/dashboard-usage.md#dashboard-plan-usage","role":"design"},{"path":"portal/src/components/DashboardUsageTile.vue","role":"implementation"},{"path":"portal/src/components/MeteringUsageList.vue","role":"implementation"},{"path":"portal/src/pages/DashboardPage.vue","role":"implementation"}],"verification":{"state":"verified","checks":[{"kind":"test","ref":"make test-portal","status":"passing","evidence":"Final run passed all 652 tests with zero failures, including the rate-unit regression."},{"kind":"command","ref":"make build-portal","status":"passing"},{"kind":"command","ref":"make verify-portalkit","status":"passing"},{"kind":"command","ref":"make verify-ui-conformance","status":"passing","evidence":"Zero violations."},{"kind":"browser","ref":"2026-11-04 dashboard-usage-spacing light/dark desktop/mobile captures and checks.json","status":"passing","evidence":"1440px and 390px viewport checks show matching provider-card left/right edges and inner padding, a 16px gap below the final provider row, no overflow, placement outside the customizable grid and zero resize handles. Upgrade navigation reached the existing scoped plan-selection route. Final captures include the visible rate unit; accessible text and measurement titles also retain the unit."}]},"relatedDocuments":[{"id":"design.components.visual-primitives","relation":"implements","path":"docs/design/components/visual-primitives.md"},{"id":"design.patterns.fluid-shell","relation":"implements","path":"docs/design/patterns/fluid-shell.md"},{"id":"design.patterns.navigation-and-feedback","relation":"see-also","path":"docs/design/patterns/navigation-and-feedback.md"},{"id":"design.accessibility.interaction","relation":"prerequisite","path":"docs/design/accessibility/interaction.md"},{"id":"design.content.ui-copy","relation":"prerequisite","path":"docs/design/content/ui-copy.md"}]}
---

# Dashboard plan usage

## Purpose

Give people a compact view of their organization's current plan and reported
usage from each Workspace dashboard. The figures are shared across all
workspaces in the organization; selecting another workspace does not make them
workspace-specific.

## Use when

Use this default host dashboard tile whenever a workspace is selected, including
workspaces with no enabled Providers. Keep the organization scope visible beside
the plan heading.

## Avoid when

Use the existing organization **Plan & billing** surface for plan comparison,
subscription management, or detailed interactive meter explanations. Do not turn
this summary into a customizable Provider tile or give it a horizontal resize
handle.

## Anatomy and variants

The tile has a plan heading, organization-scope description, a quiet billing
link, compact meter rows, and a footer with measurement recency and Refresh.
Free plans offer **Upgrade plan**; other plans offer **Plan & billing**.

Each meter row links to the existing scoped **Plan & billing** view. The entire
row is a keyboard-focusable link, with a trailing chevron.

`MeteringUsageList` supplies `variant="compact"`. It keeps the existing meter
ordering, quantity formatting, usage/limit calculations and canonical progress
bar. This presentation has no search, filters or expandable explanations; the
existing interactive billing list retains those controls.

## Behavior

Fetch billing through the tenant store's organization-scoped `fetchOrgBilling`.
Initial loading shows a status message. Initial failure offers Retry; a refresh
failure preserves the previous measurements and explicitly identifies them as
the last available data. Refresh is disabled while loading. Missing organization
selection, unconfigured tracking, and an empty meter collection have distinct
messages, with no fabricated measurements or plan name.

Clear displayed billing on organization or signed-in-user changes. Abort obsolete
requests and fence responses against the current organization, request generation,
and authentication session. Unmounting also invalidates pending responses.

**Upgrade plan** navigates to the existing scoped organization settings route with
`panel=billing&review=1`, opening plan selection. **Plan & billing** uses
`panel=billing`. The summary does not initiate checkout or change a plan.

## Content

Use the returned plan display name, plan identifier, or default plan in that
order; show the generic **Plan usage** heading until tracking and a plan name are
available. Keep **Shared across all workspaces in this organization.** visible.

Show reported quantities beside configured limits. Missing measurements read
**Not reported**, rather than zero. Usage without a configured limit is still
usage, not proof of an unlimited allowance. Rate meters retain their visible
unit, including **cost units / min** for weighted request cost. The footer uses
the reported window end as **Usage recorded** with a localized timestamp, or
**Latest reported usage** if no valid timestamp exists; it makes no billing-period
or live-update claim.

## Layout and responsive behavior

The tile follows all provider rows below the customizable grid, spanning the
same left and right edges as the provider cards. Its wrapper uses `TILE_GAP`
for horizontal gutters; the grid's existing bottom gutter supplies the vertical
gap. When all provider tiles are hidden, the wrapper adds that gap itself.
Keep the same inner padding (`p-5`) as provider cards. It uses the incumbent
raised surface, subtle border,
typography, spacing and button recipes; no new tokens or visual identity are
introduced. Header, feedback and footer content can wrap.

Compact meter rows put labels beside quantities and progress bars. Each numeric
measure aligns to the end with a seven-rem bar on wider containers. At a
container width of 540px or less, the measure can wrap and its bar takes the
available measure width. Minimum widths are zero, and long labels or quantities
can wrap. The tile has no drag or resize behavior, including in dashboard
customization mode.

## Accessibility

The section is named **Plan usage**, contains an `h2`, and exposes loading through
`aria-busy`. Loading and failure messages use status semantics. Navigation uses
links; Refresh and Retry use native buttons. Decorative icons are hidden from
assistive technology, and the measurement timestamp uses a machine-readable
`time` value.

Progress bars expose a clamped zero-to-100 range, with the meter label and actual
used/maximum quantities in accessible text. Rate units are included in that text.
This preserves meaningful quantities when usage exceeds the maximum or the
maximum is zero. Warning/danger color supplements quantities and the explicit
over-limit text; color alone does not communicate the value. Inherit the shared
keyboard, focus and contrast contract; no separate accessibility audit is
claimed by the viewport captures.

## Code and evidence

The canonical host implementation is
[DashboardUsageTile.vue](../../../portal/src/components/DashboardUsageTile.vue),
mounted by [DashboardPage.vue](../../../portal/src/pages/DashboardPage.vue).
[MeteringUsageList.vue](../../../portal/src/components/MeteringUsageList.vue)
owns the shared calculations and compact rows;
[OrgBillingCard.vue](../../../portal/src/components/OrgBillingCard.vue) owns the
existing plan-selection destination.

[Dashboard behavior tests](../../../portal/src/components/DashboardUsageTile.behavior.test.mjs)
cover navigation, organization/session fencing, retained data on refresh failure,
retry and missing tracking. [Meter behavior tests](../../../portal/src/components/MeteringUsageList.behavior.test.mjs)
cover quantity and progress semantics. The final `make test-portal` run passed all 652 tests with zero failures,
including the rate-unit regression.
`make build-portal`, `make verify-portalkit` and `make verify-ui-conformance`
passed, with zero UI conformance violations.

Browser evidence dated 2026-11-04 is stored outside the repository at
`/home/tarkin/github/railgrid/artifacts/screenshots/dashboard-usage-spacing/`:
`light-1440.png`, `light-390.png`, `dark-1440.png`, `dark-390.png` and
`checks.json`. The checked viewports have no horizontal overflow, the tile is
outside and below the grid, and resize-handle counts are zero. Left and right
edges and inner padding match provider cards, with a 16px gap after the final
row at each checked size. The Free CTA reached the
scoped organization billing route with plan review requested; this does not
claim a completed purchase. The four final captures have zero browser errors and include the visible rate-unit correction;
accessible progress text and measurement titles also retain the unit.

## Related guidance

Follow the [fluid page composition](../patterns/fluid-shell.md),
[visual primitives](visual-primitives.md),
[navigation and feedback](../patterns/navigation-and-feedback.md),
[accessible interaction](../accessibility/interaction.md), and
[UI copy](../content/ui-copy.md) contracts. This entry extends the existing
[design knowledge base](../README.md); its dashboard-specific composition does
not change shared tokens or establish a second design authority.
