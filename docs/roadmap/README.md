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
| [provider-authoring-plan.md](provider-authoring-plan.md) | 2026-09-12 | Make creating and installing a provider as smooth as `helm install`: one embedded manifest, an SDK runtime, a library chart, a `faros provider` CLI group, and a first-class SaaS/BYO path. |
