# vibe-studio provider

The wizard-first app builder (design: [docs/vibe-studio-design.md](../../docs/vibe-studio-design.md)).
A guided intake collects intent, recommends an infrastructure Template,
provisions a dev sandbox from its scaffold, then drops into conversational
building with preview, build, and promotion to production. Replaces app-studio.

## Status: Phase 0 (skeleton + contract)

What works today:

- One CRD (`Project`, `vibe.faros.sh/v1alpha1`); everything
  conversational lives in the store, never in kube.
- Event-sourced sessions: append-only log (ordinal-CAS'd appends), pure
  `Apply`/`Evolve`/`Fold`/`NextAction` state machines, full wizard lifecycle
  (intake → questions → blueprint review → approve → provisioning checkpoints
  → studio) driven by a deterministic **scripted engine** — no LLM yet.
- HTTP API: `POST/GET /api/sessions`, `POST /api/sessions/{id}/submissions`
  (`input` | `answers` | `approve`), `GET /api/sessions/{id}/events`
  (JSON or SSE). Multi-replica safe via the append CAS.
- Stores: Postgres (`VIBE_STUDIO_DATABASE_URL`) and in-memory (dev).
- Portal custom element (`faros-provider-vibe-studio`) rendering the whole
  flow, chart + CatalogEntry + APIResourceSchema, Dockerfile.

Deterministic lifecycle (first slice of Phase 1): approve resolves the
Template's `instanceCRD` as the caller and records a fully-resolved runtime
binding on the Project spec; a **multicluster Project reconciler**
(kcp apiexport provider — per-shard fan-out handled by the library) then
creates the `farosMode: development` instance in the tenant workspace,
mirrors instance status (phase/url/outputs) into `Project.status`, stamps
`Provisioning`/`Ready`, and tears instances down via finalizer on Project
delete. All decisions are code (`controller/project/desired.go` is pure and
unit-tested); the model never drives lifecycle. Requires the infrastructure
APIExport identityHash on the instance permission claims
(`VIBE_STUDIO_INFRA_IDENTITY_HASH`; dev Makefile auto-discovers it).

Still ahead: Eino engine behind `session.Engine`, workspace FileStore +
scaffold hydration + dev_sync, repo wiring (git checkpoint), build + promote.

## Local dev

Tilt (both flows) has a `providers-vibe-studio` group: `vibe-studio` builds +
serves on :8089 (auto), `vibe-studio-register` ▶ then `vibe-studio-init` ▶
register the provider with the hub, `vibe-studio-db`/`-db-down` manage the dev
Postgres container (:55435). `VIBE_STUDIO_IN_MEMORY_STORE=true` in
`providers/vibe-studio/.env` skips Postgres.

Standalone, without a hub:

```sh
# backend on :8089 with headerless auth and the in-memory store
PORT=8089 VIBE_STUDIO_DEV_TENANT=dev go run . serve

# portal bundle (embedded into the Go binary from portal/dist)
npm --prefix portal install && npm --prefix portal run build

go test ./...
```

`manifest.yaml` registers the localhost variant with a dev hub; in-cluster
installs use `deploy/chart` (the init container self-registers the
CatalogEntry and applies the APIResourceSchemas).
