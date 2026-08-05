# Agents invocation API — delegate a task, get the result

> **Status (2026-08-03): all four phases implemented.** Companion doc:
> [agents-deep-research.md](agents-deep-research.md) (sub-agents inside one
> run). This doc is about the *outside* surface: how another kedge provider, an
> MCP client, or a plain HTTP caller hands an agent a task and retrieves the
> answer.
>
> **What shipped:** `Run.Output`/`Run.Sources` on the run record and on
> `GET /api/runs/{id}`; `POST /api/agents/{name}/runs` (ad-hoc task, detached,
> `202 {runId}`, optional inline `wait`); `GET /api/runs/{id}/wait` long-poll;
> caller-supplied idempotency keys enforced by a partial unique index; and MCP
> `run_agent` / `get_run` / `list_runs`, federated by the hub as
> `agents__run_agent` and friends. See "Implementation notes" at the end.
>
> Phase 3 added the `/s2s/clusters/{cluster}/…` routes, where a caller presents
> its own ServiceAccount token and the provider does its own TokenReview +
> SubjectAccessReview; phase 4 added best-effort completion callbacks.
>
> **Not verified against a live kcp.** The S2S authn/authz path needs a running
> hub with the new APIExport claims accepted; everything around it is tested and
> it fails closed (503) without a virtual-workspace connection, but the
> TokenReview/SAR calls themselves have only been exercised by construction.

## Problem

Nothing outside the agents provider can invoke an agent today (verified: no
imports, no HTTP calls, no APIExport references from any other provider). The
existing surfaces each miss the mark for programmatic delegation:

| Surface | Why it isn't enough |
|---|---|
| `POST /api/agents/{name}/chat` (`api/agents.go:516`) | Only sync path, but SSE-only: needs a flushing client, dies with the request context, result is buried in a `done` event. |
| `POST /api/schedules/{name}/run`, `/api/triggers/{name}/run` | Fire-and-forget `202 {runID}`, but require a **pre-created** Schedule/Trigger CR — no ad-hoc task. |
| Trigger webhooks (`api/background.go:846`) | HMAC-token auth, no caller identity, still requires a pre-created Trigger. |
| `GET /api/runs/{id}` (`api/runs.go:152`) | Returns phase, input, steps, children — **not the output**. The final assistant text is a transcript row only reachable via `GET /api/agents/{name}/messages?session=…`, which requires knowing the session ID. |
| MCP (`api/mcp.go`) | Configuration surface only. `run_schedule`/`run_trigger` exist; `run_agent`, `get_run`, `list_runs` do not. |

Telling detail: `RunTriggerAPI = "api"` has been declared in
`apis/v1alpha1/types_run.go:28` since the beginning and is produced nowhere —
the slot for this feature was reserved and never built.

## Goals

1. **Invoke**: run an agent with an ad-hoc task, no pre-created CR, and get a
   `runID` back immediately.
2. **Wait or poll**: a caller can block for the result (bounded), or poll and
   fetch the result later — in both cases via the run API, without spelunking
   transcripts.
3. **Callable by other providers** using their **own ServiceAccount** identity
   (not a rider on a user token), authorized per tenant workspace.
4. **Callable over MCP**, so agents in one workspace (and any MCP consumer)
   can delegate to agents via the federated `agents__*` tools.

## Non-goals

- Sub-agent fan-out *inside* a run (that is
  [agents-deep-research.md](agents-deep-research.md)).
- Cross-replica event streaming / durable executor. Polling and long-poll are
  designed to work correctly on a multi-replica deployment; push events are
  best-effort as today.
- A `Run` CRD. Runs stay store-native (`apis/v1alpha1/types_run.go:19-31`
  records the rationale); the API below is the contract.

## Design

### 1. Result on the run record (prerequisite, smallest piece)

Persist the run's final output where the run lives.

- Add `Output string` to `store.Run` (`store/store.go:81`), new column
  `output TEXT` on `agents_runs` (clip at 64 KiB like tool results).
