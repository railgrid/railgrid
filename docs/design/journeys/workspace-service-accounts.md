---
{"schema":1,"id":"design.journeys.workspace-service-accounts","title":"Workspace service accounts","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Describes the host workspace settings flow; token enforcement and backend authorization are outside this entry."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/workspace-service-accounts.md#workspace-service-accounts","role":"design"},{"path":"portal/src/pages/TenantSettingsPage.vue","role":"implementation"},{"path":"portal/src/components/CreateServiceAccountDialog.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of workspace service accounts","status":"passing","evidence":"Source review describes the portal flow; it does not establish live token enforcement or assistive-technology behavior."},{"kind":"browser","ref":"Workspace service-account lifecycle","status":"pending"}]},"relatedDocuments":[{"id":"design.journeys.member-access","relation":"related"},{"id":"design.patterns.resource-creation","relation":"related"},{"id":"design.components.copy-control","relation":"related"},{"id":"design.components.resource-table","relation":"related"},{"id":"design.patterns.resource-reads","relation":"related"}]}
---

# Workspace service accounts

Workspace settings gives workspace admins a service-account collection for machine
identities. The create dialog names the organization and workspace, asks for a
name and role, and defaults to Member. It explains that an Admin has
administrative access to the workspace. Creation and credential issuance are
separate: after creation, **Show in list** helps locate the new account, and
**Issue token** is available from its row actions. The dialog retains an error
on failure and cannot be dismissed while its request is busy. See the
[creation pattern](../patterns/resource-creation.md) for the shared composition.

The issued token is a one-time handoff. It starts masked, can be copied or
explicitly revealed, and is cleared on dismissal. The
[copy control](../components/copy-control.md#one-time-credential-handoff)
describes focus and clipboard recovery in detail. A token response is shown only
if its workspace, route, and administrative context are still current; a late
response after a context change is discarded from the display.

The row menu also offers **Revoke tokens** and **Delete service account**. Each
requires confirmation and explains the effect on existing token holders or
active tokens. The collection supports selecting multiple accounts for deletion;
the confirmation identifies the selected accounts and workspace. Bulk deletion
requires a current, successfully read list. The
[table contract](../components/resource-table.md) covers selection and partial
results, and the [read contract](../patterns/resource-reads.md) covers retained
snapshots and recovery. Access denial clears the displayed account list.

The [service-account fixture](http://127.0.0.1:5196/ui/11111111-1111-4111-8111-111111111111/22222222-2222-4222-8222-222222222222/settings/workspaces?preview=service-account)
uses synthetic data; choose **Actions → Issue token** to inspect the handoff.
It does not establish live issuance, revocation, or authorization. The complete
lifecycle has not been browser-audited here.
