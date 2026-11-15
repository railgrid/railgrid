---
{"schema":1,"id":"design.patterns.organization-billing","title":"Organization billing journey","kind":"journey","status":"active","authority":{"design":"informative","implementation":"reference"},"implementation":{"state":"shipped","notes":"The host portal implements the organization Plan & billing journey; availability and actions depend on metering, billing configuration, and the user's role."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/organization-billing.md#organization-billing-journey","role":"design"},{"path":"portal/src/components/OrgBillingCard.vue","role":"implementation"},{"path":"portal/src/components/BillingPlanComparison.vue","role":"implementation"},{"path":"portal/src/components/MeteringUsageList.vue","role":"implementation"},{"path":"portal/src/pages/TenantSettingsPage.vue","role":"implementation"},{"path":"portal/src/lib/billing-flow.ts","role":"implementation"},{"path":"portal/src/components/OrgBillingCard.behavior.test.mjs","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of host billing journey","status":"passing","evidence":"Documents current host components and focused behavior tests; this is not an end-to-end billing transaction."},{"kind":"browser","ref":"Live checkout and subscription lifecycle","status":"not-run","evidence":"Hosted billing, payment processing, and deployed synchronization were not exercised for this document."}]},"relatedDocuments":[{"id":"design.components.dashboard-usage","relation":"related","path":"docs/design/components/dashboard-usage.md"},{"id":"design.patterns.navigation-and-feedback","relation":"related","path":"docs/design/patterns/navigation-and-feedback.md"}]}
---

# Organization billing journey

The host's organization settings contain **Plan & billing**, an organization-wide
view of the plan, subscription, usage, and allowances. The dashboard's
[plan usage tile](../components/dashboard-usage.md) links to this view and can
open plan review. Workspace selection does not change the organization scope.
The details below describe the shipped host experience, not a contract for a
billing provider's hosted pages.

## Review usage and plans

The overview identifies the current plan, subscription and limit states, and
when available the current period end. A scheduled cancellation says when the
organization returns to its default plan; if the date is unavailable, it says so.
The usage list explains measurements and their limits. Hard limits deny creates
that exceed the allowance, soft limits flag excess without blocking, and metered
usage is measured without blocking. A missing measurement is not zero, and an
unspecified allowance does not establish unlimited capacity.

Where alternative plans exist, **Change plan** opens a focused comparison; a
meter at or over its allowance may offer a focused upgrade review. An organization
administrator can initiate an available checkout; members can review but not
change the plan. Operator-managed plans and hubs without metering or checkout
have different available actions and explanatory copy. A billing portal action
is offered to authorized administrators when billing is enabled and a
subscription status exists. A subscription other than canceled or expired
incomplete directs plan management to the billing portal instead of starting
another checkout.

## Hosted actions and return

Checkout and billing management open hosted flows. Failures stay in context with
an action to retry. Duplicate checkout submission is blocked. After a successful
checkout return, the portal checks both subscription and effective allowances
before reporting activation; the current plan remains in effect while confirmation
is pending. A delayed check offers **Check activation**, rather than another
checkout. A canceled checkout reports that the current plan is unchanged. Return
from billing management triggers a refresh.

An eligible saved workspace creation draft can be resumed when confirmed workspace
capacity is available. This does not create the workspace automatically.

## Loading, recovery, and scope

Initial loading is announced. A load failure offers retry; after a successful
load, a refresh failure retains the last data and labels it as such. Pending
allowances prompt refresh before plan comparison. The component discards stale
responses when the organization or signed-in session changes. Its focused behavior
tests cover return handling, duplicate submission, retries, and stale hosted
responses; they do not establish a successful live payment or deployed billing
synchronization.

## Review the journey

The [billing flow browser fixture](../../../hack/billing-flow-visual/README.md)
runs the production portal with synthetic API responses and hosted pages. Its
scenario selector covers permissions, pricing, errors, and return states;
checkout collects no payment. The fixture's browser check exercises the flow
and captures desktop and mobile views via `make test-billing-flow-visual` while
the fixture server is running. This complements the focused behavior tests and
does not verify a live billing provider or deployed synchronization.
