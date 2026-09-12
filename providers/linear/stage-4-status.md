# Stage 4 status — 2026-09-11

Implemented locally on `codex/gru-stage-4`, stacked above the prior Gru branch.
Nothing pushed, no PR, tag, registry publication, or source mirror created.
Private Gru code remains unchanged. Its Stage 3 Mac acceptance gate stays pending.

## Verified

- Standalone provider module, typed generated Connection/Operation/Event APIs,
  SDK bootstrap and heartbeat, discovered export reconciliation, tenant-scoped
  command submission, MCP adapter, portal, chart and build/release/CI wiring.
- Provider Make verification: codegen, build, scoped lint, race tests, portal
  typecheck and behavioral tests, strict Helm lint. Chart tests cover manifest
  claim parity, init/runtime credential separation, and the one-replica guard.
- Behavioral tests cover HTTP-200 GraphQL errors, revoked credentials, rate-limit
  classification, no write retries, redirect rejection, issue-team restrictions,
  signed timestamps, subscription/team isolation, event deduplication/retention,
  uncertain-write restart handling and conflicting dispatch claims.
- CI-selection and release-command regression tests passed.
- Shared design documentation, PortalKit parity and UI conformance passed.
  The system Node build lacks TypeScript support; shared PortalKit tests passed
  with an official Node 24 runtime supplied through npx instead.
- Four rendered fixtures: light/dark, 1280px/390px. Team discovery and issue
  search use the built bundle with fixture responses. No captured browser
  errors or horizontal overflow. This is rendered fixture evidence, not proof
  of real Linear issue operations.
- Container image built locally with the repository-root Docker context.
- Local Tilt: all three APIs bound Ready. Explicit provider workspace path was
  needed for endpoint publication; the chart and dev target now supply it.
- Live KCP rejected mutation of an Operation's spec. The controller rejected
  a disallowed team. A synthetic credential reported Connection not-ready.
- Two synthetic signed webhook deliveries persisted exactly one Event, even
  when the unsigned delivery header changed. The real APIExport Secret claim
  allowed the labeled fixture and hid an unlabelled synthetic Secret.

## Acceptance still pending

A real API key and webhook subscription are required to finish the saved plan's
Linear acceptance: discover the FAR team, create a disposable issue, update its
state, add/read a comment, and consume an event genuinely sent by Linear. No
actual Linear issue or comment was created by this implementation session.

Supply the credential through an existing Secret or private key file, never in
chat or Git. The local acceptance binding accepts only Secrets carrying
`linear.faros.sh/fixture: stage4`; explicitly label only the intended test
credential. Then create a separate real Connection with the FAR team UUID and
its actual subscription IDs/signing Secret. The synthetic fixture remains
separate and deliberately cannot authenticate to Linear.

## Operational limits

- Uncertain writes require human reconciliation; no automatic semantic matching,
  write replay, or exactly-once external side-effect claim.
- Gap catch-up is an explicit paginated `reconcile` read plus comment refresh.
  It is not automatic continuous mirroring or a complete deletion history.
- Webhook subscriptions and public HTTPS ingress are configured by operators.
  The existing authenticated hub proxy is not weakened for anonymous callbacks.
- One replica, seven-day/1,000-per-namespace notification retention, and operator
  cleanup of terminal Operation history. Use resource quotas for storage limits.
- Independent subagent implementation/review was unavailable: the service
  rejected the executor spawn with its thread limit. Root performed the work.

See [README.md](README.md) for public contracts and operations, and
[chart configuration](deploy/chart/README.md) for hosting requirements.
