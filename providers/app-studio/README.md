# app-studio provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`faroshq/provider-app-studio`](https://github.com/faroshq/provider-app-studio)
> repository is **automatically synced** from the kedge monorepo
> [`faroshq/kedge`](https://github.com/faroshq/kedge) (path `providers/app-studio/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`faroshq/kedge`](https://github.com/faroshq/kedge) instead.
> See [docs/provider-publishing.md](../../docs/provider-publishing.md) for how
> the mirror is published.

App Studio is a kedge provider that gives each tenant a **persistent AI project
workspace**: named Projects with durable "memory" (goals / requirements /
constraints) and a chat surface backed by the tenant's own LLM credentials,
with optional MCP tool use against their workspace. Projects are stored as
`projects.ai.kedge.faros.sh` resources in the tenant's own kcp workspace; chat
transcripts persist in the provider's message store (Postgres in production and
local dev, with explicit in-memory mode available only for throwaway UI work).

The provider acts **as the calling user**: the hub's backend proxy forwards
`/services/providers/app-studio/*` with the verified `X-Kedge-Tenant` /
`X-Kedge-User` headers and the caller's bearer token, and the provider builds a
per-request, token-scoped client (see `tenant/`). There is no provider
service-account escalation.

## What's here

| Surface | Where |
|---|---|
| Provider binary | `main.go` — loads the provider kubeconfig, opens the message store, mounts `/api` + the embedded portal, heartbeats the hub |
| REST / LLM / message API | `api/` — Project CRUD, memory, LLM settings, streaming chat (`/api/projects/*`) |
| API type | `apis/ai/v1alpha1/` — the `Project` CRD type (deepcopy generated) |
| Typed client | `client/` — trimmed dynamic client for the Project resource |
| Tenant client | `tenant/` — token-forwarding `ClientFactory` (host+TLS from the provider kubeconfig, caller token per request) |
| Message store | `store/` — Postgres + in-memory + envelope-encryption implementations |
| Sandbox runtime | `runner/` + `api/development_*` — project-scoped sync, restart, logs, status, and signed preview proxy for infrastructure-backed `SandboxRunner` workloads |
| Portal | `portal/` — the Vue micro-frontend (`<kedge-provider-app-studio>`), embedded via `assets.go` |
| Registration | `manifest.yaml` — CatalogEntry + APIExport (`ai.kedge.faros.sh`) + Code and Infrastructure provider dependencies + the Project APIResourceSchema + `secrets` claim |
| Deploy | `deploy/chart/` — Helm chart (Deployment, Service, CatalogEntry) |
| CI (mirror) | `.github/workflows/{image,chart}.yaml` — publish the image + chart to GHCR (run only in the mirror) |

## Configuration

Environment variables consumed by the binary:

