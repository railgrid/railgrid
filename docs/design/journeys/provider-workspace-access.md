---
{"schema":1,"id":"design.journeys.provider-workspace-access","title":"Provider access in a workspace","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Describes the host Catalog's workspace binding and access review; provider installation, backend authorization and reconciliation are outside this entry."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/provider-workspace-access.md#provider-access-in-a-workspace","role":"design"},{"path":"portal/src/pages/ProvidersPage.vue","role":"implementation"},{"path":"portal/src/components/ProviderEnableDialog.vue","role":"implementation"},{"path":"portal/src/composables/useProviderEnableFlow.ts","role":"implementation"},{"path":"portal/src/stores/providers.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of workspace Provider access","status":"passing","evidence":"Reviewed the Catalog actions, consent dialog and scoped operation handling. This does not establish live authorization or backend reconciliation."},{"kind":"browser","ref":"Workspace Provider access journey","status":"pending"}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"related"},{"id":"design.journeys.organization-and-workspace-entry","relation":"related"},{"id":"design.components.confirm-dialog","relation":"see-also"}]}
---

# Provider access in a workspace

The Catalog lets a user enable a Provider in the current workspace. Required
Provider dependencies must be enabled first. Installation of an organization's
self-hosted copy is a separate journey; readiness does not enable it in every
workspace. See [resource creation](../patterns/resource-creation.md) for that
installation flow and onboarding.

The enable dialog names the Provider and separates workspace tools and resources,
access to other Providers, and Railgrid capabilities. It shows resource
coordinates, verbs or label restrictions where supplied, and warns that
declining may limit features. Platform resource requests begin selected.
Composition and hub requests begin selected only when the caller can accept them;
otherwise their controls explain which administrator can change the request.
A Provider with no additional requests gets an explicit no-access-request state.
Enabling submits the selected choices; a failure remains visible in the open
dialog. Busy controls prevent duplicate submission. The dialog initially focuses
its close button, contains Tab navigation, and restores focus on close when the
origin remains connected. A workspace change invalidates the decision and fences
late results from the new context.

Enabled cards can offer **Review access** when requested capabilities or claims
are pending or rejected. A pending composition explains that the Provider cannot
create the named resources until an administrator accepts it. A narrowed claim
scope asks for disable and re-enable; a stuck disable exposes cleanup information.
The Catalog also distinguishes an unavailable workspace binding from a usable
one. Disabling the platform copy before switching to a self-hosted copy warns
that workspace resources are deleted and must be exported and recreated; it
rechecks the binding after confirmation. These are UI descriptions, not a promise
that every backend operation succeeds or that any data migrates.

The review lens links the actual sources and uses a shell specimen for context;
it does not exercise consent, live workspace bindings, or assistive technology.
