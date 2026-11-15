---
{"schema":1,"id":"design.journeys.edge-service-setup","title":"Edge service setup","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"partial","notes":"Describes the Edges portal flow; agent reachability and controller behavior are outside this entry."},"appliesTo":["provider-portals"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/edge-service-setup.md#edge-service-setup","role":"design"},{"path":"providers/edges/portal/src/Services.vue","role":"implementation"},{"path":"providers/edges/portal/src/ServiceCreate.vue","role":"implementation"},{"path":"providers/edges/portal/src/ServiceEdit.vue","role":"implementation"},{"path":"providers/edges/portal/src/serviceValidation.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of Edge service setup","status":"passing","evidence":"Portal source review; does not establish live reachability, credential handling, or assistive-technology behavior."},{"kind":"browser","ref":"Live Edge service lifecycle","status":"pending"}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"related"},{"id":"design.patterns.resource-reads","relation":"related"},{"id":"design.journeys.mcp-access","relation":"related"},{"id":"design.components.create-guidance","relation":"related"}]}
---

# Edge service setup

An Edge service declares an endpoint reachable from an edge and can expose its
catalogued tools through the Edges MCP endpoint. The collection's first-run
path directs a workspace without edges to connect one first. Creation loads the
edge list and service catalog; a failed read offers a retry, while an empty
edge list offers the connect-edge path.

The user chooses an edge, service type, scheme, port, and target. Host edges
use a host or IP target; Kubernetes edges can also use a Kubernetes Service,
unless the selected type requires a host. A blank host means agent loopback
when the type permits it. A pasted root URL can supply the scheme and port;
the form validates the host and port and previews the resulting target before
creation. Catalog defaults and restrictions depend on the service type. The
[creation pattern](../patterns/resource-creation.md) describes the shared form
composition.

The collection and detail page use the same target label: host-edge loopback
is named explicitly, and a Kubernetes or unknown edge with no target is shown
as not specified. The [target fixture](http://127.0.0.1:5199/product.html?scenario=targets)
shows synthetic loopback, LAN, and Kubernetes examples without contacting a hub.

Credentials are a separate step on the service detail page. The catalog
specifies whether credentials are required, optional, or unsupported and which
fields to show. Saving writes the supplied value to a workspace Secret; the
existing value is never displayed. A configured credential is not itself proof
of reachability: the detail page separately shows the service phase, controller
observations, endpoint, and catalogued AI tools. A proxy-only service may have
no AI tools. See [MCP access](mcp-access.md) for accessing exposed tools.

Configuration edits and credential submission have separate actions. Refreshing
observations preserves a dirty configuration draft; a confirmed missing service
clears the old snapshot. Deleting requires confirmation that its MCP tools stop
being exposed. A failed mutation remains visible for recovery; the shared
[read contract](../patterns/resource-reads.md) covers retained snapshots.

The [service creation fixture](http://127.0.0.1:5199/product.html?view=create)
and [service detail fixture](http://127.0.0.1:5199/service-detail.html) use
synthetic data. They provide review context, not evidence of live agent
reachability, Secret storage, controller verification, or accessibility. The
complete lifecycle has not been browser-audited against a live provider here.
