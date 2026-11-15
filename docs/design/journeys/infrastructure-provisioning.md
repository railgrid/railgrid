---
{"schema":1,"id":"design.journeys.infrastructure-provisioning","title":"Infrastructure provisioning","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Source-backed portal journey; live provisioning and accessibility have not been audited here."},"appliesTo":["provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/infrastructure-provisioning.md#infrastructure-provisioning","role":"design"},{"path":"providers/infrastructure/portal/src/views/CatalogPage.vue","role":"implementation"},{"path":"providers/infrastructure/portal/src/views/ProvisionPage.vue","role":"implementation"},{"path":"providers/infrastructure/portal/src/views/MissingCredentialsPage.vue","role":"implementation"},{"path":"providers/infrastructure/portal/src/views/InstanceListPage.vue","role":"implementation"},{"path":"providers/infrastructure/portal/src/views/InstanceDetailPage.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Infrastructure portal source review","status":"passing","evidence":"Reviewed catalog, provisioning, missing-credentials, instance list and detail sources. This does not establish live provisioning or assistive-technology behavior."},{"kind":"browser","ref":"Complete live Infrastructure provisioning lifecycle","status":"pending"}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"related"},{"id":"design.patterns.resource-reads","relation":"related"},{"id":"design.components.conditions-panel","relation":"related"},{"id":"design.components.confirm-dialog","relation":"related"}]}
---

# Infrastructure provisioning

In the active workspace, **Templates** presents the catalog in cards or a list;
a template opens its provisioning form. The form asks for an instance name and
renders the template's input schema. It validates the name and inputs before
creating the instance; submission starts provisioning and opens the instance.
A failed template read offers retry. A missing provider binding or workspace is
explained in the form; a missing cloud credential opens a separate page with
setup guidance and a route back to templates.
The form seeds sample values on its first template read; a retry or readiness
recheck preserves edits already entered. The instance name is trimmed and must
use lowercase letters, numbers, hyphens, and periods, with a letter or number
at each end. Other submission errors remain in the form.

**My instances** lists provisioned instances and links to their detail. The empty
state offers a route back to the catalog. Detail shows status, values, controller
conditions and reported child resources when available. Reads can refresh, and
failed refreshes can retain the last successful result with a warning and retry.
Deleting an instance from the list or detail requires confirmation; the dialog
requires typing the instance name and explains that provisioned resources are
permanently deleted; the detail dialog also names the bridged credentials
Secret. A successful request reports that deletion was requested, rather than
that cleanup has finished. The list refreshes after the request; the detail
returns to the list. Deletion errors remain visible for recovery.

The synthetic [catalog](http://127.0.0.1:5201/catalog.html),
[instances](http://127.0.0.1:5201/instances.html), and
[instance detail](http://127.0.0.1:5201/instance.html) fixtures mount product
views with sample data. The catalog fixture also has a simulated
[provisioning read failure](http://127.0.0.1:5201/catalog.html?scenario=provision-error).
Start them using the [design review setup](../../../hack/design-review/README.md).
The separate [deployment detail example](../patterns/resource-reads.md#deployment-detail-example)
is a hypothetical composition study, not an Infrastructure product screen.
Neither fixture data nor this source review verifies live provisioning,
credential setup, deletion, or assistive technology.
