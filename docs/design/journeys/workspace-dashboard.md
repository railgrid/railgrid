---
{"schema":1,"id":"design.journeys.workspace-dashboard","title":"Workspace dashboard","kind":"journey","status":"active","authority":{"design":"informative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"The host dashboard composes workspace provider tiles and a customizable layout; individual tile content belongs to providers."},"appliesTo":["portal"],"owner":"design-system","canonicalSource":[{"path":"docs/design/journeys/workspace-dashboard.md#workspace-dashboard","role":"design"},{"path":"portal/src/pages/DashboardPage.vue","role":"implementation"},{"path":"portal/src/components/DashboardTile.vue","role":"implementation"},{"path":"portal/src/stores/dashboardLayout.ts","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"review","ref":"Source review of workspace dashboard","status":"passing","evidence":"Reviewed host composition, scoped read states, and layout controls. This does not establish live persistence, drag accessibility, or provider backend behavior."},{"kind":"browser","ref":"Dashboard customization and persistence","status":"pending"}]},"relatedDocuments":[{"id":"design.components.dashboard-usage","relation":"related"},{"id":"design.patterns.fluid-shell","relation":"related"},{"id":"design.patterns.resource-reads","relation":"related"},{"id":"design.journeys.provider-workspace-access","relation":"related"}]}
---

# Workspace dashboard

The dashboard summarizes the active workspace. It selects enabled navigation
providers with a UI from the workspace's provider state, and gives each a card.
A provider can supply its own summary tile; otherwise the card remains a
launcher. The separate plan usage tile follows the provider grid.

The catalog and workspace bindings must settle for the current scope before an
empty provider state is presented. Loading, missing workspace, and failure have
separate messages; failure offers Retry. When no provider is ready, the message
distinguishes an existing binding from no enabled providers and points to the
catalog. The Getting started guide can be reopened from the dashboard.

**Railgrid Agents** opens the workspace chat pane on send. On other pages, the floating Agents button opens that pane directly. A personal
assistant selection or Off takes precedence over a provisioned workspace
default. The choice and page-context setting are saved to the signed-in
account for that workspace. The draft is scoped to the current workspace and stays editable
until Agents accepts it. The Getting started guide remains available from
the dashboard.

Customize enables moving, resizing, and removing tiles. Add tile restores hidden
candidates, Reset clears customization, and Done returns to browsing. An empty
customized layout offers a route back to Add tile. The grid adapts to its
content width; reading order outside customization follows tile position. Layout
is scoped to the workspace, with a local optimistic cache and a hub copy. The
live eligible provider set remains authoritative over a saved layout.

This is a source account, not proof of live persistence or accessible drag
operation. The [dashboard tile contract](../components/dashboard-usage.md)
describes the provider-owned summary and recovery states. The [review lens](http://127.0.0.1:5197/?entry=design.journeys.workspace-dashboard)
links the host sources and uses a shell specimen for context, not an interactive
dashboard; see the [review setup](../../../hack/design-review/README.md).
