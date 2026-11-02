---
{"schema":1,"id":"design.patterns.resource-creation","title":"Resource creation journeys","kind":"journey","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"partial","notes":"The route-owned standard and shared guidance/first-run components are shipped; existing creation flows adopt it incrementally. Providers self-hosting uses the production SelfHostWizard and route-owned install mode for durable console and manual resume, plus explicit asynchronous removal tracking."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/resource-creation.md#resource-creation-journeys","role":"design"},{"path":"provider-sdk/portalkit-vue/FirstRunGuide.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/CreateGuidance.vue","role":"implementation"},{"path":"portal/src/pages/ProvidersPage.vue","role":"implementation"},{"path":"portal/src/components/SelfHostWizard.vue","role":"implementation"},{"path":"portal/src/stores/orgProviders.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"review","ref":"provider creation-flow adoption review","status":"pending"},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"The global creation-flow audit remains pending; the scoped self-hosted provider progress check below does not verify other creation surfaces."},{"kind":"browser","ref":"Self-hosted provider installation progress","status":"passing","evidence":"Real-component browser fixtures verify compact Installing progress, failure-only backend messages and diagnostic disclosures, retry returning to compact progress, and Ready completion in light desktop and dark mobile. No live provider installation or workspace enablement was changed. Screenshots: artifacts/screenshots/compact-install-progress/."},{"kind":"browser","ref":"Self-hosted provider completion and advanced tools","status":"passing","evidence":"Live dev Quickstart Ready returns to the Self-Hosting list. Legacy manage URLs normalize without credential reads; Ready rows show the recorded hosting Edge, version, and health inline. Ready rows have no Advanced or Installation details action and do not fetch credentials. Light desktop and dark mobile checked without provider writes. Screenshots: artifacts/screenshots/ready-row-operations/."}]},"relatedDocuments":[{"id":"design.patterns.fluid-shell","relation":"prerequisite"},{"id":"design.components.first-run-guide","relation":"related"},{"id":"design.components.create-guidance","relation":"related"},{"id":"design.components.resource-back-link","relation":"related"},{"id":"design.components.confirm-dialog","relation":"see-also"},{"id":"design.quality.review-checklist","relation":"see-also"}]}
---

# Resource creation journeys

Use a focused, route-owned flow when creating an independently managed resource
or when creation needs prerequisites, sensitive input, multiple meaningful
decisions, or follow-up progress. Use a dialog, drawer, or inline control for a
compact addition whose meaning depends on the current parent. Do not insert a
substantial form into a collection page where it reflows or competes with the
collection.

Organization and workspace member additions, and the current workspace-scoped
service-account creation form, use compact dialogs. Their short identity-and-role
decision does not need a dedicated creation route. Put **Add member** or **Create
service account** in the collection card header; keep creation fields out of the
resting collection so they do not compete with table search. The service-account
case is an explicit exception to the route-owned default for independently
managed resources. Revisit it if creation gains substantial permissions setup,
external prerequisites, multiple steps, or resumable drafts.

Each dialog names the organization or workspace receiving access, uses persistent
field labels, explains the role, and defaults to **Member**. Workspace member
addition explains that access is workspace-only and points to organization
membership management when organization access is intended. Keep validation and
request errors inside the dialog and preserve its draft after a failed request.
On success, close the dialog, refresh the collection without discarding its
search or filters, and confirm the result. Service-account creation does not
automatically issue a secret: **Issue token** remains a separate, deliberate
action. Use the existing dialog focus, keyboard, and responsive conventions.

Choose the surface for the user's task, not field count, API shape, or
implementation convenience. Use the operation's truthful domain verb, such as
**Connect**, **Provision**, or **Deploy**. Use readable provider-owned routes;
avoid collisions between action routes and valid resource identifiers, preferring
`/create/<resource-type>` when detail routes use `/<collection>/:name`.

A route-owned flow has one back action, one title and description, one principal
form surface, and a right-aligned **Cancel → primary action** footer. Simple
forms are constrained; dense provisioning forms may fill the column. Wizards
keep this skeleton and put progress inside it instead of retaining dialog chrome.
Use an ordered list with the shared `k-wizard-steps` recipe for progress labels:
equal-width columns, uppercase mono text, and an accent underline on the item
with `aria-current="step"`. Give the list an accessible progress label. These
labels describe progress, rather than acting as navigation. Edges connection and
Databricks import use this recipe; providers retain ownership of step state and
route-specific layout.

For an authoritative first-use empty collection, use `FirstRunGuide.vue` (or
matching vanilla `k-first-run*` markup). It explains the value, offers the
immediate primary action, and shows the shortest meaningful journey. Missing
prerequisites change the current step and action rather than leaving an inert
empty table.

Agents connection creation uses the shared stepper for **Type → Configure →
Review**. Type selection retains the existing typed creation route. Returning to Type and
advancing to Review are local steps within that route: the URL continues to name
the selected connection type. Reloading that typed URL opens Configure with a
fresh draft; browser Back/Forward follow route history, not local step history.
The in-form Back action preserves the draft in memory without persisting secrets
to browser storage. This is intentional; Type before choosing a connection still
has its own untyped `/create/connection` route. Back between
steps preserves the draft when the same type is selected; selecting another type
resets its credentials and configuration. Continue uses native field validation;
only the Review step submits creation. Review shows identity and authentication
mode without echoing credential or endpoint values. Toolset creation remains a
single focused form. Connection and toolset field regions stay within `42rem`
while the surrounding route and guidance remain fluid. The connection wizard
keeps progress, the form body, and actions in one task column with a separate
context rail, following the Databricks import composition. Below a `900px`
surface width, guidance follows the task; at `1800px` and above, the optional
`k-wizard-steps--responsive` recipe presents progress vertically. The form owner
places that rail beside its body and footer; the canonical step recipe owns its
borders, active state, and typography.

When domain help is useful, put `CreateGuidance.vue` beside `k-create-fields`
inside `k-create-surface--guided`. The rail contains only timely prerequisites,
a live non-secret summary of what Railgrid will create, and controller-owned next
steps. Provider copy and value derivation remain local. A shared container query
puts fields before guidance on narrow surfaces and keeps both regions fluid at
desktop and 4K widths.

After creation, navigate to the resource when it owns status or recovery;
otherwise return to the collection with the result clearly visible. This is the
target standard; existing flows adopt it incrementally.

Required setup and recommended integrations must be distinguishable. A recommended
integration must offer an explicit skip action and must not block creation while
its connection is missing, validating, or failing. Preserve the creation draft
when the user visits setup or changes the integration choice. App Studio applies
this to Git: model setup is required for the assistant journey, while Git can be
connected later from project settings. Completion copy must describe the services
actually connected, not imply that skipping established a connection.

## Organization provider self-hosting

The Providers → Self-Hosting task uses the route-owned `SelfHostWizard` with
**Review → Install provider → Ready**. Review offers console installation as
the primary action and manual installation as a secondary action. Either action
is explicit; opening Review does not create the provider workspace or fetch a
credential. The wizard keeps provider installation separate from workspace
enablement, which remains a later, explicit consent flow.

Console installation records a durable operation, so the `install` route can
resume its progress after refresh without posting another install request.
Manual selection is retained by a non-secret route marker; restoring that route
continues readiness monitoring and retrieves instructions through the existing
credential endpoint for an authorized user. If an older or unmarked provider
has no console installation plan, the wizard treats its progress as manual or
unknown, monitors provider readiness, and requires an explicit action to show
manual commands. Once manual instructions are open, there is no switch to
console installation: the browser cannot determine whether any of the manual
steps have already changed the cluster.

Console progress shows a concise phase-aware status with an activity indicator
and a note that installation continues after leaving. Keep the task checklist,
backend implementation messages, installation metadata, and troubleshooting out
of the normal path. Show reported failure messages, available recovery actions,
and collapsed technical details only when installation fails or a request/status
refresh reports an issue. Do not infer which substep failed from a terminal
`Failed` phase. Status refresh errors preserve the last reported phase; offer
**Check again** after a terminal failure and describe automatic retry only while
status polling continues. The pending screen has one **Back to providers** action
because leaving does not cancel the durable install. Workspace enablement remains
a separate explicit action. During Review, show a loading status until connected
clusters have been checked; report that no eligible target exists only after that
result is known.

On Ready, **Continue** returns to the Self-Hosting list and focuses its heading;
it does not open another management step or enable a workspace. A ready provider
row shows the recorded hosting Edge and workspace, installed version, and health
inline. Ready rows do not offer installation details or advanced credential tools.
Manual commands and credentials remain in the manual installation flow. Workspace Enable and Disable remain in Catalog. When an org
provider shadows a platform provider, label the workspace's actual copy only
from a ready, current binding projection; otherwise report the copy as unknown.
When that projection identifies a copy but the catalog entry is not Ready, show
both facts in the status label.
Disabling the platform copy requires a confirmation that warns resources may be
deleted and describes the separate later Enable action for the org copy. Recheck
the binding identity and tenant scope after confirmation before disabling.

The retired `manage` query is normalized to the Self-Hosting list with the query
cleared. It must never fetch installation credentials or enable workspace access.

The Self-Hosting list refreshes health every 15 seconds while visible and when
returning to the tab. Pause this polling while the document is hidden or the
installation wizard is open, and stop it on unmount. Preserve last-known rows on
failure and offer an inline **Retry**; do not show a permanent refresh button.
Confirm completed removal through the shared scoped success toast rather than a
persistent notice in the list.

Removal deletes the organization provider workspace and its API registration;
it does not uninstall the Helm release or workloads from the cluster, or disable
bindings in workspaces. Keep a **Removing** row while kcp completes asynchronous
workspace deletion, and report completion only after the organization inventory
no longer contains that provider.


## Workspace onboarding

The host’s [WelcomeWizard](../../../portal/src/components/WelcomeWizard.vue)
uses three steps: context, goal, and a starting Workspace preview. Context
suggests a goal; an explicit goal choice takes precedence. The preview walks
the live catalog’s requirements in dependency order, reports missing Providers,
and uses the existing ProviderEnableDialog for each consent decision, including
cross-Provider composition access. It never enables a bundle implicitly.
A ready, enabled Provider with a web UI and satisfied dependencies offers a
Workspace-scoped link into its workflow; enablement is not task completion. Workspace members who are also Organization
members can launch directly from goal selection when the selected Provider and
all transitive prerequisites are enabled and ready, with a web UI and current
Workspace binding data. Admins retain the setup preview. This shortcut performs
no enablement or permission changes.

**Not sure, I’m exploring** continues to a neutral introduction and the scoped
Provider catalog without selecting or enabling a Provider. Back returns to goal
selection. **Go to dashboard** dismisses the guide, and Getting started on the
dashboard reopens it at **Your goal**, preserving a previous concrete selection.
A saved exploration choice resets to the audience’s default goal so one radio
choice is selected on re-entry. Automatic
entry happens once per signed-in user in this browser, marked when the guide is
shown rather than dismissed. Creating or switching Workspaces and reloading an
unfinished journey do not reopen it. Legacy dismissal history has no account owner and is not assigned to the current
user. This intentionally includes configured Workspaces, so invited members can
choose their own workflow. On rollout, existing accounts without the new
account-owned marker also receive this one-time introduction; legacy unowned
Workspace dismissals cannot safely identify who completed it. This is a
browser-first-visit preference, not a server-side account creation check.
Clearing storage or changing browsers can show it again.
Choices remain browser-local and scoped by user, Organization, Workspace, and mode;
changing scope invalidates outstanding consent.


While open, Getting started hides all shell navigation positions and persistent
terminal and agent docks without changing preferences or ending sessions.
Dismissal or route exit restores the shell. Catalog refreshes keep the wizard
mounted. Native radio groups, associated descriptions, heading focus, and the
shared wizard progress recipe support keyboard and screen-reader navigation.
There is no Workspace eyebrow or top skip action.

The card is centered horizontally and vertically in the viewport when it fits;
taller content starts at the top and remains scrollable.
All steps share one bordered 640px-maximum card, a common 540px minimum height
capped by the available viewport, and a footer separated by a subtle divider.
The body absorbs unused space to keep the card and footer stable across the
common three-step journey. Use the standard functional type scale: 16px panel
headings, 13px choice headings, 13px supporting copy, and shared controls.

At widths of 640px and above, goal choices use two columns. Provider setup
uses a single compact list with names, categories, status, dependency warnings,
and explicit enable actions. Optional task prerequisites and full Provider
descriptions are available in the keyboard-accessible **Setup details** disclosure.
Opening details allows the card to grow naturally. Compact vertical spacing fits the common three-Provider journey at 1280×720 and 1440×800. On phones the
minimum height is removed. Long catalogs, errors, narrow and zoomed layouts
retain natural scrolling; never clip content to satisfy a viewport target.

Validation uses portal regression tests and the isolated real-component fixture:
`make serve-onboarding-visual`, then `make test-onboarding-visual`. Supply
`PLAYWRIGHT_MODULE` and `CHROMIUM_EXECUTABLE` if these are not installed in the
usual locations. The fixture uses the production wizard, shell, stores, and
consent dialog with synthetic catalog data and mocked network actions. It covers
light/dark laptop, tablet, and phone layouts, card/footer geometry, exploration,
restoration, and consent forwarding. It does not verify live Provider
provisioning or completion of downstream tasks.

### Onboarding language and audience

Team setup asks what the team should be able to do, describes Providers as tools
that tailor a Workspace, and completes at the Workspace dashboard. Individual
project work keeps task-oriented goals and opens the selected Provider. Selecting
a team starting point is a presentation preference, not a membership or permission
change. Only confirmed Workspace or Organization admins receive setup controls;
other users follow the member path regardless of their audience preference.
Unknown roles keep the availability check pending. Confirmed members using the
individual-work path can launch ready tools directly after choosing a goal.

Provider rows show a short purpose before enablement; supporting Providers explain
their role in the chosen task. Destination actions use the catalog display name.
Enabling makes tools available; creating an app, agent, or service happens inside
the Provider. Permission copy distinguishes Workspace and organization access and
avoids credential implementation promises. Provider tiles are 36px with 20px icons;
readable text and the consistent onboarding frame are preserved.

All Provider launch paths require current Workspace bindings and enabled, ready
transitive prerequisites. Retained stale binding data may display status, but
cannot enable the launch action.

Members choosing individual project work never see Provider enable controls in
onboarding. Ready tools open directly from the goal step. Missing or unavailable
tools lead to an administrator handoff with a copyable request and an always
available return to the Workspace. Copying does not send a message. Unknown or
stale availability offers a retry, not a setup diagnosis. Once availability is
confirmed, the same screen can offer the direct launch. Exploration returns
members to their existing Workspace tools rather than asking them to enable more.
