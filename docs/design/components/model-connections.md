---
{"schema":1,"id":"design.components.model-connections","title":"Model connections","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Shared cards, selector, form, and usage-section composition are used by Agents and App Studio. Aggregate usage is available in Agents only. Presentation is canonical in optional AgentKit; APIs and validation stay provider-owned."},"appliesTo":["provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/model-connections.md#model-connections","role":"design"},{"path":"provider-sdk/agentkit-vue/ModelConnectionCard.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelConnectionForm.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelIDSelector.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/modelIDSelection.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelUsageSection.vue","role":"implementation"},{"path":"provider-sdk/agentkit/agent-ui.css","role":"implementation"},{"path":"providers/agents/portal/src/views/ModelConnectionEditor.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/ModelsSettings.vue","role":"implementation"},{"path":"hack/models-form-visual-regression.mjs","role":"reference"},{"path":"hack/models-form-visual/README.md","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make test-model-connections","status":"passing","evidence":"Provider UI checks cover required verification, discovery without saving, draft retention, key replacement and stale authority results."},{"kind":"command","ref":"make test-model-connections-api","status":"passing","evidence":"Both provider API packages and Agents model tests pass; stored-key reuse rejects changed endpoints."},{"kind":"browser","ref":"2026-10-08 Models custom-element fixture","status":"passing","evidence":"All 16 real-source OpenAI/custom × light/dark × 1440px/390px captures pass shared geometry, typography, focus, target-size, labelled-field, overflow, font and runtime checks. Both providers clear typed keys on provider and endpoint changes with zero writes. Raw unmasked PNG differences remain observational because adapter guidance differs. See hack/models-form-visual/README.md for reproduction instructions. Fixture services are mocked; no live credential validation was performed."},{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"2026-09-09: final optional AgentKit parity, independent style markers, canonical import direction and conversation conformance passed. Evidence: /tmp/agentkit-final-gates.log."},{"kind":"browser","ref":"2026-10-05 live Tilt model verification","status":"passing","evidence":"Actual Tilt UI+kcp verified a new candidate before the named connection existed, rejected a replacement key without enabling Save, and successfully retested the original stored credential. All three task-owned probe pairs and the final fixture connection/Secret were removed. The upstream was a loopback OpenAI-compatible fixture, not a paid or external model. This check proves ordinary success/failure cleanup; abandonment is covered by the separate deadline lifecycle check. Evidence: .kcp/agents-design-conformance/root-pass1/model-result.json."},{"kind":"browser","ref":"2026-10-05 live Tilt abandoned-probe cleanup","status":"passing","evidence":"The actual UI created a temporary candidate and called a loopback fake model. With the successful response held, closing the tab left the pair intact; the controller removed both at the shortened 25-second fixture deadline while a saved connection retained both UIDs. A Secret created after its expired owner was removed was garbage-collected by real kcp. All task fixtures were removed. Server maximum/annotation clamping, restart, ownership, deletion races and tenant isolation are covered by make test-agents-credential-cleanup (race-enabled unit tests with separate fake tenant clients). Evidence: .kcp/agents-design-conformance/cleanup-pass1/abandoned-model-probe-result.json."}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"implements"},{"id":"design.components.status-badge","relation":"see-also"},{"id":"design.components.form-select","relation":"see-also"},{"id":"design.accessibility.interaction","relation":"see-also"}]}
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

Cards use the raised surface token, a subtle hairline, and a 6px radius so each
saved connection has its own visible boundary against the page canvas in both
themes. They show identity, endpoint, credential state, test state, provider-owned
metadata and actions. App Studio supplies its default designation; Agents
supplies primary and fallback assignments. The default slot carries pricing or
capabilities, and the actions slot carries caller-owned mutations.

`ModelConnectionForm` is the canonical shared form for the Name, Connection,
Credential, and Model controls, discovery, test feedback, and Cancel → test →
Connect actions. The Agents and App Studio adapters provide state, validation,
API event handlers, and provider-specific slots, including a `probe-details` disclosure after the test
summary; Google credential-method and
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
providers. Test results are session-local, not persisted health guarantees. Changing the
provider or endpoint clears a typed replacement key; enter the credential for
the new destination explicitly. Verification failures lead with a recovery
summary and keep sanitized, bounded diagnostics in a collapsed technical section.
Agents tests new endpoints and replacement keys with temporary credentials owned
by its controller. The browser removes them after a completed test; an abandoned
test expires within ten minutes of server creation and is cleaned up when the
controller can reach the workspace. This lifecycle does not mutate the named
connection or retain keys in browser draft storage.

## Content

Harness identities use the same form and card geometry with provider-owned
credential controls. Their create and edit headings name the identity rather
than a model connection. On a saved identity, set the card's `resourceLabel` to
“Harness identity” and `endpointLabel` to “Runs on,” with “Edge machine” as the
value. Chat connections retain the default “Model” and “Endpoint” labels.
Keep credential format validation separate from runner readiness: a checked
credential has not yet verified a connection on a machine. Associate validation
errors with the field requiring correction and keep request errors at form level.
AgentKit owns the danger tone of field hints announced as alerts, so inline
validation stays visible in both themes without depending on host utilities.

Use “Connect model,” “Edit,” “Test connection,” and “Find models.” State input
and output prices separately as USD per million tokens and label them catalog
estimates. Both providers use `provider-sdk/modelcatalog`; its existing rates
are a reference snapshot, not live pricing. App Studio does not yet have
aggregate usage reporting. Agents preserves window selection, cost, tokens,
runs, errors, latency, daily spend and model/agent breakdowns. Usage without a
recorded price is **Unknown**, with a count of unpriced runs. When some runs are
priced, the estimate explicitly covers priced usage only. The aggregate latency
is labeled **slowest agent p50**, with p95 scoped the same way; it is the maximum
of the per-agent percentiles, not a workspace-wide percentile. Model breakdowns
name their attribution to the agent’s current model connection. A failed
per-agent read keeps successful rollups visible with an explicit incomplete-data
warning and manual retry. State the 5,000-most-recent-runs per-agent coverage
limit and disclose when a breakdown shows only its top six rows.

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
create routes at 1440px and 390px in light and dark themes, loading the real
host fonts and capturing empty OpenAI and explicitly selected Custom drafts.
The October 8 audit passes all 16 captures and all eight shared geometry pairs.
It checks the documented typography, title focus ring, back arrow, field
alignment, touch targets, accessible field names, overflow, fonts and runtime
errors. Post-capture draft interaction verifies that provider and endpoint
changes clear typed keys without writing resources. Raw unmasked PNG diffs
remain available for inspection; different adapter guidance is not a pixel
identity requirement. See the
[fixture instructions](../../../hack/models-form-visual/README.md) to reproduce
the captures and geometry report.
The fixture services are mocked; no live credential validation was performed.
App Studio loads Models settings on demand; its bootstrap keeps an independent
Vite preload helper so it stays a repeatable classic script. The existing build
checks enforce that contract and both page and total budgets.

## Related guidance

See [resource creation](../patterns/resource-creation.md) and
[interaction accessibility](../accessibility/interaction.md).