- `finishRun` (`api/run.go:355`) currently writes `Message` only for
  errors/cancellation; on success it now also stamps `Output` with the final
  assistant content (which `executeTask` already has in hand when it writes the
  transcript row at `api/run.go:272`).
- `runDetail` (`api/runs.go:60`) gains `output`; `runSummary` gains a
  `hasOutput bool` so lists stay light.

This alone fixes the worst asymmetry: today a caller can learn *that* a run
succeeded but not *what it said*.

### 2. `POST /api/agents/{name}/runs` — the invoke endpoint

Fills the vestigial `RunTriggerAPI` slot. Reuses `startDetachedRun`
(`api/run.go:291`) — pre-write a `Pending` run, detach with
`context.WithoutCancel`, return immediately.

```http
POST /api/agents/{name}/runs
{
  "task": "Summarize open incidents and their owners",   // required
  "sessionId": "",            // optional: continue an existing session;
                              // default: fresh session "api:<runID>"
  "idempotencyKey": "",       // optional, see §5
  "timeoutSeconds": 0,        // optional, capped by spec.limits as today
  "wait": 0                   // optional long-poll, see below
}
→ 202 { "runId": "…", "phase": "Pending" }
```

- Trigger stamped as `RunTriggerAPI`. Tool grants follow the **background**
  class (`spec.tools.background`), same as schedule/trigger runs — an API
  caller is not an interactive user, and background runs already have the
  right posture (agent SA data-plane token, no edges pass-through,
  approval-gating intact: a run that hits an approval gate parks in
  `PendingApproval` and the caller sees that phase when polling).
- Budget, `maxToolTurns`, timeout, usage accounting: unchanged — everything
  funnels through `executeTask` (`api/run.go:161`), so the endpoint inherits
  all governance for free.
- `wait: N` (seconds, cap 120): the handler starts the detached run, then
  long-polls the store for a terminal phase before responding. On timeout it
  returns `202` with the current phase — the caller falls back to polling.
  This gives simple callers sync semantics without holding the run itself
  hostage to the HTTP request (the run always survives disconnects, unlike
  `/chat` today).

### 3. Result retrieval

- `GET /api/runs/{id}` → now includes `output` (§1).
- `GET /api/runs/{id}/wait?timeoutSeconds=60` → long-poll: returns as soon as
  the run reaches a terminal phase (`Succeeded|Failed|Aborted`) or
  `PendingApproval`, else `200` with the current non-terminal state at
  timeout. Implemented as store polling (1–2 s interval), NOT via the
  in-process `eventBus` (`api/events.go:31`) — the run may be executing on a
  different replica, and Postgres is the only shared truth.
- `POST /api/runs/{id}/cancel`: exists; unchanged.

### 4. MCP tools

Three additions to `api/mcp.go` (breaking the "configuration surface only"
scope on purpose — update the server instructions at `api/mcp.go:48`):

- `run_agent {agent, task, sessionId?, wait?}` → `{runId, phase, output?}`.
  With `wait` set, behaves like the REST long-poll and returns the output
  inline when the run finishes in time.
- `get_run {runId}` → phase, timing, usage, `output`, error message, children.
- `list_runs {agent?, phase?, since?, limit?}` → summaries.

Because the hub federates these as `agents__run_agent` etc.
(`pkg/hub/mcpaggregate`), every MCP consumer in the tenant — including *other
agents* holding the edges family — gains delegation with zero additional
plumbing. This is the cheapest cross-agent delegation path and ships before
any S2S auth work: it rides the calling user's token exactly like every other
federated tool.

Note `mcpRunNow`'s tenant-mapping precondition (`api/mcp_config.go:655`,
"open the agents UI once") applies here too; keep the same guard and error
text until the mapping story improves.

### 5. Correlation and idempotency

