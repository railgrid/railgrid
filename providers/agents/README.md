# agents provider

Long-running personal AI agents: chat, scheduled and heartbeat runs, tool use,
approvals, budgets, and durable memory — reachable from Slack, Telegram,
Discord, SMTP, and the portal.

APIExport: `agents.faros.sh`, in `root:faros:providers:agents` (or your own
workspace when self-hosted).

## Messaging channels

Inbound chat (Slack, Telegram) arrives on a per-connection webhook URL. The URL
carries an HMAC token, but that only proves the caller knows the URL, so every
delivery is additionally verified with the platform's own secret before a
message can run an agent:

- **Slack** — paste the app **signing secret** (Slack app → Basic Information →
  App Credentials → Signing Secret) when creating the connection (or add it to
  an existing one via *Edit*). Requests are checked against
  `X-Slack-Signature` / `X-Slack-Request-Timestamp` (5-minute window). A
  Slack connection with inbound enabled and no signing secret shows
  `Error: webhook verification secret required; update the connection` and its events are
  rejected until one is added.
- **Telegram** — nothing to paste: the provider generates a webhook
  `secret_token` per connection and registers it with `setWebhook`; updates
  without the matching `X-Telegram-Bot-Api-Secret-Token` are rejected.
  Connections created before this existed are migrated automatically at
  startup (the bot's registered webhook is re-registered with the token); if
  that fails the connection says so and **Enable inbound** fixes it.
- **Discord** — chat rides the bot's own gateway WebSocket (no public
  endpoint); **SMTP** is outbound only.

Duplicate deliveries (Slack retries, Telegram redelivery) are acknowledged
without running the agent again, and a full executor queue answers `503` with
`Retry-After` instead of dropping the message. See
[docs/agents-multi-channel.md](../../docs/agents-multi-channel.md#inbound-verification-de-duplication-and-quarantine).

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

## Further reading

- [docs/agents-provider-architecture.md](../../docs/agents-provider-architecture.md)
- [deploy/chart/README.md](deploy/chart/README.md) — chart values
