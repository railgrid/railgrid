---
{"schema":1,"id":"design.byo-organization-trust","title":"Trusted provider UI and org-bound backends","kind":"policy","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"partial","notes":"UI explicitly trusted as portal code; backend org-bound relay retained; hosted enrollment remains quota-blocked."},"appliesTo":["portal","provider-portals"],"owner":"platform","canonicalSource":[{"path":"docs/design/byo-organization-trust.md","role":"design"},{"path":"portal/src/pages/ProviderFrame.vue","role":"implementation"},{"path":"pkg/hub/restapi/org_backend.go","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"docs/reviews/byo-mvp.md","status":"pending","evidence":"Earlier isolated-origin evidence is historical; trusted UI deployment verification is recorded separately."}]},"relatedDocuments":[]}
---

# Trusted provider UI and org-bound backends

The user explicitly chose trusted provider JavaScript in the portal document.
Org UI bundles use authenticated source-specific grants, SRI and the same custom-
element loader as platform providers. There is no browser isolation boundary: UI
code can access portal DOM/storage/token and exercise every organization or
platform privilege of the signed-in user. Do not claim containment of hostile
provider UI, or treat a context without a token field as isolation.

Dedicated origins, provider iframes, bootstrap sessions, cookies, renewal loops
and bridge transports are removed. Ordinary portal authentication and browser
APIs apply. Approvals are checked before loading; already executed JavaScript
cannot be revoked from the current document and requires a full portal reload.

The restricted native runtime and opaque backend credential remain independently
org-bound. Server checks preserve live membership, source and binding identity,
approval revision, current caller RBAC, APIExport claims, native identity fences
and shared-infrastructure controls. These guarantees apply to those credentials,
not to a trusted UI holding the user's general portal token.

The existing Self-Hosting UI and SDK interfaces remain. No provider DNS/TLS setup
is needed. Historical dedicated-origin tests describe the former architecture;
current acceptance must exercise trusted portal loading on the user's actual URL.
See [installation guidance](../byo-providers.md) and
[implementation evidence](../reviews/byo-mvp.md).
