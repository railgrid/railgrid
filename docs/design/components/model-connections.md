---
{"schema":1,"id":"design.components.model-connections","title":"Model connections","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Shared cards, selector, form, and usage-section composition are used by Agents and App Studio. Aggregate usage is available in Agents only. Presentation is canonical in optional AgentKit; APIs and validation stay provider-owned."},"appliesTo":["provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/model-connections.md#model-connections","role":"design"},{"path":"provider-sdk/agentkit-vue/ModelConnectionCard.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelConnectionForm.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelIDSelector.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/modelIDSelection.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelUsageSection.vue","role":"implementation"},{"path":"provider-sdk/agentkit/agent-ui.css","role":"implementation"},{"path":"providers/agents/portal/src/views/ModelConnectionEditor.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/ModelsSettings.vue","role":"implementation"},{"path":"hack/models-form-visual-regression.mjs","role":"reference"},{"path":"hack/models-form-visual/README.md","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make test-model-connections","status":"passing","evidence":"Provider UI checks cover required verification, discovery without saving, draft retention, key replacement and stale authority results."},{"kind":"command","ref":"make test-model-connections-api","status":"passing","evidence":"Both provider API packages and Agents model tests pass; stored-key reuse rejects changed endpoints."},{"kind":"browser","ref":"Models custom-element root fixture","status":"passing","evidence":"Final8 exact pixel comparison passes with zero differing pixels across all 8 OpenAI/custom \u00d7 light/dark \u00d7 1440px/390px pairs, using real host fonts and unmasked full-create-page screenshots. Evidence: /var/tmp/codex-build/models-heavy-render/form-compare-final8/models-form-visual-regression.json. Fixture services are mocked; no live credential validation was performed."},{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"2026-09-09: final optional AgentKit parity, independent style markers, canonical import direction and conversation conformance passed. Evidence: /tmp/agentkit-final-gates.log."}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"implements"},{"id":"design.components.status-badge","relation":"see-also"},{"id":"design.components.form-select","relation":"see-also"},{"id":"design.accessibility.interaction","relation":"see-also"}]}
---

# Model connections

## Purpose

Provide one recognizable pattern for connecting and maintaining workspace model
credentials while retaining each provider's assignment semantics.

## Use when

Use `ModelConnectionCard` for a saved connection and `ModelIDSelector` for
selecting a discovered or manually entered model. Use `ModelUsageSection` below
the collection to separate connection management from reporting.

## Avoid when

Do not imply that a stored credential proves a working model. Do not treat a
successful model-list request as a successful model response. Do not substitute
zero cost for missing pricing or unavailable reporting.

## Anatomy and variants

Cards show identity, endpoint, credential state, test state, provider-owned
metadata and actions. App Studio supplies its default designation; Agents
supplies primary and fallback assignments. The default slot carries pricing or
capabilities, and the actions slot carries caller-owned mutations.

`ModelConnectionForm` is the canonical shared form for the Name, Connection,
Credential, and Model controls, discovery, test feedback, and Cancel → test →
Connect actions. The Agents and App Studio adapters provide state, validation,
API event handlers, and provider-specific slots; Google credential-method and
service-account JSON controls, plus App Studio recommendations, remain adapter
owned.
The provider control uses a native `<select>`; this is a sanctioned operating-system
popup under [FormSelect guidance](form-select.md), while model IDs retain
the shared `ModelIDSelector` behavior.

## Behavior

Connect and edit use a focused form. Discovery changes the draft selection only.
A new or edited connection must pass a model-response test before the UI saves
it. Changing the endpoint, credential or model invalidates that verification.
An empty new draft starts with OpenAI and no model selection; choosing the
Custom OpenAI-compatible provider and endpoint is explicit. Provider selection
updates the draft only and does not alter saved models or their default
designation.
Saved credentials can be reused by probes only against their original provider
and endpoint. Stored keys are never returned to the browser. These UI gates do
not change the providers' API upsert contracts or share credentials between
providers. Test results are session-local, not persisted health guarantees.

## Content

Use “Connect model,” “Edit,” “Test connection,” and “Find models.” State input
and output prices separately as USD per million tokens and label them catalog
estimates. Both providers use `provider-sdk/modelcatalog`; its existing rates
are a reference snapshot, not live pricing. App Studio does not yet have
aggregate usage reporting. Agents preserves window selection, cost, tokens,
runs, errors, latency, daily spend and model/agent breakdowns.

## Layout and responsive behavior

Models routes use an unboxed page header with the title and Connect model action
on one wrapping row, followed by supporting copy. Do not wrap the collection in
a settings-dialog frame or place the primary action in a separate toolbar.
Keep the Models title, supporting copy, and Connect model action at the canvas
level while existing saved cards remain in the collection.
Match Databricks collection spacing: the host owns the outer inset, with no
additional page padding and 16px between content groups.

Use `.k-model-grid` for compact cards, constrained to 280–360px where space
permits and a single column on narrower surfaces. Route-owned create forms use
`.k-create-page` as the root (`display: block`, `width: 100%`, `min-width: 0`),
with explicit 14px font size and 21px line height, one back action, one heading,
and one form surface. The explicit route typography prevents inherited inline
link offsets. Agents Models removes
its former `agents-menu` and `agents-create-page` wrappers so its create route
matches App Studio's wrapper geometry. The `.k-back-action` arrow is a fixed
14px by 14px flex-none SVG. Focused forms use the shared wide creation surface,
with the name followed by Connection, Credential, and Model sections. Use the
`.k-model-form-*` recipes for identical section geometry and responsive columns
across both providers; keep field help inside the form surface. Fields top-align
their grid contents so adjacent controls stay aligned when only one has help text.
The canonical stylesheet version 12 also owns the host font-feature settings
and the `.k-create-title:focus-visible` title-focus recipe.

Usage summaries appear below the collection. `ModelUsageSection` is one bordered,
raised section card with 20px padding and a 6px radius; its title, time controls,
metrics, and expandable detail content stay inside that boundary. App Studio's
usage-unavailable message remains inside the same card without a nested box.
Shared recipes ship through `make sync-portalkit` and stylesheet version 12,
including compatibility fallback for older host stylesheets.

## Accessibility

The shared selector retains keyboard search, arrow navigation, manual IDs,
disabled unsuitable models and focus restoration. Label credential and test
states independently. Keep error text visible with alert semantics and test
feedback announced as status. Preserve the route's heading focus target.

## Code and evidence

Run `make test-model-connections`, `make test-model-connections-api`, and the
three design gates. The repository fixture and
`hack/models-form-visual-regression.mjs` comparator mount both providers on
both Models and create routes at 1440px and 390px in light and dark themes,
loading the real host fonts and capturing empty OpenAI and explicitly selected
Custom drafts. The final8 comparison passes with zero differing pixels across
all 8 pairs (OpenAI/custom × light/dark × 1440px/390px), using unmasked
full-create-page screenshots. Evidence is recorded at
`/var/tmp/codex-build/models-heavy-render/form-compare-final8/models-form-visual-regression.json`.
The fixture services are mocked; no live credential validation was performed.
App Studio loads Models settings on demand; its bootstrap keeps an independent
Vite preload helper so it stays a repeatable classic script. The existing build
checks enforce that contract and both page and total budgets.

## Related guidance

See [resource creation](../patterns/resource-creation.md) and
[interaction accessibility](../accessibility/interaction.md).
