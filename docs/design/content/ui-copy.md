---
{"schema":1,"id":"design.content.ui-copy","title":"UI copy policy","kind":"policy","status":"active","authority":{"design":"normative","implementation":"reference"},"implementation":{"state":"partial","notes":"The policy consolidates shipped copy and state patterns; provider wording remains owned locally and has not received a comprehensive audit."},"appliesTo":["portal","provider-portals","portalkit"],"owner":"design-system","canonicalSource":[{"path":"docs/design/content/ui-copy.md#ui-copy-policy","role":"design"},{"path":"provider-sdk/portalkit-vue/CreateGuidance.vue","role":"implementation"},{"path":"provider-sdk/portalkit-vue/ResourcePage.vue","role":"implementation"},{"path":"provider-sdk/portalkit/dashboardtile.ts","role":"implementation"},{"path":"providers/agents/portal/src/views/Activity.vue","role":"implementation"},{"path":"providers/agents/portal/src/views/DashboardTile.vue","role":"implementation"},{"path":"providers/agents/portal/src/conn-defs.ts","role":"reference"},{"path":"providers/agents/portal/src/assisted-setup.ts","role":"reference"}],"verification":{"state":"partial","checks":[{"kind":"command","ref":"make verify-design-docs","status":"passing","evidence":"The design knowledge-base metadata, IDs, sources, and links validate."},{"kind":"review","ref":"comprehensive provider copy audit","status":"pending"}]},"relatedDocuments":[{"id":"design.patterns.resource-creation","relation":"see-also","path":"docs/design/patterns/resource-creation.md"},{"id":"design.patterns.resource-reads","relation":"see-also","path":"docs/design/patterns/resource-reads.md"},{"id":"design.components.create-guidance","relation":"see-also","path":"docs/design/components/create-guidance.md"},{"id":"design.components.resource-page","relation":"see-also","path":"docs/design/components/resource-page.md"},{"id":"design.components.resource-table-actions","relation":"see-also","path":"docs/design/components/resource-table-actions.md"},{"id":"design.components.first-run-guide","relation":"see-also","path":"docs/design/components/first-run-guide.md"}]}
---

# UI copy policy

Copy is part of the interaction contract: it tells a user what Faros is doing,
what exists, and what can be done next. This policy governs visible labels,
state messages, guidance, and safe technical detail. Component mechanics stay
in the [component entries](../components/); page composition stays in the
[patterns](../patterns/).

## Normative contract

- **Use the domain verb.** Name the outcome the user is choosing: Connect,
  Provision, Register, Deploy, Refresh, Retry, or Remove. Do not hide a
  meaningful operation behind `Submit`, `Run`, or `Continue` when the product
  can name it. The [resource-creation journey](../patterns/resource-creation.md)
  decides whether that verb belongs on a route, dialog, or contextual control.
- **Make actions self-describing.** A visible label says what happens and, when
  needed, which object it affects. Icon-only actions still receive a
  resource-specific accessible label; an in-flight label changes to the
  operation in progress (for example, `Deleting connection`), not merely
  `Working`. Keep technical identifiers as supporting detail, never as the
  only product noun.
- **Tell state apart.** Loading means that an authoritative result is not yet
  available. Empty means a settled read returned no items and should offer the
  shortest useful next step. An initial failure says that the read failed and
  offers `Retry` only when recovery is supported. A background failure keeps a
  useful snapshot visible and says that the last result is being shown. Do not
  turn a missing workspace or provider binding during ordinary bootstrap into a
  red error, and do not hide transport or permission failures as an empty list.
- **Lead with the product meaning.** Put the task, result, or decision first;
  put resource kinds, IDs, controller conditions, schema fields, and raw
  diagnostics in a labeled technical section or disclosure. `ResourcePage`
  separates caller-owned summary from body content, while `CreateGuidance`
  marks technical values explicitly. Technical detail is useful for operators,
  but it must not make a user-facing action unreadable.
- **Keep details secret-safe.** Never render credential, token, password, or
  secret values in page content, state copy, errors, summaries, or clipboard
  helpers. Explain whether a credential is needed and how to supply or rotate
  it; expose a safe reference or non-secret status instead. Internal workloads
  may be described by name precisely because their setup does not need a URL or
  token.

## Informative examples

These are observed shipped examples, not strings every provider must copy:

| Source | Observed pattern | What it demonstrates |
| --- | --- | --- |
| `providers/agents/portal/src/views/Activity.vue` | `Loading runs…` and `No runs yet. Chat with an agent…` | Pending and authoritative empty snapshots are different states with different next steps. |
| `providers/agents/portal/src/views/DashboardTile.vue` | `Could not refresh. Showing the last loaded data.` | A background failure preserves and labels the last useful snapshot. |
| `portal/src/components/DashboardTile.vue` | `Loading summary…`, `Summary unavailable`, and `Retry`/`Reload page` | A tile distinguishes loading, failure, and the recovery available in the current document. |
| `providers/agents/portal/src/conn-defs.ts` and `assisted-setup.ts` | Internal workload guidance says there is no URL or token and asks for no credential input. | Explain the product's trust boundary without asking users to copy a secret that does not exist. |

## Verification boundary

`make verify-design-docs` checks this entry's metadata, local sources, and
links. The UI conformance scanner and focused component tests check selected
markup/style contracts; they do not perform a comprehensive provider-copy
audit, editorial review, or runtime state census. A new or changed provider
must still be reviewed against this policy and its actual success, empty,
loading, failure, retry, and sensitive-data paths.
