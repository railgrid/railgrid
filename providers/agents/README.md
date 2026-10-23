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

New endpoints and replacement keys are verified through temporary, explicitly
marked ModelCredential/Secret pairs before the named connection is saved. The
portal removes the pair after verification; if it closes or loses workspace
authority, the tenant-scoped controller removes the pair at its persisted expiry,
capped at ten minutes after server creation. It checks the Secret's probe marker
and exact owner UID, uses conditional deletes, and retains the credential to retry
failed Secret cleanup. These temporary objects do not receive background model
readiness probes. See [probe lifecycle](../../docs/agents-provider-architecture.md#temporary-model-verification-credentials).

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

## Webhooks and triggers

A `Webhook` is one inbound URL (`status.webhookPath`, minted by the provider
with the token that is its whole credential) and the sink of what arrived at
it. Any number of `Trigger`s route from it by naming it in `spec.webhookRef`;
every authenticated POST is offered to each of them, and each judges it with
its own `suspend`, `filter` and `task`. A trigger written before the Webhook
kind existed is migrated by the reconciler to a Webhook of its own name, so
the URL already pasted into GitHub keeps working.

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: agents.railgrid.ai/v1alpha1
kind: Webhook
metadata:
  name: github
spec:
  source: github          # or webhook: any sender that POSTs
  deliveryRetention: 200  # recent deliveries kept in the sink; 0 keeps none
  recordBodies: true      # false keeps headers and verdicts but no payload
---
apiVersion: agents.railgrid.ai/v1alpha1
kind: Trigger
metadata:
  name: review-prs
spec:
  agentRef: reviewer
  webhookRef: github
  filter:
    eventType: pull_request
    action: opened,synchronize
  task: Review the pull request in the payload.
EOF
kubectl get webhook github -o jsonpath='{.status.webhookPath}'
```

Filter keys are `eventType`, `action`, `repository`, `match` (a substring of
the payload) and `header.<name>`; the first three take a comma-separated
any-of list. An unknown key is ignored at delivery time and reported as
`Validated=False/UnknownFilterKey` on the trigger.

The verbs, all kcp custom subresources on the hub's `/clusters/<id>` path:

| Resource | Verb | What |
|---|---|---|
| `webhooks` | `deliveries` (GET) | the sink, newest first (`?limit=&outcome=&trigger=`); `…/deliveries/{id}` one entry whole |
| `webhooks` | `run` (POST) | `{"deliveryID"}` re-feeds a recorded delivery to every routed trigger; `{"headers","body"}` injects a sample |
| `triggers` | `evaluate` (POST) | dry-run a candidate filter against a recorded delivery, answered per key |
| `triggers` | `run` (POST) | fire now; `{"deliveryID"}` replays one delivery through this trigger alone |
| `triggers` | `enable` (POST) | clear the disabling applied after repeated failed runs |

Anyone who can `get` a Webhook can read its sink, bodies included; set
`spec.recordBodies: false` on a webhook whose sender posts secrets or personal
data. A sender's redelivery (same `X-GitHub-Delivery`) does not start a second
run on a trigger that already fired for it.

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

## MCP tool results

MCP tools preserve their complete text response up to a 64 KiB text budget.
JSON supplied in MCP text blocks retains its original numeric precision.
Unlike fetched web pages, these results are not shortened to a fixed prefix:
doing so can remove fields or pagination cursors and break JSON.
An oversized result fails explicitly after the tool has executed. For read-only
queries, the agent can retry with narrower filters or a smaller page when the
tool supports them; side-effecting calls must not be repeated just to recover
the output. This is a per-result safety limit; the conversation still uses the
context-budget and compaction policy below.

## Saved results and MCP Apps

An attended chat can save one successful tool result with
`capture_agent_artifact`, then attach its artifact ID and SHA-256 digest to a
later chat turn. `open_analysis_artifact` rereads only an exact attached
capture; it does not rerun the source tool. Capture and source-backed reopen
require caller-attended MCP so the provider can check current run/source access
and exact source object UIDs; no data-plane POST capture route is served.
Source-free detail reads remain available from the Agent artifacts endpoint.
Each caller can retain at most 100 captures and 16 MiB of result plus recorded
input across all of that caller's Agents in a workspace. The provider reports
size-limit and quota failures directly, and the caller can delete an existing
capture to make room. Artifacts retain only a content-free App resource URI
and SHA-256 receipt; the provider fetches exact HTML transiently for an
attended capture or reopen and never writes it to the run trace or artifact
store. Captures are immutable and caller-owned; list responses contain
metadata only.

Run checkpoints retain only the attached artifact owner and ID/digest pointers,
never the caller bearer or captured bytes. If an approval resume needs to reopen
a source-backed saved result, the data-plane approval route has no caller bearer;
the provider fails that continuation closed with a current-caller-authorization
error. Start a fresh attended chat with the result attached to reopen it.

MCP App negotiation and resource reads are limited to the explicit trusted Hub
aggregate connection. Ordinary MCP connections do not advertise Apps or fetch
their UI resources. An App is available only when its tool result includes
validated source references and its provider plus exact HTML digest appears in
the operator's `RAILGRID_MCP_APP_APPROVED_DIGESTS` allowlist (chart value
`mcpApps.approvedDigests`). The allowlist defaults to empty, which disables
hosted Apps while preserving ordinary captured result data. Capture and every
reopen resolve the exact namespaced URI through the current Hub aggregate and
require the returned HTML digest to match both the saved receipt and an
operator-approved pin. Removing approval or changing the installed resource
withholds the App while leaving the saved result available after source
revalidation. This source check applies to saved capture data and App HTML; it
does not retroactively revoke ordinary historical MCP text, prior assistant
answers, or the existing run trace. Hosted App HTML may make its own network
requests; the provider does not claim to prevent those requests.

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
