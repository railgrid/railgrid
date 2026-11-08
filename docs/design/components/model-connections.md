---
{"schema":1,"id":"design.components.model-connections","title":"Model connections","kind":"component","status":"active","authority":{"design":"normative","implementation":"canonical"},"implementation":{"state":"shipped","notes":"Agents and App Studio use provider-first setup with reusable provider connections and separate model bindings. Agents shows both tables on Setup’s Models page and reports usage in Activity. App Studio keeps its collections on separate tabs. Shared presentation is canonical in AgentKit and PortalKit, while credentials, revision pinning, APIs and validation stay provider-owned."},"appliesTo":["provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/components/model-connections.md#model-connections","role":"design"},{"path":"provider-sdk/agentkit-vue/ModelConnectionCard.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelConnectionForm.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelIDSelector.vue","role":"implementation"},{"path":"provider-sdk/agentkit-vue/modelIDSelection.ts","role":"implementation"},{"path":"provider-sdk/agentkit-vue/ModelUsageSection.vue","role":"implementation"},{"path":"provider-sdk/agentkit/agent-ui.css","role":"implementation"},{"path":"providers/agents/portal/src/views/ModelConnectionEditor.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/ModelsSettings.vue","role":"implementation"},{"path":"hack/models-form-visual-regression.mjs","role":"reference"},{"path":"hack/models-form-visual/README.md","role":"reference"},{"path":"provider-sdk/agentkit-vue/AIModelReasoningPicker.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/App.vue","role":"implementation"},{"path":"providers/app-studio/portal/src/llmRegistryRead.ts","role":"implementation"},{"path":"providers/app-studio/portal/src/llmRegistry.ts","role":"implementation"},{"path":"providers/app-studio/api/dataplane_table.go","role":"implementation"},{"path":"providers/app-studio/api/llm_provider_connections.go","role":"implementation"},{"path":"providers/app-studio/apis/ai/v1alpha1/types_modelproviderconnection.go","role":"implementation"},{"path":"providers/app-studio/controller/modelproviderconnection/controller.go","role":"implementation"},{"path":"providers/app-studio/manifest.yaml","role":"implementation"},{"path":"docs/app-studio-model-provider-connections.md","role":"reference"},{"path":"provider-sdk/agentkit-vue/ModelCollection.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ActionMenu.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/Models.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/ModelDetails.vue","role":"implementation"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make test-model-connections","status":"passing","evidence":"Provider UI checks cover required verification, discovery without saving, draft retention, key replacement and stale authority results."},{"kind":"command","ref":"make test-model-connections-api","status":"passing","evidence":"Both provider API packages and Agents model tests pass; stored-key reuse rejects changed endpoints."},{"kind":"browser","ref":"2026-10-08 Models custom-element fixture","status":"passing","evidence":"All 16 real-source OpenAI/custom × light/dark × 1440px/390px captures pass shared geometry, typography, focus, target-size, labelled-field, overflow, font and runtime checks. Both providers clear typed keys on provider and endpoint changes with zero writes. Raw unmasked PNG differences remain observational because adapter guidance differs. See hack/models-form-visual/README.md for reproduction instructions. Fixture services are mocked; no live credential validation was performed."},{"kind":"command","ref":"make verify-portalkit","status":"passing","evidence":"2026-09-09: final optional AgentKit parity, independent style markers, canonical import direction and conversation conformance passed. Evidence: /tmp/agentkit-final-gates.log."},{"kind":"browser","ref":"2026-10-05 live Tilt model verification","status":"passing","evidence":"Actual Tilt UI+kcp verified a new candidate before the named connection existed, rejected a replacement key without enabling Save, and successfully retested the original stored credential. All three task-owned probe pairs and the final fixture connection/Secret were removed. The upstream was a loopback OpenAI-compatible fixture, not a paid or external model. This check proves ordinary success/failure cleanup; abandonment is covered by the separate deadline lifecycle check. Evidence: .kcp/agents-design-conformance/root-pass1/model-result.json."},{"kind":"browser","ref":"2026-10-05 live Tilt abandoned-probe cleanup","status":"passing","evidence":"The actual UI created a temporary candidate and called a loopback fake model. With the successful response held, closing the tab left the pair intact; the controller removed both at the shortened 25-second fixture deadline while a saved connection retained both UIDs. A Secret created after its expired owner was removed was garbage-collected by real kcp. All task fixtures were removed. Server maximum/annotation clamping, restart, ownership, deletion races and tenant isolation are covered by make test-agents-credential-cleanup (race-enabled unit tests with separate fake tenant clients). Evidence: .kcp/agents-design-conformance/cleanup-pass1/abandoned-model-probe-result.json."},{"kind":"command","ref":"Combined model and reasoning picker portal validation","status":"passing","evidence":"Full App Studio npm test and vue-tsc --noEmit passed, including mounted slider commit, per-model restoration, keyboard navigation, focus, saving and recovery regressions. Final production build and budget check passed: page 1,213,197 B raw / 330,285 B gzip; total 1,320,976 B raw / 364,568 B gzip. PortalKit parity, provider contract and UI conformance gates passed."},{"kind":"browser","ref":"Combined model and reasoning picker live browser validation","status":"passing","evidence":"2026-10-17: Actual isolated Tilt hub and App Studio backend passed Chromium checks. Landing verified model-default inheritance, per-model restoration, unsupported endpoint gating, nested Escape and focus, internal Tab order, and keyboard model selection. Project chat verified pointer preview with exactly one preference write on release, server persistence and reload restoration, and light/dark popup bounds at 1440x1000 and 390x844 with no horizontal document overflow or page errors. Placeholder credentials were used without upstream model calls. Screenshots: artifacts/screenshots/app-studio-picker-effort-light.png, app-studio-picker-models-dark.png, and app-studio-picker-narrow-dark.png. Execution log: /tmp/app-studio-picker-browser-check.log."},{"kind":"command","ref":"App Studio reasoning code-review verification","status":"passing","evidence":"2026-10-17: make test-app-studio-reasoning passed the catalog, full API/store/Studio-controller suites, portal typecheck and complete portal runner. Pinned fix-lint and lint targets passed after review fixes. The malformed preference PATCH regression passed after the final presence-validation change. Full store tests also passed against task-owned Postgres with unique schemas, including the deterministic turn/preference event-ordering regression. Regenerating 200 App Studio schema/catalog artifacts produced identical hashes. Boilerplate, provider contract, PortalKit parity, design-doc and UI-conformance gates passed; UI conformance scanned 463 files with zero violations. Full repository make verify was not run."},{"kind":"browser","ref":"App Studio reasoning coarse-pointer review verification","status":"passing","evidence":"2026-10-17: Actual Tilt hub and App Studio backend passed Chromium checks at 390x844 with mobile/coarse-pointer emulation in light and dark themes. Reset, model and back targets measured at least 44x44 pixels; touch navigation and popup bounds passed without horizontal document overflow or page errors. Placeholder model credentials were used without upstream calls. Screenshots: artifacts/screenshots/app-studio-picker-review-touch-light.png and artifacts/screenshots/app-studio-picker-review-touch-dark.png. Execution log: /tmp/app-studio-reasoning-review-browser-touch.log."},{"kind":"command","ref":"App Studio reasoning second-review verification","status":"passing","evidence":"2026-10-17: The catalog and complete API/store/Studio-controller legs of make test-app-studio-reasoning passed. After replacing Object.hasOwn for the configured JavaScript target, portal typecheck and make test-app-studio-portal passed. Regression coverage verifies an already-recorded legacy thread migration on isolated Postgres, repeated startup and reopen, invalid saved-default repair with stored or replacement credentials, and scoped first-thread preference snapshots and retry copies. Pinned formatting/lint and the final portal/provider build passed; the Go build used GOFLAGS=-buildvcs=false after a managed-worktree VCS-stamping error. Final bundle: page 1,214,179 B raw / 330,615 B gzip; total 1,321,958 B raw / 364,901 B gzip, within unchanged budgets. All 200 generated artifacts still match the previously verified hashes. PortalKit, provider-contract, UI-conformance and boilerplate gates passed. No markup or style changes were made in this pass; browser evidence remains the earlier live checks. Full repository make verify was not run."},{"kind":"command","ref":"App Studio reasoning third-review verification","status":"passing","evidence":"2026-10-17: Independent runtime and persistence reviews found no new confirmed defects; their focused catalog/API/store/controller tests passed. The reasoning wire-payload regression also passed with a local test server. The canonical picker now skips its disabled Reset control on opening during a pending save. Its mounted regression reproduces native disabled-control focus behavior, keyboard model selection and absence of an extra effort write; focused picker/persistence/reasoning/resilience/settings tests and portal typecheck passed. PortalKit parity, provider contract and UI conformance passed. The actual Tilt portal/provider build passed with GOFLAGS=-buildvcs=false; bundle page 1,214,190 B raw / 330,623 B gzip, total 1,321,969 B raw / 364,905 B gzip, within unchanged budgets. All 200 generated schema/catalog artifacts still match the previously verified hashes. Full repository make verify was not run."},{"kind":"browser","ref":"App Studio reasoning third-review live keyboard recheck","status":"not-run","evidence":"2026-10-17: Attempted actual Tilt validation of opening the picker during a pending real preference save. App Studio and its production bundle became ready, but make dev-browser-ready timed out while Agents initialization failed with test timeouts and worker startup errors despite a bounded retry. The new browser script never reached login or interaction, so this round has no new live browser pass or screenshot. Earlier live browser evidence remains historical. Logs: /tmp/app-studio-reasoning-third-review-tilt.log and /tmp/app-studio-reasoning-third-review-ready.log. Task-owned stack cleanup uses make dev-browser-down."},{"kind":"browser","ref":"2026-11-03 provider-first cross-provider fixture","status":"passing","evidence":"All 16 actual-entrypoint OpenAI/custom × light/dark × 1440px/390px captures passed provider-first anatomy, route focus, named fields, shared control geometry, touch targets, key clearing and zero draft writes. Unmasked pixel differences remain observational. Report: /tmp/railgrid-model-form-visual/models-form-visual-regression.json. Fixture upstreams are synthetic; live Tilt verification is recorded separately."},{"kind":"browser","ref":"2026-11-03 App Studio provider connection live lifecycle E2E and post-fix viewport check","status":"passing","evidence":"Actual local Tilt hub, kcp, and App Studio backend used a loopback synthetic OpenAI-compatible endpoint; the 21-check report is /tmp/railgrid-appstudio-connections-e2e.json (cleaned=true). It covers detour return to the original Models route, two bindings sharing one connection, model and connection label-only edits without inference, rejected key replacement preserving active config, successful rotation rolling both bindings while retaining archived snapshots, standalone discovery without a model or inference request, friendly default labels, and cleanup without browser Secret reads. Cleanup removed task-owned connections, models, history, and generated keys; the pre-existing Haiku model remained active/default with its original revision. The final post-fix recheck passed with the current worktree main bundle, matching provider-catalog integrity, backend ready at HTTP 200, a 132px Vertex textarea at 390px, no document overflow, and no page errors. Updated screenshot: /home/crwilhit/github/railgrid/artifacts/screenshots/app-studio-google-provider-mobile-live.png; log: /tmp/appstudio-connections-vertex-recheck.log. Other captures are under /home/crwilhit/github/railgrid/artifacts/screenshots/: app-studio-model-bindings-live.png, app-studio-provider-connect-live.png, app-studio-provider-connected-live.png, app-studio-provider-connections-desktop-dark-live.png, app-studio-provider-connections-mobile-live.png, and app-studio-bind-model-mobile-live.png. Vertex service-account JSON was rendered only; no external Google/Vertex request was sent. The workspace had a pre-existing Haiku model; no empty-workspace live scenario was run."},{"kind":"browser","ref":"2026-11-08 live Tilt Agents provider readiness table","status":"passing","evidence":"Agents, Edges, and Infrastructure were healthy in Tilt and their API bindings were active. Chromium verified live provider rows at 1440px and 583px in light and dark themes, including Ready and Needs credential badges with no technical disclosures in the provider table. Fresh captures are in artifacts/screenshots/agents-provider-readiness-{1440,583}-{light,dark}.png in the managed Tilt checkout. The cluster-wide dev-browser-ready command remained blocked because Dashboards requires Databricks, which is absent from this stack. This check used scoped read-only readiness and real tenant data."}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"implements"},{"id":"design.components.status-badge","relation":"see-also"},{"id":"design.components.form-select","relation":"see-also"},{"id":"design.accessibility.interaction","relation":"see-also"}]}
---

# Model connections

## Purpose

Provide one recognizable pattern for connecting and maintaining workspace model
credentials while retaining each provider's assignment semantics.

## Use when

Use `ModelCollection` and `ResourceTable` for saved models and provider access,
and `ModelConnectionCard` for card layouts. Use `ModelIDSelector` for selecting
a discovered or manually entered model. Use `ModelUsageSection` for usage
reporting, which Agents places in Activity.

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

`ModelConnectionForm` supplies the shared full-form composition used by harness
identity and legacy adapters. API provider setup uses the staged provider-first
flow described below, with the same canonical form field and creation recipes.
Verification groups the test action, its request-charge hint, prerequisite help,
and results inside the form body. The footer contains Cancel and Connect actions. The Agents and App Studio adapters provide state, validation,
API event handlers, and provider-specific slots, including a `probe-details` disclosure after the test
summary; Google credential-method and
service-account JSON controls, plus App Studio recommendations, remain adapter
owned.
The provider control uses a native `<select>`; this is a sanctioned operating-system
popup under [FormSelect guidance](form-select.md), while model IDs retain
the shared `ModelIDSelector` behavior.

### Composer model and reasoning picker

`AIModelReasoningPicker` combines model choice with reasoning effort in one
anchored popup. App Studio supplies configured model connections, model-specific
effort options, the saved default label, and persistence state. The component
never infers capabilities or changes a run's settings by itself.

The effort card presents the selected effort, a model-list button, a discrete
slider, and reset. Its first stop inherits the saved model default; the
remaining stops are explicit efforts, including `none` where supported. The
slider previews pointer movement and emits a selection on commit. A model list
with a checkmark and optional workspace-default designation replaces the card
inside the same popup. Selecting a model returns to its own effort state.

The native range control supports Arrow, Home, and End keys and announces its
current effort. The model list supports keyboard navigation; Escape returns to
the card, then closes and restores trigger focus. Popup positioning stays inside
the viewport. Unsupported models retain model choice without an effort slider;
stale selections retain the error and provider-owned recovery actions.

## Behavior

Connect and edit use a focused form. Discovery changes the draft selection only.
A new model binding must pass a model-response test before the UI saves it.
Provider-only setup may verify access through discovery without adding a model;
Vertex service-account setup instead requires an explicit model-response check.
Changing the provider, endpoint, credential mode, workspace, or model invalidates
the corresponding verification.
An empty new draft starts with OpenAI and no model selection; choosing the
Custom OpenAI-compatible provider and endpoint is explicit. Provider selection
updates the draft only and does not alter saved models or their default
designation.
Saved credentials can be reused by probes only against their original provider
configuration and current server fingerprint. Stored keys are never returned
to the browser. These UI gates do
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
App Studio background reconciliation does not issue credential-bearing probes.
A saved check requires current `use` authorization and publishes only a result
whose connection and Secret fingerprint still matches. Unchecked saved accounts
show “Not verified” until such a check completes.

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

Agents uses “Add model,” “Connect provider,” and “Test connection.” App Studio
uses “Add model,” “Connect provider,” and “Refresh models,” and labels the
binding check “Verify model.” State input
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

Models routes use an unboxed page header with the title and provider-appropriate
primary action on one wrapping row, followed by supporting copy. Agents uses a
single Create menu. App Studio uses Add model, or Connect provider when no
connection is saved. Keep the title, supporting copy, and primary action at the
canvas level and use the collection area for tables or cards.
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
with provider and credential controls first, followed by model selection only
when the provider stage has completed. Use the
`.k-model-form-*` recipes for identical section geometry and responsive columns
across both providers; keep field help inside the form surface. Fields top-align
their grid contents so adjacent controls stay aligned when only one has help text.
The canonical PortalKit stylesheet version 43 also owns the host font-feature settings
and the `.k-create-title:focus-visible` title-focus recipe.

Agents places **Usage & cost** in Activity, alongside Run history and Inbox.
`ModelUsageSection` is one bordered,
raised section card with 20px padding and a 6px radius; its title, time controls,
metrics, and expandable detail content stay inside that boundary. App Studio's
usage-unavailable message remains inside the same card without a nested box.
PortalKit recipes ship through `make sync-portalkit` at core stylesheet version
43; optional AgentKit model and reasoning recipes use independent version 36.
Both keep compatibility fallbacks for older host stylesheets.

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
The comparator covers OpenAI and Custom drafts in both providers and the
App Studio Vertex service-account form at desktop and mobile sizes in both
themes. It checks typography, title focus, back targets, field alignment,
touch targets, accessible field names, overflow, fonts, and runtime errors.
Post-capture interactions verify that provider and endpoint changes clear typed
keys without writing resources. The mocked fixture does not prove provider
credential behavior; use the loopback live validation described in
[App Studio model provider connections](../../app-studio-model-provider-connections.md).
See the [fixture instructions](../../../hack/models-form-visual/README.md) to
reproduce the captures and geometry report.
App Studio loads Models settings on demand;
its bootstrap keeps an independent Vite preload helper so it stays a repeatable
classic script. The existing build checks enforce that contract and both page
and total budgets.

## Related guidance

See [resource creation](../patterns/resource-creation.md) and
[interaction accessibility](../accessibility/interaction.md).

### Provider-first BYOK flow in Agents and App Studio

**Agents.** Setup opens to Models. The Models page stacks **Available models**
above **Provider connections & harness identities**. The page-level **Create**
button opens one action menu with **Add model**, **Connect provider**, and
**Add harness identity**. The first-run guide offers a direct **Connect
provider** action. Existing provider links open the combined page and focus
the provider section.

Model rows name their provider connection when its details are readable and
distinguish inaccessible details from a confirmed broken reference. An
expandable row shows the endpoint, catalog capabilities and price estimates,
and primary or fallback agent assignments. The lower table shows the type,
model-binding count where applicable, and readiness badge. Its readiness cells
omit technical disclosures and explain the scope of the harness credential
check where applicable. Both tables use one recovery control for a failed
credential read and retain the last successful results on refresh failure.

**App Studio.** The **Models** tab offers **Add model**, or **Connect provider**
when no connection is saved. App Studio keeps **Provider connections** on a
separate tab. Its searchable list shows the provider, endpoint, active
model-binding count, readiness, and Add model, Edit, and Delete actions.
App Studio retains its Google API-key and Vertex service-account credential
controls, model reasoning defaults, and workspace default designation.

Both portals connect an API provider once, then choose the saved connection
when adding and verifying each model binding. A connection with active model
bindings cannot be deleted.

Agents stores bindings in ModelCredential resources. App Studio keeps logical
model IDs and immutable run revisions in `Studio.spec.llm.models`, with each
active binding naming its provider connection. Existing project selections and
the workspace default keep their IDs during migration. Each legacy account is
promoted independently; matching endpoints are not evidence of shared credentials.
Connection changes must preserve the endpoint and credential snapshot used by
a pinned run revision.

In Agents, a `ModelProviderConnection` owns the endpoint, workspace selection, and Secret
reference. Each configured model has its own `ModelCredential` with
`spec.connectionRef` and `spec.model`. References remain inside the workspace;
model bindings do not own or copy the shared key. Legacy direct API credentials
are migrated while preserving model names and existing Secret data. A server
finalizer retains provider-owned keys while models reference the connection.
See [Agents connection lifecycle and access](../../agents-model-provider-connections.md)
and [App Studio revisions and credentials](../../app-studio-model-provider-connections.md).

Add model starts with saved provider connections and a **Connect provider** link
beneath the selector. That link opens the separate provider setup route; creating
a new provider is not an option embedded in the saved-connection dropdown.

API provider create routes offer only API provider presets. In Agents, **Add
harness identity** opens the dedicated harness login form. The
provider form keeps fixed endpoints under Connection details and displays custom
URLs and Anthropic workspace settings only when applicable. Standalone Connect
provider uses discovery where supported and saves the provider connection
without requiring or adding a model. For Vertex service-account credentials,
setup requires an explicit verification model because automatic discovery is
unavailable. If another provider does not support discovery, an optional verification model
can test inference without adding a binding. Authentication failures retain the
credential draft and focus its field without opening manual verification.
Connection names are editable human labels; when omitted, App Studio assigns a
friendly provider label such as “OpenAI” or “Google AI Studio / Vertex AI” with
an ordinal when needed, while the Kubernetes resource name remains generated and
stable. Renaming a connection changes its display-name annotation only and does
not discover models or send an inference request. Existing model display names
are editable independently of model IDs and provider connections. A model
label-only save changes the model name in the Studio registry, preserves its
stable model ID and active revision, and sends no discovery or inference
request. Those IDs remain the references used by projects and conversations.

After standalone setup, Provider connected offers **Add model** and **Done**.
Setup entered from Add model or chat recovery continues directly to Add model
with the new connection selected, preserving the original return route. The
acknowledged connection is available immediately; background collection refresh
must not hold the success state or require key re-entry. Adding a model verifies
the exact selected model before saving its binding. Editing shared connection
details verifies every active model binding before activating provider changes,
or uses discovery alone when the connection has no model bindings. A label-only
edit skips discovery and inference. A selected verification model is used for
checking only and is never added as a model binding. Request cost is explained in
the connection and model verification forms. The mobile action footer stays in
normal document flow.
