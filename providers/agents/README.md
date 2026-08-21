# agents provider

Long-running personal AI agents: chat, scheduled and heartbeat runs, tool use,
approvals, budgets, and durable memory — reachable from Slack, Telegram,
Discord, SMTP, and the portal.

APIExport: `agents.faros.sh`, in `root:faros:providers:agents` (or your own
workspace when self-hosted).

## Dependencies

The only hard dependencies are the **hub** and **Postgres**. That is deliberate:
agents is meant to run on its own.

Compute- and storage-backed features — the claude-code runner and the file
workspace — light up only when the `infrastructure` provider is present.
Infrastructure is therefore an *optional* dependency, and is intentionally not
listed under `spec.dependencies`: declaring it would gate enablement on a
provider most installs do not need.

Configure storage with `store.databaseURLSecretRef`. See
[deploy/chart/README.md](deploy/chart/README.md) for every value.

## Running it

- **On the platform**, an admin onboards the provider and mints its credential.
- **Yourself**, faros creates a workspace in your organization, mints a
  credential scoped to it, and generates the install commands under
  **Providers → Self-Hosting** in the portal. See
  [docs/byo-providers.md](../../docs/byo-providers.md).

Self-hosting is the usual choice here if you want the agents' data — conversation
history, memory, credentials for the channels they speak on — to stay in your own
Postgres and your own cluster.

## Product activation telemetry

Product activation telemetry is disabled by default. A self-hosted installation
makes no telemetry network calls unless the chart value
`telemetry.enabled=true` (or `FAROS_PRODUCT_TELEMETRY_ENABLED=true`) is
explicitly set. When enabled, the provider authenticates telemetry with the
ServiceAccount bearer in `providerKubeconfig`; no extra telemetry secret is
required. Events contain only bounded pseudonymous scope, actor, and agent
resource identifiers plus fixed outcome values — never agent names, prompts,
run content, or credentials. Enabled telemetry requires an HTTPS `FAROS_HUB_URL`;
`FAROS_HUB_INSECURE=true` permits HTTP only as an explicit development escape hatch.

## Further reading

- [docs/agents-provider-architecture.md](../../docs/agents-provider-architecture.md)
- [deploy/chart/README.md](deploy/chart/README.md) — chart values
