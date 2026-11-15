---
{"schema":1,"id":"design.journeys.mcp-access","title":"MCP access and client connection","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The portal implements workspace-scoped MCP server management and client setup; provider discovery and client connectivity depend on the backend and external clients."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/mcp-access.md#mcp-access-and-client-connection","role":"design"},{"path":"portal/src/pages/MCPPage.vue","role":"implementation"},{"path":"portal/src/router/routes.ts","role":"implementation"},{"path":"portal/src/components/AccountAccessMenu.vue","role":"implementation"}],"verification":{"state":"unverified","checks":[{"kind":"review","ref":"Source review of MCPPage and routes","status":"passing","evidence":"Documents the current portal implementation; no live hub, external client, clipboard, browser, or assistive-technology behavior was verified."},{"kind":"browser","ref":"MCP access journey","status":"not-run"}]},"relatedDocuments":[{"id":"design.patterns.resource-reads","relation":"see-also"},{"id":"design.components.resource-page","relation":"related"},{"id":"design.components.resource-table","relation":"related"}]}
---

# MCP access and client connection

MCP Access lets a workspace user manage named endpoints and obtain setup for an AI client. This is a description of the shipped portal journey, not evidence that a particular workspace has an available endpoint or that an external client can connect. Entry is through MCP Access in the account access menu; the collection, create form, and named detail each have workspace-scoped routes.

## Create and find an endpoint

The collection shows server name, display name, status, discovered tool count, and the tool discovery update time. It supports search, status filtering, pagination, and deletion. An empty collection offers creation; loading, read failure, and stale snapshots use the shared resource table states. A user opens a server from its row or name.

New server opens a focused creation route. The form asks for a name and display name, optional instructions, and a read-only choice. Submission requires a nonblank name; while creating, text fields become read-only, the checkbox and submit action remain focusable but unavailable, and cancel is disabled. This preserves the submitted draft and focus while preventing duplicate requests. Errors stay on the form with a dismiss action and the draft becomes editable again. The result is a named workspace endpoint, whose provisioning and tool discovery can continue after creation.

## Connect a client

The server detail uses a resource page with access, provider, and tool summaries. Its connection section loads an endpoint and setup details separately from the server snapshot. It offers Claude Code, Claude Desktop, and Codex snippets. The tabs support arrow keys, Home, and End; the active panel is keyboard focusable. Long snippets scroll horizontally.

The displayed snippet contains a placeholder in place of the token. Copy setup snippet inserts the token into clipboard text only when the token is ready; copy endpoint copies the endpoint separately. The page warns that the token must be kept secret. Token provisioning has a refresh action, connection-detail errors have retry, and clipboard failures suggest allowing clipboard access and trying again. A copied setup snippet contains a credential and must be handled accordingly. The displayed placeholder is not a working credential.

## Inspect and maintain

The provider inventory reflects tools discovered using the endpoint identity. Provider rows expose reachability and expand to show their tools or an error message. Search matches provider and tool names, titles, and descriptions, announces the result summary, and offers a clear action for no matches. Before discovery completes the page reports waiting; an empty discovered inventory and discovery failure have distinct messages. Refresh requests an updated server snapshot. Deletion confirms that the access token will be revoked and preserves the last snapshot while removal is pending; a changed server requires refresh and review before retrying.

Read and connection state are scoped to the selected workspace and server. Changing that identity clears connection details and fences old responses so they cannot populate the new selection. A missing server is reported in its detail view. See [resource reads](../patterns/resource-reads.md) for the shared read-state contract.

## Evidence and limits

The behavior above is grounded in [MCPPage.vue](../../../portal/src/pages/MCPPage.vue), [workspace routes](../../../portal/src/router/routes.ts), and the [account access menu](../../../portal/src/components/AccountAccessMenu.vue). The [synthetic MCP product fixture](http://127.0.0.1:5197/mcp-access.html) mounts the actual page with local sample responses. Use the scenario links for a ready server, pending token, failed connection read, and empty discovery; open the [collection](http://127.0.0.1:5197/mcp-access.html?view=collection) to review the table. Deletion always returns a simulated conflict. The token and endpoint are synthetic and all requests are intercepted; no hub is contacted. When reviewing the actual route, check a ready server and a pending token, failed connection read and clipboard write, empty or unreachable discovery, deletion conflict, and a workspace switch; also check the client tabs by keyboard and the snippet at narrow width. [MCP page conformance tests](../../../portal/src/pages/MCPPage.conformance.test.mjs) inspect selected source-level contracts; they do not prove live connection, clipboard permissions, external client compatibility, rendering, or assistive-technology behavior. This review did not exercise the live route against a hub.

Chromium fixture checks at desktop light and 390px dark covered all four
scenarios, collection-to-detail navigation, and keyboard movement between
client tabs. The snippet scrolls within its container without page overflow.
These checks do not establish clipboard permissions or live connectivity.
With a controlled pending create response, Chromium checks at desktop and
390px also exercised click, Enter, and Space submission: focus and draft were
retained, duplicate submissions and checkbox changes were blocked, and a 503
restored editing. This remains synthetic fixture evidence, not a live hub or
assistive-technology check.
