---
{"schema":1,"id":"design.journeys.member-access","title":"Organization and workspace member access","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Describes the host settings membership flow; backend authorization and provisioning are outside this entry."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/member-access.md#organization-and-workspace-member-access","role":"design"},{"path":"portal/src/pages/TenantSettingsPage.vue","role":"implementation"},{"path":"portal/src/components/AddMemberDialog.vue","role":"implementation"},{"path":"portal/src/components/MemberList.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of member access","status":"passing","evidence":"Source reviewed; this does not establish live authorization, backend cleanup, or assistive-technology behavior."},{"kind":"browser","ref":"Member access journey","status":"pending"}]},"relatedDocuments":[{"id":"design.journeys.organization-and-workspace-entry","relation":"related"},{"id":"design.patterns.resource-creation","relation":"related"},{"id":"design.components.resource-table","relation":"related"},{"id":"design.patterns.resource-reads","relation":"related"}]}
---

# Organization and workspace member access

Settings presents organization and workspace membership as separate scopes. The
add dialog identifies the target scope and organization, defaults to Member,
and explains the selected role. An organization admin has administrative access
to all workspaces; an organization member needs separate workspace access.
A workspace admin manages that workspace; a workspace member can edit its
resources. The dialog accepts an email or member ID and offers matching users,
excluding people already in the displayed roster. Arrow keys navigate the
suggestions, Enter selects one, and Escape dismisses the suggestion list first.
While adding, the dialog stays busy; a failed add keeps it open with an error.
Successful addition closes it, refreshes the roster, and offers **Show in list**.
The [creation contract](../patterns/resource-creation.md) describes the dialog
composition and its scope guidance.

Admins can change roles or remove members from the relevant roster, subject to
the current scope and permissions. Removal requests confirmation. The
[table contract](../components/resource-table.md) describes bulk removal,
self-removal, and the additional cleanup and retry implications of removing an
organization member. Refresh failures and access loss follow the
[resource read contract](../patterns/resource-reads.md); access denial clears
the sensitive roster. Scope and authority are checked again around asynchronous
actions so feedback from an old context does not apply to a new one.

The [table review fixture](http://127.0.0.1:5197/?entry=design.components.resource-table)
uses sample data to exercise selection and recovery; it does not run membership
requests or demonstrate authorization. The membership journey itself has not
been browser-audited here.
