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
| kcp admin kubeconfig | `tilt-frontproxy.kubeconfig` | `FAROS_E2E_TILT_KUBECONFIG` |
| hub REST + MCP | `https://localhost:9443` | `FAROS_E2E_HUB_URL` |
| infrastructure `/mcp` | `http://localhost:8082` | `FAROS_E2E_INFRA_URL` |
| hub static token | `dev-token` | `FAROS_E2E_STATIC_TOKEN` |
| operator/KRO runtime kubeconfig | `.faros-cluster.kubeconfig` | `FAROS_E2E_TILT_RUNTIME_KUBECONFIG` |
| operator namespace | `faros-infrastructure-operator` | `FAROS_E2E_TILT_OPERATOR_NAMESPACE` |

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
  with no caller identity (no `X-Faros-Tenant`, no bearer token) is refused
  rather than silently acting cross-tenant.

## Using Config Connector with the infrastructure operator (opt-in)

This example demonstrates how an infrastructure provider `Template` can use
the operator-managed KRO runtime to compose Google Config Connector resources.
It has a credential-free composition check for the infrastructure operator
contract and a manual real-cloud workflow that creates and deletes Pub/Sub.

Run the focused composition test only after the operator-managed runtime is up:

```sh
make e2e-tilt-cluster-config-connector
```

`TestConfigConnectorComposition` verifies the operator's
`InfrastructureProvider` lifecycle (`Bootstrapped`, `KroReleased`,
`ProviderDeployed`, and `Registered`), applies a test-owned minimal
`storage.cnrm.cloud.google.com/v1beta1` `StorageBucket` CRD, and creates a
test-only `Template` whose `GCSBucket` instance composes to a KRO-labeled
runtime `StorageBucket` child. The assertion checks the child's `location` and
`uniformBucketLevelAccess` values. This is the same composition boundary used
when a real Config Connector controller reconciles that child into GCP.

This is deliberately a composition-only check. It does not install or fake a
Config Connector controller, configure `ConfigConnectorContext` or cloud IAM,
contact GCP, or prove that a real bucket is reconciled. If a non-test-owned
StorageBucket CRD is already installed, the test skips rather than replacing
it. Direct package execution also skips cleanly when the Tilt stack or runtime
kubeconfig is absent.

### Real Pub/Sub create/delete demonstration (manual Tilt workflow)

Config Connector is not part of ordinary `make tilt-cluster`. The Tiltfile
surfaces three manual resources, all default-off and independent of the
automatic resource graph:

1. `config-connector-install` downloads the checksum-pinned Config Connector
   1.153.0 operator, imports the service-account file into
   `cnrm-system/gsa-key`, configures cluster mode, and waits for healthy
   controllers plus the `PubSubTopic` CRD.
2. `config-connector-enable` applies the checked-in
   `providers/infrastructure/contrib/config-connector/pubsub-template.yaml`
   to the infrastructure provider workspace and waits for the Template,
   infrastructure APIExport, tenant-facing `publish-templates` cache, and
   runtime KRO ResourceGraphDefinition. It fails if the cache does not converge
   or the exact Template is absent from the replication endpoint, so provider
   readiness alone cannot falsely report console availability.
3. `config-connector-smoke` creates and deletes one uniquely named cloud topic
   through that already-enabled Template. It does not install Config Connector
   or re-apply the Template.

The equivalent Make targets are `config-connector-install`,
`config-connector-enable`, and `config-connector-smoke`. Set the disposable
GCP inputs in the gitignored repository-root `.env` (the tracked example uses
dev-neutral placeholders). The public names are
`FAROS_CONFIG_CONNECTOR_GCP_PROJECT` and
`FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE`; the older
`FAROS_E2E_GCP_*` names remain accepted for compatibility:

```sh
export FAROS_CONFIG_CONNECTOR_GCP_PROJECT='disposable-test-project'
export FAROS_CONFIG_CONNECTOR_GCP_CREDENTIALS_FILE='/absolute/path/to/service-account.json'
make config-connector-install
make config-connector-enable
make config-connector-smoke
```

The service account must be able to mint OAuth tokens and create, get, and
delete Pub/Sub topics in the selected disposable project; the Pub/Sub API must
already be enabled. The credential file is read locally, imported only into the
runtime cluster's `cnrm-system/gsa-key` Secret, and never copied into the
repository or rendered inline in YAML. The installer is idempotent. It
intentionally leaves Config Connector installed after the smoke, but the test
removes every test-owned tenant workspace/binding, parent instance, child CR,
and cloud topic. The stable enabled Template is left in place for the next
manual smoke; delete it explicitly when you want to disable the offering.

For backwards compatibility, `make e2e-tilt-cluster-config-connector-gcp`
still performs installation and runs the isolated lifecycle test. The
lifecycle test loads the same checked-in YAML and renames it before creating a
test-owned Template, so it cannot mutate the stable enabled fixture.

`TestConfigConnectorGCPPubSubLifecycle` requires the child to report
`Ready=True` at its current generation. A direct authenticated Pub/Sub REST GET
must then return HTTP 200. After deleting the Faros parent, both Kubernetes
objects must be NotFound and the same REST GET must return HTTP 404. Failure
cleanup can directly delete only the exact `faros-kcc-e2e-<hex>` topic and must
also prove HTTP 404; it never removes finalizers.

To rerun the isolated lifecycle without reinstalling the operator, use
`make e2e-tilt-cluster-config-connector-gcp-run`. Setting the real-cloud opt-in
without both required environment variables is a failure, not a skip. The
manual smoke also requires the install and enable actions to have completed.

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