| Var | Purpose |
|---|---|
| `PORT` | Listen port (default `8081`) |
| `KEDGE_HUB_URL` | Hub base URL (heartbeat + MCP endpoint resolution) |
| `KEDGE_HUB_TOKEN` | Bearer token for the heartbeat |
| `KEDGE_PROVIDER_NAME` | CatalogEntry name (default `app-studio`) |
| `KEDGE_PROVIDER_KUBECONFIG` | Provider kubeconfig (kcp front-proxy host + TLS only) |
| `APP_STUDIO_PREVIEW_BASE_DOMAIN` | Optional DNS zone for companion Sandbox preview routing. |
| `APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAME` | Gateway resource name to attach each preview `HTTPRoute` to. |
| `APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_NAMESPACE` | Namespace of the parent Gateway (defaults to `kedge-preview`). |
| `APP_STUDIO_PREVIEW_HTTPROUTE_PARENT_GATEWAY_SECTION_NAME` | Listener section name on the parent Gateway (defaults to `https`). |
| `APP_STUDIO_PREVIEW_BACKEND_NAMESPACE` | Namespace for the shared preview gateway Service backend. The Helm chart sets this to the release namespace unless `previewGateway.backend.namespace` is configured. |
| `APP_STUDIO_PREVIEW_BACKEND_SERVICE_NAME` | Shared preview gateway Service name. Defaults to the chart-created `preview-gateway` Service when unset. |
| `APP_STUDIO_PREVIEW_BACKEND_SERVICE_PORT` | Shared preview gateway Service port (defaults to `8080`). |
| `APP_STUDIO_PREVIEW_TOKEN_SECRET` | Optional shared signing secret for preview URLs; configure for multi-replica deployments |
| `APP_STUDIO_DATABASE_URL` | Postgres DSN for the message store |
| `APP_STUDIO_IN_MEMORY_MESSAGE_STORE` | `true` → non-durable in-memory store (dev) |
| `APP_STUDIO_MESSAGE_ENCRYPTION_KEYS` | Comma-separated `key-id:base64-aes-key` entries for message content and metadata encryption at rest |
| `APP_STUDIO_MESSAGE_RETENTION` | Retention window (`time.ParseDuration`, e.g. `720h`) |
| `APP_STUDIO_WORKSPACE_ROOT` | Filesystem root for App Studio project workspaces and local file tools |
| `APP_STUDIO_ASSISTANT_MAX_ITERATIONS` | Optional positive emergency model-call ceiling. The default is continuation-driven/unlimited, matching Codex; exhaustion fails with `budget_limited`. |
| `APP_STUDIO_ASSISTANT_ROLLOUT_BUDGET_TOKENS` | Optional positive weighted-token budget for the Project conversation; disabled by default. Usage and reminders survive compaction and carry across runs. Exhaustion produces `failed` with `budget_limited`. |
| `APP_STUDIO_ASSISTANT_MODEL_CONTEXT_TOKENS` | Active model context window used for token-pressure compaction (default `128000` when provider model metadata is unavailable). |
| `APP_STUDIO_BROWSER_WORKER_URL` | Internal URL of the read-only Playwright worker. When unset or unhealthy, `inspect_development_preview` is not exposed to the model. |
| `APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY` | `true` → skip TLS verify on MCP calls (dev) |
| `APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY` | `true` → skip TLS verification only for preview readiness probes (local dev with a self-signed Gateway) |
| `APP_STUDIO_PREVIEW_CONSOLE_ENABLED` | Automatically shares bounded browser-console evidence while the embedded preview is open; set `false` for a deployment-wide kill switch. |
| `APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY` | PEM-encoded P-256 private key used to sign short-lived ES256 iframe capabilities |
| `APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID` | Stable key ID matching the public JWK independently deployed to the preview bridge |

## Local message history

`make run-provider-app-studio` starts/reuses a local Postgres container by
default and passes `APP_STUDIO_DATABASE_URL` to the provider:

```sh
make app-studio-db-up
make run-provider-app-studio
```

Tilt starts the browser worker as a separate resource. Outside Tilt, run
`make run-app-studio-browser-worker`; the provider uses
`http://127.0.0.1:8090` by default. The model can supply only a path within the
server-resolved current preview plus bounded semantic assertions. It cannot
select an origin, click, type, or execute arbitrary JavaScript.

The database container is named `kedge-app-studio-postgres`, listens on
`127.0.0.1:55432`, and stores data under `.kcp/app-studio-postgres/`. Both
Tiltfiles expose it as the `app-studio-db` resource, so hard-refreshing the UI
or rebuilding the provider no longer drops prior conversation history.

Both Tilt stacks also expose an `app-studio-preview-console-key` resource.
It atomically generates and reuses a P-256 key under
`.kcp/app-studio-preview-console/`. The App Studio process receives the private
key and derived key ID; Infrastructure receives only the matching public JWKS
and propagates it into development-preview init containers. The directory is
gitignored and uses mode `0700`; the private key uses mode `0600`. Delete that
directory only when intentionally rotating the local key, then restart the
App Studio and Infrastructure Tilt resources so both sides receive the new
pair. Local Make/Tilt targets intentionally ignore one-sided signing-key or
JWKS environment overrides; use the Helm values or launch the binaries directly
when testing a custom key pair.

To use your own database, set `APP_STUDIO_DATABASE_URL` in the environment or in
`providers/app-studio/.env` (copy from `.env.example`). To intentionally use the
old throwaway behavior, set `APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true`.

## Resilient assistant conversations

Once a Project and its first user message exist, App Studio owns assistant work
on the provider lifecycle rather than on an HTTP request. Its public contract is
Thread → Turn → Item: clients create or select an assistant thread, `POST
.../assistant/threads/{thread}/turns`, materialize the transcript with `GET
.../threads/{thread}/items`, and follow typed events from `GET
.../threads/{thread}/events`. Event sequence numbers and `Last-Event-ID` make
reconnection incremental. Closing an SSE connection only removes that
subscriber; it never cancels the Eino worker. The former `/messages`, latest-run,
resume, stop, and snapshot-stream routes are no longer public.

