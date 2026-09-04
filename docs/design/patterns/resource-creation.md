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

Choose the surface for the user's task, not field count, API shape, or
implementation convenience. Use the operation's truthful domain verb, such as
**Connect**, **Provision**, or **Deploy**. Use readable provider-owned routes;
avoid collisions between action routes and valid resource identifiers, preferring
`/create/<resource-type>` when detail routes use `/<collection>/:name`.

A route-owned flow has one back action, one title and description, one principal
form surface, and a right-aligned **Cancel → primary action** footer. Simple
forms are constrained; dense provisioning forms may fill the column. Wizards
keep this skeleton and put progress inside it instead of retaining dialog chrome.

For an authoritative first-use empty collection, use `FirstRunGuide.vue` (or
matching vanilla `k-first-run*` markup). It explains the value, offers the
immediate primary action, and shows the shortest meaningful journey. Missing
prerequisites change the current step and action rather than leaving an inert
empty table.

When domain help is useful, put `CreateGuidance.vue` beside `k-create-fields`
inside `k-create-surface--guided`. The rail contains only timely prerequisites,
a live non-secret summary of what Faros will create, and controller-owned next
steps. Provider copy and value derivation remain local. A shared container query
puts fields before guidance on narrow surfaces and keeps both regions fluid at
desktop and 4K widths.

After creation, navigate to the resource when it owns status or recovery;
otherwise return to the collection with the result clearly visible. This is the
target standard; existing flows adopt it incrementally.
