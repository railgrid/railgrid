# tilt-cluster e2e suite

End-to-end tests that run against the **operator-deployed, multi-shard Tilt
stack** — the topology brought up by `make tilt-cluster` / `Tiltfile.cluster`
(kcp-operator + root/theseus shards + front-proxy + the in-cluster hub + the
host-run providers + the kro runtime cluster).

Unlike the other suites (which spawn their own embedded-kcp processes), this one
does **not** own the stack. It assumes the stack is already running and connects
to it, so it exercises the real operator/multi-shard behaviour (cross-shard
CachedResource projection, MCP federation, the caller-identity gate) that the
embedded-kcp suites can't.

## Run it

```sh
# terminal 1 — bring the stack up and leave it running
make tilt-cluster

# terminal 2 — once the stack is healthy
make e2e-tilt-cluster
```

`make e2e-tilt-cluster` prechecks that the hub and infrastructure provider answer
`/healthz` and fails fast with guidance if the stack isn't up. Running the suite
directly (`go test ./test/e2e/suites/tiltcluster/...`) **skips** every test when
the stack isn't detected, so it's safe under `go test ./...`. (`make test`
already excludes `test/e2e`.)

## Connection points (override via env)

| what | default | env |
| --- | --- | --- |
| kcp admin kubeconfig | `tilt-frontproxy.kubeconfig` | `KEDGE_E2E_TILT_KUBECONFIG` |
| hub REST + MCP | `https://localhost:9443` | `KEDGE_E2E_HUB_URL` |
| infrastructure `/mcp` | `http://localhost:8082` | `KEDGE_E2E_INFRA_URL` |
| hub static token | `dev-token` | `KEDGE_E2E_STATIC_TOKEN` |

## What it asserts

- **Provider comes up** (`TestInfrastructureProviderRegistered`) — the
  infrastructure provider's `CatalogEntry` is `Ready` and its `APIExport`
  exports `templates`.
- **Templates broker chain** (`TestTemplatesCatalogProjected`) — the seeded
  `Templates` exist in the provider workspace and the `CachedResource`
  (`publish-templates`) that projects them into tenant workspaces is `Ready`.
- **MCP federation** (`TestInfraMCPToolsFederatable`) — the provider's `/mcp`
  exposes `list_templates` / `describe_template` / `provision`, the tools the
  hub aggregate federates as `infrastructure__<tool>`.
- **Tenant isolation** (`TestTenantIsolationRequiresIdentity`) — a tool call
  with no caller identity (no `X-Kedge-Tenant`, no bearer token) is refused
  rather than silently acting cross-tenant.

## Possible follow-ups

These need tenant-provisioning plumbing and are intentionally not in this first
pass:

- Provision end-to-end as a freshly-created tenant (create workspace → bind the
  infrastructure APIExport → `provision` → assert the `RedisCache` instance is
  created and reconciles), then tear it down.
- Hit the **aggregate** MCP VW (`/services/mcpserver/{cluster}/.../mcp`) with a
  minted per-`MCPServer` SA token and assert `infrastructure__*` shows up in the
  aggregate `tools/list` (full federation, not just the source).
- Two-tenant cross-read denial (tenant A's token cannot read tenant B's
  instances).
