---
{"schema":1,"id":"design.components.provider-agent-context","title":"Provider-published Agents context","kind":"reference","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"partial","notes":"The host contract, immutable normalizer, scoped publisher, overlay preview, and Agents transcript renderer are implemented; provider adoption and branch-specific live host acceptance remain incomplete."},"appliesTo":["provider-portals","portalkit","agents"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/provider-agent-context.md#provider-published-agents-context","role":"design"},{"path":"provider-sdk/portalkit/agent-context.ts","role":"implementation"},{"path":"portal/src/providers/agentContextPublisher.ts","role":"implementation"},{"path":"portal/src/providers/agentOverlayContext.ts","role":"implementation"},{"path":"portal/src/stores/pageResource.ts","role":"implementation"},{"path":"portal/src/pages/ProviderFrame.vue","role":"implementation"},{"path":"portal/src/components/AgentOverlay.vue","role":"implementation"},{"path":"providers/agents/portal/src/pageContext.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/ChatMessage.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"test","ref":"PR2 provider context normalization and host lifecycle tests","status":"passing","evidence":"447 host portal and 813 Agents portal tests passed, including bounded normalization, invalid input clearing, query-only navigation, A-B-A route fencing, identity/access fencing, and async token-refresh owner checks."},{"kind":"browser","ref":"PR2 context preview desktop/mobile light/dark fixture","status":"passing","evidence":"The synthetic-content fixture verified preview fit and scrolling in expanded and compact layouts at desktop and mobile sizes in both themes. It is fixture evidence, not live host/provider integration."},{"kind":"browser","ref":"Branch-built host and Agents portal with live backend","status":"pending","evidence":"No branch-specific live acceptance is claimed."}]},"relatedDocuments":[{"id":"design.ai.agent-overlay","relation":"see-also","path":"docs/design/ai/agent-overlay.md"}]}
---

# Provider-published Agents context

`ProviderAgentContext` is bounded reference data that an enabled provider page
may publish to the shared Agents overlay. It is a presentation and model-context
contract; it grants no permission, chooses no tool, and does not start a request.

## Payload shape

A provider supplies a version-1 object with a non-empty title and optional
summary, resource references, and JSON data. Resource references contain only
API version, kind, name, and optional resource plural, namespace, UID, and
resource version. Arbitrary Kubernetes spec and status objects are not part of
the resource shape.

Normalization accepts only plain objects and arrays with enumerable own data
properties. It never calls provider getters. Functions, symbols, non-finite
numbers, custom prototypes, cycles, sparse arrays, and unknown fields are
rejected. Valid input is copied into a frozen object so a provider cannot mutate
published data after the host accepts it.

The complete normalized JSON is limited to 8 KiB of UTF-8. The normalizer also
limits strings, keys, nesting, node count, object keys, array items, and the
resource list. Invalid, unknown-version, or oversized input clears the previous
snapshot rather than leaving stale context attached to a different page.

## Provenance and lifecycle

The host owns the workspace, provider, path, and provider-route metadata in the
outer message envelope. The provider payload cannot supply or override that
provenance. The host accepts publication only while the publishing provider
mount, principal, workspace, access, and exact route epoch remain current.
Query-only navigation advances the page epoch. A return from route A to B to A
does not revive callbacks created on the first visit. Same-owner token refresh
keeps the transport usable; identity, access, workspace, provider, and mount
changes retire it. The host rechecks ownership after asynchronous token refresh
before sending a provider request.

## Opt-in and presentation

Page context is attached to a user message only when the user has enabled it.
The overlay preview shows the exact serialized envelope and the current enabled
or paused state. The Agents transcript displays saved provenance, summaries,
resource references, and JSON as escaped text under a disclosure. Malformed or
unknown experimental envelopes remain visible verbatim so a compatibility
renderer never silently rewrites user content.

Provider-published strings are untrusted input. Providers must publish only
curated reference data; they must not include credentials, secrets, commands,
query values, logs, or full resource contents. The host bounds and normalizes
the JSON structure but does not inspect its meaning or identify sensitive
values. Agents must inspect live resources with its own configured tools and
current authorization; the context envelope is not evidence of current state.
