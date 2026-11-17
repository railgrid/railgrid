# Railgrid design-system audit

The local review recorded **27 findings across the host portal, seven provider portals and their shared UI**. Fifteen are resolved. Twelve variations have a documented functional reason. Complete design-book conformance across all runtime states remains unverified.

## Edge error notices and PR overlap

A failed harness switch, harness selection or permissions save shows an error beside that control. Service deletion, configuration and credential failures each have their own notice. A successful write elsewhere cannot erase the failed operation's message. A late response from an earlier resource cannot put its error on the current one. PR #1182 landed those changes upstream. This branch keeps the shared refresh spinner in the service view.

During the audit, all 205 Edges tests passed. Chromium fixtures rejected a harness write and a service configuration save and verified that the corresponding alert appeared and the initiating control kept keyboard focus. The branch was then rebased onto upstream main after #1182 merged. Three overlapping files dropped out of the diff entirely, and the service editor diff now contains only the shared spinner change.

## Other changes already on the branch

- All 41 legacy feedback occurrences in Code, Edges, Infrastructure and Kuery have a disposition. Thirty-nine use the shared notification and two use field validation. A separate review of 44 Agents and AgentKit sites moved 38 ordinary notices to the shared component. Six progress or multi-state indicators retain their specialized presentation.
- Four section bars use shared Tabs, fourteen busy icons use the shared spinner and three Edge connection instructions use the shared technical disclosure. Five standalone provider information pages share one inline stylesheet. Their before and after screenshots matched in both system themes.
- Organization creation keeps the centered pre-workspace layout. Its one-field form now uses the shared title, surface and footer. All 13 Kuery Chromium cases passed after fixing the four failures that also reproduced before the notice migration. The browser fixture now verifies first-use creation of the user’s scratch SavedView.

## Coverage and limits

All **1,745 candidate files** have a recorded disposition and a verified content hash. The audit mechanically analyzed 1,091 source files, checked 468 shared copies, inventoried 108 support records and documented 78 exclusions. The route matrix accounts for **133 route and state families**. Fifty-five lack a rendered observation and the Kuery impact state remains blocked. The older Tilt survey observed 68 families and the login precondition. Eight additional families have current-source fixture evidence. Targeted fixtures also revisit some of the earlier observations.

The design, copy-sync and UI conformance gates pass. Live mutation recovery, a full assistive-technology pass, and real-data Kuery graph behavior remain open verification work. The current review has 284 CSS similarity groups with insufficient evidence to classify as duplicate implementations.

[Machine-readable findings and source locations](findings.jsonl)

The separate local bundle contains the offline illustrated report, coverage matrix, verification logs and reproduction commands. Set `AUDIT_DIR` to a copy of that bundle to regenerate the large exports outside the worktree.
