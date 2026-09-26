# agents provider

Long-running personal AI agents: chat, scheduled and heartbeat runs, tool use,
approvals, budgets, and durable memory — reachable from Slack, Telegram,
Discord, SMTP, and the portal.

APIExport: `agents.railgrid.ai`, in `root:railgrid:providers:agents` (or your own
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

## Model credentials

An agent reaches a model through a **`ModelCredential`** — a cluster-scoped
object in the tenant's own workspace carrying the provider flavour
(`openai-compatible` or `openai`), the base URL, a default model id, and a
`secretRef` naming the Secret that holds the API key. The key itself is never
on the object, so a credential is safe to list, watch and show.

The Secret must carry the label `railgrid.ai/owner: agents`. The provider's
`secrets` permission claim is scoped to it, so kcp hides anything without it
from the provider — an unlabelled Secret saves cleanly and is then invisible to
every unattended run. The portal stamps the label on everything it writes; if
you create one by hand, stamp it yourself.

A reconciler keeps the verdict on the object, so its status is the source of
truth rather than a button somebody pressed once:

| Condition | True when |
| --- | --- |
| `SecretResolved` | the Secret exists, carries `spec.secretKey` (default `apiKey`), and is labelled |
| `Reachable` | `GET {spec.baseURL}/models` answered with that key |
| `Ready` | both of the above |

`status.models` records the **chat-capable subset** of what the endpoint
served, which is what the portal's model picker offers, and
`status.lastProbeError` explains a failure (bounded, never the key).

The subset matters. An endpoint answers `GET /models` with everything the
account can reach — for OpenAI that is ~130 ids including speech,
transcription, embeddings, images, realtime, moderation, the legacy completion
models, and the families served only on `/v1/responses` (`*-codex`, `*-pro`,
`*-deep-research`, `computer-use-*`). This engine speaks Chat Completions only,
so picking one of those saves cleanly and then fails on the first turn with the
provider's 404 "This model is not supported in the v1/chat/completions
endpoint". `llm.ChatCapable` / `llm.FilterChatModels` (`llm/chatmodels.go`)
remove them, inside `llm.DiscoverModels` so the reconciler's `status.models`
and the `discover` verb's answer are one list. Curated catalog ids come first,
in catalog order, then the rest alphabetically; the portal groups on that split
(**Recommended** / **Other models this endpoint serves**) and manual entry
stays available for anything the deny-list is wrong about.

Two verbs probe a **saved** credential on demand:
`modelcredentials/{name}/test` runs a real chat round-trip, and
`modelcredentials/{name}/discover` re-reads the model list and refreshes the
status. `test` takes an optional body `{"model": "<id>"}` that probes that
chat-capable id instead of the saved `spec.model` — everything else (endpoint,
key) still comes from the object and its Secret. That is what lets the editor
prove a model a person just picked *before* "Save changes" writes it, rather
than leaving the first agent run to discover it does not work. Agents reference a credential by name in `spec.models[purpose]` and
`spec.modelFallbacks`, and their own `ModelCredentialsReady` condition names
any that are missing or not ready.

```bash
kubectl create secret generic railgrid-agents-model-openai \
  --from-literal=apiKey=sk-… -n default
kubectl label secret railgrid-agents-model-openai railgrid.ai/owner=agents -n default
kubectl apply -f - <<'EOF'
apiVersion: agents.railgrid.ai/v1alpha1
kind: ModelCredential
metadata:
  name: openai
spec:
  provider: openai-compatible
  baseURL: https://api.openai.com/v1
  model: gpt-4o
  secretRef:
    name: railgrid-agents-model-openai
EOF
kubectl get modelcredentials
```

## Dependencies

The only hard dependencies are the **hub** and **Postgres**. That is deliberate:
agents is meant to run on its own.

The hub is also how the provider reaches a tenant's workspace. Every portal,
CLI, and MCP request carries the caller's bearer token and the workspace's
cluster ID (`X-Railgrid-Cluster`); the provider turns those into a plain kube
REST client on the hub's kcp proxy at `<RAILGRID_HUB_URL>/clusters/<cluster-id>`
and acts as the caller. The proxy authorizes by workspace membership, so the
provider can read and write Agents, Connections, Toolsets, and Secrets in any
workspace the caller belongs to, with kcp's own admission and RBAC errors
surfacing unchanged. `RAILGRID_HUB_INSECURE` relaxes TLS for in-cluster hub
certificates.

Compute- and storage-backed features — the claude-code runner and the file
workspace — light up only when the `infrastructure` provider is present. What
the CatalogEntry declares about it is one entry under `spec.requires` naming
`provider: infrastructure` and `group: infrastructure.railgrid.ai`: the
`instances` kind (`get`, `list`, `watch`) and the `instances/proxy` verb
coordinate, which carries no verbs of its own because the verb *is* the
capability. Naming the provider there is also the dependency edge the hub
orders enablement by.

Configure storage with `store.databaseURLSecretRef`. See
[deploy/chart/README.md](deploy/chart/README.md) for every value.

## Running it

- **On the platform**, an admin onboards the provider and mints its credential.
- **Yourself**, railgrid creates a workspace in your organization, mints a
  credential scoped to it, and generates the install commands under
  **Providers → Self-Hosting** in the portal. See
  [docs/byo-providers.md](../../docs/byo-providers.md).

Self-hosting is the usual choice here if you want the agents' data — conversation
history, memory, credentials for the channels they speak on — to stay in your own
Postgres and your own cluster.

## Further reading

- [docs/agents-provider-architecture.md](../../docs/agents-provider-architecture.md)
- [deploy/chart/README.md](deploy/chart/README.md) — chart values

## Conversation history and compaction

Chat, channel, and resumed runs retain structured assistant tool calls and their
matching tool responses. Ordinary tool results remain available on later turns;
they are not shortened to a fixed prefix for replay. Historical records written
by older versions remain readable, but content already truncated by those
versions cannot be recovered by upgrading.

Context pressure is checked before model requests, including tool-schema costs.
When history needs to shrink, the compaction model produces a handoff summary.
The provider persists a versioned replacement checkpoint while retaining the
original transcript. New messages outside the checkpoint's coverage remain in
history, and current agent instructions and tool capabilities are assembled
again for subsequent runs. Compaction is a lossy summary, not a guarantee that
every earlier field or result remains in the active context; agents should
rediscover details when the retained evidence is insufficient.

The engine preserves tool-call/result pairing across interruption and restart.
Missing results are explicitly unavailable rather than evidence that a tool
succeeded. Internal tool-call records without assistant prose are kept for
model replay without creating empty chat bubbles.

## Optional visualization tools

Enable **Visualize data** in an agent's built-in capabilities, or create a
Toolset with that capability and attach it to the agent. It is off by default;
background runs require their own grant. The API family name is
`visualization`, and the first tool is `visualize_data`.

The tool accepts supplied rows and named axes for bar, line, area, scatter, and
pie charts. Agents can compose it with any granted query tool, including
semantic BI tools. It does not query a warehouse or manufacture observations:
query and aggregate the data first, then pass the resulting rows and a source
label. Results are stored with the normal scoped conversation transcript and
render inline in portal chat, including after reloading a session. Other chat
channels receive the agent's textual explanation; inline rendering is a portal
capability.

Charts use bundled [Vega-Lite](https://vega.github.io/vega-lite/) and
[Vega-Embed](https://github.com/vega/vega-embed), with no external rendering
service. The tool accepts a bounded typed chart request, not arbitrary HTML,
JavaScript, remote data URLs, or Vega expressions. Limits are 1,000 rows, 16
fields and 512 KiB per request, with a 513 KiB encoded-result cap. Numbers
that would lose decimal precision in the browser are rejected; round them
explicitly upstream or keep exact identifiers as strings. Prepare larger
datasets upstream. The portal also provides the underlying data table so
the values remain inspectable.
