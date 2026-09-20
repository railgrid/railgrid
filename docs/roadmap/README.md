# Roadmap: proposals that are not implemented

Everything in this directory is a plan, not a description of the system.
Nothing here is shipped, and no reader, human or agent, should treat a command,
package, flag or endpoint named in these documents as existing until the
document says so and has moved out.

Conventions:

- Every document starts with a `Status:` line that says **NOT IMPLEMENTED**
  and the date it was written. A document may contain an accurate
  "where we are" section describing current code; that section is dated and
  the rest is proposal.
- When work starts, the status line tracks which phases landed, with PR
  numbers. When the plan is fully implemented or superseded, move the
  document to `docs/` (or delete it) and update the links that point here.
- Existing design documents in `docs/` that describe shipped behaviour stay
  where they are. Plans that were written before this directory existed
  (for example `agents-provider-improvement-plan.md`,
  `security-remediation-plan.md`) keep their own status lines and are not
  moved retroactively.

| Document | Written | Summary |
|---|---|---|
| [provider-authoring-plan.md](provider-authoring-plan.md) | 2026-09-12 | Make creating and installing a provider as smooth as `helm install`: one embedded manifest, an SDK runtime, a library chart, a `railgrid provider` CLI group, and a first-class SaaS/BYO path. |
| [provider-contract-remediation.md](provider-contract-remediation.md) | 2026-09-19 | Bring every provider onto the three-pillar contract, provider by provider: shared `provider-sdk/dataplane` kit first, then quickstart, code, planner/databricks, infrastructure, edges, kuery, factory, agents, app-studio, and the hub identity service. |
| [console-url-slugs-prd.md](console-url-slugs-prd.md) | 2026-09-13 | Add immutable, human-readable organization and workspace URL slugs while retaining UUID identity, authorization, APIs, and provider context. |
| [audit-telemetry.md](audit-telemetry.md) | 2026-09-13 | Turn kcp audit events into signals: enable auditing in both install modes, a small stateless sink (`pkg/audit`, in-process for embedded, `railgrid-audit` for multi-shard) that enriches events with org/workspace/actor, matches YAML rules, and fans out to Discord, Slack, signed HTTP webhooks, stdout and Prometheus; org-scoped subscriptions later. |
| [managed-platform-operator.md](managed-platform-operator.md) | 2026-09-16 | A `railgrid-operator` (own module) that turns one `Platform` CR into a running railgrid: embeds the kcp-operator's importable config/workload controller groups to run a managed multi-shard kcp, installs the hub chart via the Helm SDK, and a `Provider` reconciler that registers, credentials, installs and gates each platform provider; a per-release version manifest drives ordered upgrades. |