A message submitted while the current run is working is durable steering for
that same run, not a replacement run. The request names the expected run and is
accepted only for the actor who started it. The supervisor persists an
idempotent receipt, the user item, a new assistant segment, and the advanced run
revision before Eino can observe the input. Eino drains steering between model
calls; input that races with a final response is carried into the new segment.
Admission and the terminal boundary share one lock, so late input is either
queued or rejected for the next run, never acknowledged and lost. The active
collaboration mode remains sticky.

New assistant runs use one sticky collaboration mode: `Default`, `Plan`, or
`Review`. `Plan` is read-only. `Review` is an explicitly started, independently
durable read-only turn over the `current_workspace` target; clients start one
with `POST .../assistant/threads/{thread}/reviews` and may provide bounded review
instructions. It reports evidence-backed findings and is never an automatic
completion gate. `Default` follows the user's request directly and can use the
current evidence tools and one contextual `apply_patch` source-mutation tool.
The semantic action router, WorkItem promotion flow, phase-driven inner loop,
whole-file write tools, and model-facing workspace hydration tool have been
removed. The portal's explicit **Implement plan** action starts a fresh Default
turn rather than silently changing the mode of a running turn.

The Thread/Turn/Item cutover intentionally starts with no canonical threads.
Pre-cutover assistant history is not projected into the new public transcript.
Legacy run/message rows remain an internal Eino persistence bridge during the
cutover and are not exposed to clients.

Every model response batch is admitted before dispatch. Tool-call IDs are
deterministic, malformed calls and conflicting IDs fail closed, and the model's
call order and cardinality are preserved. Eino retains native concurrent
execution and ordered rejoin, while a run-scoped reader/writer gate permits
only explicitly parallel-safe reads to overlap; effects, unknown tools, and
MCP tools are exclusive. An append-only
`AssistantRunEvent` ledger records each admitted call and exact model-visible
result together with its typed semantic disposition. Model-call audit entries
also bind the visible tool contracts to a stable schema digest. The ledger
provides idempotency within the active run and between concurrent workers; it
is not a provider-restart continuation mechanism.

Transient setup failures and incomplete model streams retry from the current
accepted turn history. Partial responses are discarded before tools can be
dispatched or prose published. The configured retry count is bounded at the
execution boundary with a hard maximum of 100, independently of HTTP settings
validation; exhaustion returns the original classified stream failure.

The encrypted, append-only thread event stream records user and assistant items,
tool calls, plans, steering, approval/input requests, and lifecycle transitions.
Thread and turn rows are materialized projections; a turn's terminal projection
and terminal event commit atomically. New turns reconstruct model context from
the latest persisted compaction plus subsequent conversation evidence instead
of dropping tool results. Reasoning, secrets, and transient preview-console
payloads are not stored there.

Source edits use contextual Add/Update/Delete patches. Moves are represented as
an add/update of the destination plus deletion of the source. The repository
commit bridge carries the complete atomic upsert/delete bundle to provider-code.
Failed contextual patches reopen only the affected read coverage so the model
can reread current source and retry without rediscovering unrelated evidence.
If rollback after an I/O failure is incomplete, the actual remaining paths are
reported as a partial failure and retained in the durable dirty-path set; stale
reads for those paths are invalidated before another edit. Dirty paths are
workspace information, not a hidden verification or commit obligation.
Repository commits use the complete server-owned durable dirty bundle,
including paths from earlier turns; the model supplies commit prose rather
than authoritative file scope. Approval is bound to the bundle's current path
membership and content digest, and only successfully committed paths are
removed from the dirty set. The model is
instructed never to commit unless the user explicitly requests repository
persistence.

After a source mutation, runtime verification requires positive completion of
workspace synchronization for that exact mutation revision before it can report
`ready`. Operational readiness covers synchronization, process/log health, and
preview reachability only. It never proves rendered content, interactions, data
flow, application behavior, or acceptance criteria. Verification remains an
optional model-selected tool; middleware does not force it or rewrite the final
assistant response.

Mutation syncs are serialized in submission order per Project UID, and their
revision, status, failure, and one bounded retry are checkpointed across
permission or follow-up interrupts. Runtime verification and commit both hash
the complete dirty bundle. Verification remains optional, but when a run
claims both verified and committed state they must refer to the same digest;
membership or content changes invalidate approval and any stale verification
binding. While a run
owns the project, server-side reservations reject external workspace hydration,
template switching, manual sync, and deletion; matching disabled portal controls
are only the UX layer over that server boundary.

This remains a single-replica execution design: work cannot continue across a
provider restart. Recovery marks an orphaned active turn `interrupted`, while
durable permission and input checkpoints remain resumable. Interrupt first
persists the internal stopping transition, then asks Eino to cancel gracefully.
Clients recover from the canonical item projection and resume the typed event
stream after their last sequence; they do not depend on token replay.

