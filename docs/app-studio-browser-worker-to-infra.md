# App Studio direct Playwright MCP browser integration

## Status

App Studio now uses the Infrastructure provider's shared `Browser` instance,
which runs the upstream Playwright MCP server. The browser image is pinned by
the verified architecture-neutral OCI image index digest recorded from the
Playwright MCP registry reference:
`mcr.microsoft.com/playwright/mcp@sha256:18c0a9c934004fe9580cc79f1e8e6e6cde7c667348b215335e8a23fd3e509804`.
The instance is provisioned once per workspace by the Studio reconciler and
reached through the Infrastructure data-plane proxy.

The model receives approved upstream `browser_*` tools directly. App Studio no
longer retains a model-facing `inspect_development_preview` or
`interact_development_preview` wrapper contract. Historical compatibility code
may still decode old event records, but those wrapper names are not advertised
as new callable capabilities.

This is a direct-native exposure inspired by Codex's browser-tool model. The
upstream Playwright server owns browser mechanics and native tool schemas;
App Studio owns the safety, tenancy, evidence, and lifecycle boundary around
those calls.

## Model-facing capability boundary

At turn setup, App Studio calls MCP `tools/list`, validates the required
capabilities, and passes through the approved upstream descriptions and JSON
schemas. The allowlist includes navigation, snapshots, console and network
observation, screenshots, waits, and bounded interactions such as click, drag,
fill, dialog handling, hover, key press, resize, option selection, and type.
`browser_tabs` is retained as an App Studio-only safety tool. Arbitrary-code
tools such as `browser_evaluate` or `browser_run_code` are not exposed.

The required catalog includes `browser_navigate`, `browser_snapshot`,
`browser_console_messages`, `browser_click`, `browser_tabs`, and either
`browser_type` or `browser_fill_form`. If discovery cannot prove that catalog,
App Studio fails closed instead of exposing a partial browser surface.

Successful catalogs are cached for the shared browser reference. A discovery
request or legacy inspection cannot displace an active managed session; the
single-replica browser is serialized per data-plane reference while distinct
owners receive distinct MCP sessions.

## Streamable-HTTP lifecycle

App Studio implements the upstream streamable-HTTP lifecycle at the
Infrastructure data-plane proxy:

1. `POST initialize` negotiates the MCP protocol and captures
   `Mcp-Session-Id`.
2. `POST notifications/initialized` completes initialization.
3. A persistent `GET` event stream is opened with `Mcp-Session-Id` and
   `MCP-Protocol-Version`.
4. `POST` requests carry `tools/list` and `tools/call` messages, echoing the
   session and protocol headers after negotiation.
5. `DELETE` closes the session while the GET stream is still alive; App Studio
   then cancels and drains the stream.

The GET stream drains server events and answers heartbeat `ping` requests with
JSON-RPC responses. Keeping this stream open is the critical fix for the prior
roughly five-second `Session not found` failures: the Playwright MCP server's
heartbeat must have a live event channel.

## App Studio-owned safety and evidence

Each managed session is owned by the caller identity, organization/workspace,
project, and assistant run. App Studio also holds the shared-browser lock and
closes the session at run termination, server shutdown, or idle expiry.

For a private preview, App Studio validates the preview's authorization
redirect, mints a short-lived one-use handoff through the hub, and navigates the
browser to redeem it. The caller's bearer token is not exposed to Chromium.
Before a browser call, App Studio requires the current source mutation to have
completed the development-sync fence and requires the preview to be ready.
Model navigation is constrained to the server-resolved preview origin.

After each successful native call, App Studio performs a server-owned safety
observation: a fresh `browser_snapshot` (unless the call itself was a snapshot)
and an internal `browser_tabs` listing. Reported URLs and tab URLs must remain
on the preview origin. These safety receipts are not added to model-visible
evidence; the model receives the original native MCP receipt, and the evidence
reducer derives bounded verification from that receipt. There is no bespoke
server-side assertion envelope.

If an MCP session is lost, App Studio never replays a mutating interaction. It
returns an explicit `outcome_unknown` result with `replayed: false`. A safe
read may be reconstructed once on a fresh session, but only when no prior
interaction is pending; otherwise App Studio returns an unverifiable result
and requires a new successful snapshot. Read reconstruction is therefore not
used to claim that an uncertain interaction happened.

## Historical migration context

The retired App Studio browser worker exposed `POST /v1/inspect` from an
App-Studio-owned image and evaluated assertions inside that bespoke service.
That design was replaced by the shared Infrastructure `Browser` instance and
the standard Playwright MCP toolset. The old aggregate inspection model was not
preserved as a new wrapper: direct native receipts are the browser evidence,
while App Studio's origin, session, sync, private-preview, and no-replay rules
remain the product-specific boundary.

## Verification

The completed flow was live-verified in local Tilt against `todo-theme-app`:

- navigate succeeded;
- the first snapshot showed 25% and 3 tasks;
- a click succeeded;
- a fresh snapshot showed 50% and 2 tasks; and
- the interaction result was `interactions_verified`.
