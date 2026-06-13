# Copilot review instructions — kedge

kedge is a kcp-based multi-tenant control plane (the **hub**) plus pluggable
**providers**. Tenant isolation is the core security property. When reviewing a
PR, weight the checks below heavily and call out any violation explicitly,
citing the file and line. Architecture reference: [AGENTS.md](../AGENTS.md),
[docs/provider-scoping.md](../docs/provider-scoping.md),
[docs/providers.md](../docs/providers.md), [docs/security.md](../docs/security.md).

## 1. Tenant isolation in providers (highest priority)

A provider that talks to kcp MUST act **as the caller, in the caller's
workspace** — never with elevated or shared credentials.

- The hub forwards the caller's bearer token plus a resolved `X-Kedge-Tenant`
  path. The provider's `tenant/` package (`client.go`, `credentials.go`) must
  build a **per-(tenant, caller) dynamic client** scoped to
  `<host>/clusters/<tenantPath>`. Canonical patterns:
  `providers/code/tenant/` and `providers/infrastructure/tenant/`.
- **Flag** any code that constructs a kcp/Kubernetes client that is NOT derived
  from the caller's forwarded token + resolved tenant path: a client built from
  a shared/long-lived kubeconfig, a cached client reused across requests/tenants,
  a package-level/singleton client, or a client whose host/cluster path is not
  scoped to the request's tenant.
- **Flag** any path where the tenant scope comes from request *body* or a
  client-supplied value instead of the hub-resolved `X-Kedge-Tenant` header.

## 2. No credential substitution / privilege escalation

Users must keep their own identity end-to-end. The provider serve identity
(`cluster-admin within the provider's own workspace`) is for the provider's own
resources — it must never be used to act **on behalf of a user** against tenant
data.

- **Flag** any code that replaces, swaps, or falls back to an admin/elevated
  credential when serving a user request: use of `admin.kubeconfig`,
  `kcp-admin`, a "provider" / workspace-admin / cluster-admin kubeconfig, a
  `system:masters` group, or any service-account token, on a code path that
  handles a user's request to tenant resources.
- **Flag** impersonation, token swapping, or "if no token, use the system
  client" fallbacks on user-facing paths.
- Discovery/schema-building at **startup** may legitimately use admin
  credentials (e.g. the graphql gateway's
  `--workspace-schema-kubeconfig-override`). Per-request serving may not. Hold
  this line.

## 3. No new clients — reuse the scoped constructors

- **Flag** new direct uses of `kubernetes.NewForConfig`, `dynamic.NewForConfig`,
  `rest.RESTClientFor`, `client.New` (controller-runtime), raw GraphQL HTTP
  clients, etc., introduced in backend/provider request paths instead of the
  existing `tenant/`-package constructors.
- A backend should not stand up its own GraphQL or kube client to reach data it
  should obtain through the caller-scoped client. If a PR adds one, ask why the
  existing per-tenant client path doesn't suffice.

## 4. Provider boundary / hub-mediation

- Providers must stay self-contained: no new imports reaching into hub-internal
  packages, and standalone providers (own `go.mod`) must not couple to the hub
  binary. Built-ins register via `manifest.go`; standalone via `CatalogEntry`.
- Org workspaces are **hub-mediated only**. Tenants never receive a kubeconfig
  reaching an Org workspace; CatalogEntry/Membership/child-workspace operations
  go through hub REST endpoints (the proxy refuses exec-credentials for
  `root:kedge:orgs:{uuid}` paths). Flag any change that hands a tenant a
  client/kubeconfig into an Org workspace, or that lets a provider write Org-scoped
  resources directly.
- CatalogEntry breaking fields (`spec.apiExport.*`, `spec.backend.url`) are
  immutable by CEL — flag edits that loosen those `XValidation` rules.

## 5. New-provider completeness

When a PR adds a **new provider** (a new `providers/<name>/` directory, usually
with its own `go.mod` and a `manifest.yaml`/`CatalogEntry`), check that it ships
the standard scaffolding and flag anything missing. Follow `providers/quickstart/`
as the reference layout.

- **`README.md`** at `providers/<name>/README.md` — what it does, how to build,
  run, install/uninstall, and its tenant-isolation story.
- **`.github/`** — a `providers/<name>/.github/workflows/` with at least build +
  test CI for the standalone module (see `providers/code/.github/workflows/`).
- **Helm chart** at `providers/<name>/deploy/chart/` with a `Chart.yaml` (every
  existing standalone provider has one: `code`, `kuery`, `infrastructure`,
  `app-studio`, `quickstart`).
- **e2e tests** — a suite under `test/e2e/suites/` whenever the provider has
  tenant-isolation or cross-tenant behavior, plus a `make e2e-<name>` target.
  If the PR adds isolation logic but no e2e suite, ask for one (or an explicit
  justification for why it can't be tested).
- **Wiring** — module added to `go.work`; Makefile `build-<name>-provider[-portal]`
  + `run/install/uninstall` targets; portal embedded via `assets.go` if it has UI.

Treat a missing README / chart / CI / e2e suite as a review finding, not a
blocker — note it clearly so the author can decide.

## 6. Cross-check against AGENTS.md

The repo root [AGENTS.md](../AGENTS.md) is the authoritative architecture and
conventions map; the deeper [docs/](../docs) are the territory. Before
finalizing a review:

- Read [AGENTS.md](../AGENTS.md) and confirm the PR is consistent with it — the
  hub/provider boundary, the three planes (connectivity / tenancy / provider),
  the repository layout, API construction rules, and the build/test/lint/codegen
  commands. Flag anything that contradicts it.
- If a change makes AGENTS.md (or the referenced per-provider docs) **stale or
  wrong** — new provider, moved package, changed command, altered isolation
  model — flag that the docs must be updated in the same PR. Code and AGENTS.md
  should not drift.
- When AGENTS.md and this file disagree, prefer the stricter reading and call
  out the discrepancy so a human can reconcile it.

## 7. General

- Don't approve changes that weaken an e2e isolation suite under
  `test/e2e/suites/` (e.g. `provider`, `providerflags`, infrastructure
  cross-tenant) without an equivalent replacement.
- Secrets/tokens must not be logged or returned in API responses.

When none of the above are at risk, keep comments brief and focus on
correctness. Prefer a small number of high-confidence findings over noise.