- `idempotencyKey`: unique per `(org, workspace, agent)`. On conflict return
  the **existing** run (`200`, not `202`) — retried deliveries and at-least-
  once callers get exactly-one-run semantics. Implemented as a nullable
  indexed column on `agents_runs`.
- The `runId` is the correlation handle everywhere (REST, MCP, and the
  `parent` filter on `GET /api/runs` already threads delegation lineage).

### 6. Service-to-service auth: provider SAs

Decision: **callers use their own ServiceAccount token; the agents provider
authorizes it locally against the target workspace.** No rider on user
tokens, no new hub machinery.

Why not the hub tenant resolver: `pkg/hub/provider_tenant_resolver.go:138`
resolves bearer → User CR → personal org / membership index. Provider SAs
have no User CR and no membership, so hub-side injection of `X-Kedge-Tenant`
can never work for them without teaching the hub a parallel SA identity
model. We don't need to: the app-studio → infrastructure data plane
(`providers/app-studio/api/dataplane_client.go`) already established the
pattern — address the target workspace **by cluster ID in the URL**, forward
the bearer as-is through the hub backend proxy, and let the receiving
provider authorize.

S2S route (alongside, not replacing, the portal-shaped routes):

```
POST {hub}/services/providers/agents/s2s/clusters/{clusterID}/agents/{name}/runs
GET  {hub}/services/providers/agents/s2s/clusters/{clusterID}/runs/{id}
GET  {hub}/services/providers/agents/s2s/clusters/{clusterID}/runs/{id}/wait
```

Authorization in the agents provider, per request:

