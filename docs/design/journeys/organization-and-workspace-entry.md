---
{"schema":1,"id":"design.journeys.organization-and-workspace-entry","title":"Organization and workspace entry","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Describes host portal entry; backend provisioning and provider behavior are outside this entry."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/organization-and-workspace-entry.md#organization-and-workspace-entry","role":"design"},{"path":"portal/src/router/contextGuard.ts","role":"implementation"},{"path":"portal/src/router/landingPreference.ts","role":"implementation"},{"path":"portal/src/router/workspaceEntry.ts","role":"implementation"},{"path":"portal/src/pages/OrganizationsPage.vue","role":"implementation"},{"path":"portal/src/pages/WorkspaceChooserPage.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of organization and workspace entry","status":"passing","evidence":"Source reviewed; browser interaction, live provisioning, and assistive technology were not checked for this entry."}]},"relatedDocuments":[{"id":"design.patterns.navigation-and-feedback","relation":"related"},{"id":"design.patterns.resource-creation","relation":"related"},{"id":"design.journeys.sign-in-and-session-recovery","relation":"related"}]}
---

# Organization and workspace entry

This journey joins the host portal's entry decisions and recovery states. The
[navigation contract](../patterns/navigation-and-feedback.md) owns scoped URLs,
the workspace picker, and switching; [workspace onboarding](../patterns/resource-creation.md#workspace-onboarding)
starts after entry and describes tailoring the workspace.

## Find an operating scope

After sign-in, the landing route refreshes organization access. It resumes a
remembered organization only if still available, otherwise enters the sole
available organization or presents the organization chooser. It then refreshes
that organization's workspaces. A remembered available workspace is entered
only when usable; otherwise a sole available, usable workspace is entered.
A workspace marked for deletion is unavailable; a workspace without a cluster
name is not yet usable. Multiple available workspaces without a valid preference
lead to the chooser, even if only one is currently usable. Preferences are stored
per account, with the last workspace remembered separately per organization;
storage can be unavailable and does not replace the access check.

The organization chooser identifies personal or shared organizations and the
user's role. A Change organization visit can provide a validated local return
path: Back or continuing in the already active organization returns there; a
different organization goes to its workspace entry. Loading and navigation
failures expose Retry. An empty list offers organization creation.

While a retry is pending, the error and focused action remain visible; repeat
activation is blocked. When recovery removes the action, focus returns to the
chooser heading if the user has not moved it. Synthetic Chromium checks at
1440px and 390px covered initial-read recovery.
A separate fixture mounting the actual chooser with the host stylesheet and
Tailwind sources showed no horizontal overflow at those widths in either theme,
including an error and a long organization name. Live backend behavior remains
unverified.

Organization creation is a focused route before workspace entry. Keep its
single name field in the centered standalone entry shell and use the shared
create title, form surface, and right-aligned Cancel and Create actions inside
it. The form stays in place after a failed request. Successful creation
continues to workspace preparation for the new organization.

## Wait, choose, or recover

The workspace chooser checks organization readiness before refreshing the
workspace list. Usable rows open directly; preparing rows cannot be opened.
Lists longer than five offer search and duplicate display names include a short
ID. Pending provisioning is checked every 2.5 seconds for up to a minute;
Check again restarts the wait. A remembered or sole workspace can open when it
becomes usable. An empty organization offers workspace creation to admins or
members allowed by its policy; others are directed to an admin. Load and create
failures are shown with recovery actions. The page also links to organization
settings without requiring an active workspace.

## Evidence and boundaries

The [landing guard](../../../portal/src/router/contextGuard.ts), [preference storage](../../../portal/src/router/landingPreference.ts), [selection rule](../../../portal/src/router/workspaceEntry.ts), [organization chooser](../../../portal/src/pages/OrganizationsPage.vue), and [workspace chooser](../../../portal/src/pages/WorkspaceChooserPage.vue) are the source of this account. The [workspace chooser tests](../../../portal/src/pages/WorkspaceChooserPage.test.mjs) and [scoped navigation tests](../../../portal/src/router/scopedNavigation.test.mjs) use simulated state; they do not establish live provisioning or assistive-technology behavior. This entry records source review, not a browser or live-backend check.

The [design review catalog](http://127.0.0.1:5197/) provides the journey's source lens; start it with the [review setup](../../../hack/design-review/README.md). Its shell specimen provides visual context, not this entry flow or a live hub.
