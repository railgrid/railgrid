---
{"schema":1,"id":"design.journeys.app-studio-sharing","title":"App Studio sharing","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Describes the share dialog; backend enforcement and the complete deployment journey are outside this entry."},"appliesTo":["app-studio"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/app-studio-sharing.md#app-studio-sharing","role":"design"},{"path":"providers/app-studio/portal/src/ProjectShareDialog.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/publishingState.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of App Studio sharing","status":"passing","evidence":"Dialog source review; does not establish live access enforcement or assistive-technology behavior."},{"kind":"browser","ref":"App Studio sharing lifecycle","status":"pending"}]},"relatedDocuments":[{"id":"design.journeys.member-access","relation":"related"},{"id":"design.components.copy-control","relation":"related"},{"id":"design.components.confirm-dialog","relation":"related"}]}
---

# App Studio sharing

The share dialog separates **Production app** and **Preview** access. Production
must be deployed before its audience can be chosen or its link shared. Each
available channel offers a restricted audience (workspace and invited people) or
anyone with the link, and each is saved separately. Public access can still leave
individual app features requiring sign-in. For a public Preview the dialog also
warns that the mutable preview can expose test data and future changes.

Restricted access includes workspace members and may include individually invited
people. The dialog offers a workspace-member selector and an email invitation;
viewer management requires a saved restricted mode. A change of audience must be
saved before managing people. Switching channels with an unsaved audience draft
asks for confirmation before discarding it. Existing invitations remain saved
when switching to public access.

Production shows deployment and publication readiness, including pending and
error states. Preview can show access as unconfirmed or pending; while a Preview
change is applying, the dialog says the link retains its previous visibility.
If viewer membership cannot refresh, existing viewers remain visible with a
retry action. The links have copy controls with manual-copy recovery. Production
access can also be disabled. See the [copy control](../components/copy-control.md)
and [confirmation](../components/confirm-dialog.md) contracts for shared behavior.

The [local product fixture](http://127.0.0.1:5197/app-studio-sharing.html)
mounts the dialog with simulated production and Preview audiences, viewer
refresh failure, and pending Preview access. Start it using the
[design review setup](../../../hack/design-review/README.md).

This is a source-backed description of the dialog, not evidence of live access
enforcement. The full sharing lifecycle has not been browser-audited here.
In the synthetic fixture, Chromium checks at 1440px light and 390px dark covered
opening Preview access and returning focus to its Manage button on Cancel, with
no page overflow. They do not verify live grants or assistive technology.
The fixture also kept production access disabled through a simulated viewer
refresh failure and retry at both widths; no access change was sent to a backend.
