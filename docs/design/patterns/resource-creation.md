---
{"schema":1,"id":"design.patterns.resource-creation","title":"Resource creation journeys","kind":"journey","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"partial","notes":"The route-owned standard and shared guidance/first-run components are shipped; existing creation flows adopt it incrementally."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/patterns/resource-creation.md#resource-creation-journeys","role":"design"},{"path":"provider-sdk/portalkit-vue/FirstRunGuide.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/CreateGuidance.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"Byte-for-byte PortalKit copy and manifest parity passed; this does not verify rendered or interactive behavior."},{"kind":"review","ref":"provider creation-flow adoption review","status":"pending"},{"kind":"browser","ref":"PortalKit rendered and interaction audit","status":"pending","evidence":"No browser or mounted behavior audit was run in this checkout."}]},"relatedDocuments":[{"id":"design.patterns.fluid-shell","relation":"prerequisite"},{"id":"design.components.first-run-guide","relation":"related"},{"id":"design.components.create-guidance","relation":"related"},{"id":"design.components.resource-back-link","relation":"related"},{"id":"design.components.confirm-dialog","relation":"see-also"},{"id":"design.quality.review-checklist","relation":"see-also"}]}
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