1. **Authenticate**: TokenReview on the caller's bearer. Do this through the
   provider's kcp access — NOT by re-rooting a workspace-scoped kubeconfig to
   the tenant path (that exact move 404s behind the prod hub proxy; see the
   edges provider's `authorize()` lesson). The agents APIExport should claim
   `subjectaccessreviews` / `tokenreviews` so both calls ride the APIExport
   virtual workspace the provider already uses for background work
   (`api/background.go`).
2. **Authorize**: SubjectAccessReview in `{clusterID}` — does the
   authenticated subject have `create` on `agents.kedge.faros.sh/v1alpha1
   agents/delegate` (a named subresource used purely as an RBAC hook)?
   Verb-level RBAC in the tenant workspace then controls delegation: a tenant
   (or a provider's bootstrap step) grants a calling provider's SA exactly
   this, and nothing else. Read access to runs maps to `get` on the same
   subresource.
3. Resolve `clusterID → (org, workspace)` scope via the existing
   `TenantRef` mapping (`store.SaveTenantRef`, `api/http.go:160`) — the same
   mapping background execution already depends on.

Caching: TokenReview+SAR results cached ~2 min per (token-hash, cluster),
mirroring the 30-min identity cache pattern in `api/agentidentity.go:99`.

The result: a provider calls agents with the same SA credential it already
holds for its own hub/VW access, and the audit trail shows the run's
initiator as `system:serviceaccount:…` — a real identity, not a borrowed
user.

### 7. Completion callback (phase 3, optional)

For callers that don't want to poll: `callback: {url, secret}` on the invoke
body. On terminal phase, POST `{runId, phase, output, usage}` with an HMAC
signature header (same recipe as trigger webhooks, `api/background.go:134`).
Best-effort, 3 attempts with backoff, no delivery guarantee — pollers remain
the reliable path. Restrict `url` with the SSRF guard from
`tools/web.go:52`. Deferred until a concrete consumer needs push.

## What deliberately does not change

- **Channels remain the human delivery path.** API-triggered runs do not
  notify channels unless the agent itself calls `notify` — the caller asked
  programmatically and gets the result programmatically.
- **Task-injection posture**: an ad-hoc `task` from an authorized caller is
  no more exposed than webhook triggers, which already append free-form JSON
  to the task (`api/triggers.go:268`). Approval gates and tool grants are the
  containment, as today.
- **No Run CRD**, no executor changes, no new run phases.

## Phases

| Phase | Contents | Unblocks | Status |
|---|---|---|---|
| 1 | §1 output-on-run + `GET /runs/{id}` returning it | everything below; portal run view improves too | **done** |
| 2 | §2 invoke endpoint + §3 `/wait` + §5 idempotency + §4 MCP tools | user-token delegation: portal, MCP consumers, agent→agent via federated tools | **done** |
| 3 | §6 S2S route + TokenReview/SAR authz + APIExport claims | provider→agent delegation under provider identity | **done** |
| 4 | §7 callbacks | push-style consumers | **done** |

Phases 1–2 deliver the whole "delegate and wait/poll" loop for every caller
that has a user token. Phase 3 is where the S2S identity work lives and can
proceed independently.

## Implementation notes

Recorded because each was decided while building, not designed up front.

1. **An API run is NOT interactive**, even though its caller supplies a user
   token. `isInteractive` used to list `RunTriggerAPI` (declared but never
   produced, so nothing depended on it); it now does not, which puts API runs on
   `spec.tools.background` — the narrower grant — because nobody is present to
   answer an approval gate. A caller needing more widens the background grant.
2. **The edges gate became explicit.** It read
   `if run.EdgesEndpoint != "" && run.HubToken != ""`, relying on background runs
   carrying no token. An API run *does* carry one (it needs it for the
   infrastructure data plane) while being unattended, so the condition now tests
   the class as well. The old comment claimed "naturally interactive-only"; it is
   now actually enforced.
3. **Idempotency is enforced by the database, not only by the read.** A partial
   unique index on `(org, workspace, agent, idempotency_key) WHERE key <> ''`
   means two racing retries cannot both pass the pre-check and create duplicate
   work. Partial so the overwhelming majority of runs, which carry no key, stay
   unconstrained.
4. **`IdempotencyKey` had to be on `taskRun`, not just the pre-write.**
   `executeTask` rewrites the run record from a fresh struct literal, so a key
   stored only by `startDetachedRun` would have been silently dropped the moment
   the run started.
5. **An API run does not notify the agent's channel.** `startDetachedRun`
   delivers to the notify channel because its other caller is schedule/trigger
   run-now, whose whole point is that delivery. An API caller polls or waits, so
   pushing to the channel as well would message the user about work they never
   asked to hear about.
6. **`runDetailFor` was extracted** so `GET /api/runs/{id}`, the long-poll, and
   an invoke that waited all return the same shape. Three ways of asking about a
   run answering in three shapes would have been a bad contract.
7. **`/wait` polls the store, deliberately.** The in-process event bus cannot see
   a run executing on another replica; Postgres is the only thing both agree on.
   Tested by having another goroutine transition the run and asserting the waiter
   observes it.
8. **`PendingApproval` counts as settled** for a waiter. Nothing further happens
   until a human acts, so blocking longer is pointless — the response says so in
   words rather than leaving the caller to infer it from a phase.
9. **The MCP surface is no longer configuration-only.** The server instructions
   said so explicitly; they now lead with `run_agent`. This is the cheapest
   cross-agent delegation path and needed no new auth: it rides the calling
   user's token like every other federated tool.

## Testing

Shipped (`api/invoke_test.go`, `store/postgres_test.go`):

- `runSettled` treats the three terminal phases *and* `PendingApproval` as
  settled, and `Pending`/`Running` as still moving.
- `waitForRun`: returns immediately for an already-settled run; observes a
  transition made by another goroutine (the multi-replica case, via the store);
  times out without mutating the run; stops when its own caller is cancelled.
- Idempotency: lookup hits, misses on an unknown key, never matches an empty
  key, and does not leak across agents. On Postgres the partial unique index
  rejects a second keyed run while leaving keyless runs unconstrained, and the
  same key under a different agent is allowed.
- API tool posture: `RunTriggerAPI` is not interactive while chat and channel
  still are; an API run takes the background grant (no `spawn`, no
  `web_search`, core intact); and `edges` is withheld *even with the caller's
  token set* — the case the old token-presence check would have got wrong.
- Live boot: both routes are registered and auth-gated (401 without tenant
  headers, 404 for a neighbouring path), and `tools/list` on `/mcp` returns
  `run_agent`, `get_run`, `list_runs`.

Not yet covered: a handler-level test of `invokeAgentRun` itself (it needs a
tenant GraphQL client, which the suite has no fake for — the pieces it composes
are each tested), and everything in phases 3–4.

### Phase 3–4 notes

10. **The caller identity that actually works is a ServiceAccount minted in the
    TENANT workspace**, not a provider's own SA. kcp ServiceAccount tokens only
    authenticate in their home logical cluster: a token whose home is provider
    A's workspace can only be TokenReview'd there, and the agents provider cannot
    address another provider's workspace. The code handles a foreign SA (it
    re-roots at the token's home cluster and cluster-qualifies the resolved
    identity, mirroring `providers/edges/internal/tunnel/auth.go`), but that path
    only succeeds where the home cluster is reachable. The supported pattern is
    the one the platform already uses twice — `api/agentidentity.go` mints an SA
    in the tenant workspace, and edges mints agent credentials in the consumer
    workspace: the caller holds an SA *in the workspace it is invoking into*.
11. **Authorization is the tenant's to grant, in their own RBAC.** The SAR asks
    about `create`/`get` on `agents.kedge.faros.sh` `agents/delegate`, named for
    the agent — a subresource that exists only as an RBAC hook, so permission to
    *run* an agent is expressible and revocable separately from permission to read
    or edit its configuration, and can be scoped to one agent by `resourceNames`.
    Nothing is granted by default.
12. **Both reviews go through the APIExport virtual workspace**, never by
    re-rooting the provider kubeconfig at the tenant path — that is the approach
    the production hub proxy answers with an opaque 404 (kcp#4279), the same trap
    the edges provider hit. This is what the new `tokenreviews` +
    `subjectaccessreviews` claims are for, and `tenantScoped: true` confines each
    review to the tenant's own workspace.
13. **The run executes as the AGENT's ServiceAccount, not the caller's.** The
    caller's token authorizes the *request*; it does not become the identity the
    agent acts with. Same posture as a scheduled run, and it means an S2S invoke
    cannot be used to borrow the caller's privileges inside the workspace.
14. **Decisions are cached for 2 minutes, including denials.** Two API calls per
    poll of a long wait would be pointless load; caching denials stops a
    misconfigured caller retrying in a loop from becoming load on kcp. The cache
    key separates (token digest, cluster, verb, agent) — tested, because an allow
    for one agent leaking to another would be the whole authorization model gone.
    The token itself is never stored, only a digest.
15. **Availability failures are 503, not 403.** A caller seeing 403 goes and
    rewrites RBAC; one seeing 503 retries. With no virtual-workspace connection
    the provider cannot authenticate anyone, so it fails closed with 503 —
    verified on a live boot.
16. **A callback URL is caller configuration, not a model-chosen destination.**
    My first cut used the public-only SSRF guard, which a test caught immediately:
    it refuses loopback and private addresses, so an in-cluster callback target —
    the normal case for a service-to-service caller — could never be reached. It
    now uses the same "configured endpoint" client a websearch Connection's
    baseURL uses (private allowed, link-local still refused), which is what
    `dialGuard`'s own comment says the trust distinction is for. One dial guard,
    two documented trust levels; no second implementation.
17. **Callbacks are best-effort by construction.** They fire from the goroutine
    that ran the task, so a restart between finishing and delivering drops one.
    Polling stays the reliable path, and an idempotency key makes a re-invoke
    safe. An at-least-once outbox would need a delivery queue, cross-restart
    retries, and a dead-letter story — worth building when a consumer needs it,
    not on spec. A failed or approval-paused run notifies the callback too, or a
    caller waiting on one would wait forever.