Every effect is re-admitted at the Stop-serialized supervisor boundary against
the run's durable actor digest. The model-visible project/repository snapshot
and executable tool adapters share one per-sample request snapshot, so a tool
cannot execute against an older request view than the one shown to the model.

The public turn lifecycle is `in_progress`, `completed`, `failed`, and
`interrupted`. Approval and structured-input waits are typed items/events within
an in-progress turn, not extra public lifecycle states. Model, provider, and
budget failures are `failed` with structured error data; explicit interruption
and provider process loss are `interrupted`. The portal renders terminal errors
separately from real assistant prose and re-enables input for every terminal
state.

Approval defaults to `on_request`: routine workspace work proceeds, while
consequential external effects and repository commits ask. `always_ask` asks
before every state-changing/external action, and `never` denies actions that
need authority. A plan communicates intended work and progress; it never grants
permission. Default collaboration mode has no structured follow-up tool, while
Plan mode may request structured input and remains read-only.

Lifecycle logs contain only organization, workspace, project, run, revision,
and status fields. They intentionally omit prompt text, assistant content,
tool arguments, and credentials.

Useful checks from this module:

```sh
go test ./...
go test -race ./api ./store
cd portal \
  && npm run test:workbench \
  && npm run test:preview-state \
  && npm run test:preview-actions \
  && npm run test:create-readiness \
  && npm run test:llm-settings \
  && npm run test:assistant-actions \
  && npm run test:assistant-plan \
  && npm run test:assistant-plan-popover \
  && npm run test:conversation-resilience \
  && npm run typecheck \
  && npm run build
```

## Local project files

App Studio keeps project files in its own workspace root so the assistant can
list, read, search, and safely mutate text files before asking provider-code to
commit selected changed files to git. Set `APP_STUDIO_WORKSPACE_ROOT` to choose
the directory; the binary defaults to a temp directory, while the Helm chart
mounts a persistent volume at `/var/lib/kedge-app-studio/workspaces`.

The assistant-facing workspace tools are App Studio local tools. Provider-code
remains the git-source boundary: `commit_project_files` reads changed workspace
files, represents missing dirty paths as deletions, and delegates the atomic
upsert/delete commit to the Code provider's `code__commit_files` tool. A
workspace move is persisted as an upsert of the destination and deletion of the
source in the same repository commit.

## Development runtime

App Studio owns the project-facing development runtime API. It creates and
deletes infrastructure-backed `SandboxRunner` resources in the caller's tenant
workspace, syncs App Studio workspace files to the runner, and serves project
preview URLs by minting signed, host-based URLs from the companion
`SandboxPreviewHTTPRoute` status.

Infrastructure owns the resource composition: the `sandbox-runner` Template uses
KRO to create the runtime namespace, PVC, Deployment, Service, control Secret,
and network policy. App Studio holds **no** kubeconfig to the runtime cluster:
the live data plane (logs, file sync, restart, preview readiness) is served by
the infrastructure provider as subresources on the `SandboxRunner` instance,
which App Studio calls through the hub as the requesting user. See
[`docs/app-studio-runtime-decoupling.md`](../../docs/app-studio-runtime-decoupling.md).

This is the platform
[provider-isolation rule](../../docs/providers.md#provider-isolation-the-cross-provider-boundary)
in practice: App Studio reaches infrastructure-owned workloads only through
the `SandboxRunner` CR and its published subresources — never into the
infrastructure provider's runtime cluster — which is what lets a tenant be
backed by a different infrastructure provider (BYO compute) with no App
Studio change.

When `APP_STUDIO_PREVIEW_BASE_DOMAIN` is set, App Studio also creates a
`SandboxPreviewHTTPRoute` infrastructure resource beside each `SandboxRunner`.
That companion template creates a Gateway API `HTTPRoute` that attaches to the
configured parent Gateway and routes traffic to the shared preview-gateway
Service.

Preview browser traffic does not use the App Studio provider backend path. The
browser goes to the preview host, the cluster Gateway routes to the
preview-gateway Service, and the preview gateway validates the signed session
before proxying to the sandbox Service.

In the Helm chart, set `runtimeKubeconfig.secretName` to a Secret with key
`kubeconfig` to enable those runtime data-plane APIs. Leaving it empty starts App
Studio normally with sandbox runtime operations disabled.

See `../../docs/app-studio-sandbox-runtime.md` for the current runtime boundary
and caveats.
