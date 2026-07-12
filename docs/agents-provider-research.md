# Agents provider — research and architecture options

Status: **Research complete (2026-07-11), design not started.**
Author: 2026-07-11
Related: [`providers.md`](./providers.md), [`mcp-architecture.md`](./mcp-architecture.md),
[`infrastructure-architecture.md`](./infrastructure-architecture.md),
[`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md),
`providers/app-studio/api/assistant_eino_engine.go`,
`providers/infrastructure/install/templates/{worker,cron-job}.yaml`.

## Goal

Add a provider that hosts long-running personal AI agents, OpenClaw-style: a
tenant chats with their agent, the agent runs scheduled/cron jobs on its own,
uses tools over MCP, and keeps durable memory and sessions. Strong preference
for Go, and for reusing the existing provider model (APIExport + REST backend +
portal, like app-studio and infrastructure).

Findings below were produced by a multi-agent research pass with adversarial
verification (claims cross-checked 3-vote against primary sources, 2026-07-11).
Framework, MCP, Claude-headless, and durable-execution claims are 3-0 verified;
OpenClaw scheduler internals are single-sourced from its repo docs.

## What kedge already has

- **Agent loop**: app-studio's assistant is a streaming tool-call loop on
  [CloudWeGo Eino](https://github.com/cloudwego/eino) (`adk.ChatModelAgent`,
  bounded at 32 tool turns, `providers/app-studio/api/assistant_eino_engine.go`),
  with human-in-the-loop tool approval and Eino checkpoint interrupt/resume.
- **Durable runs**: `providers/app-studio/store/` persists messages plus
  resumable `AssistantRun` rows with opaque JSONB checkpoints
  (`SaveAssistantRun`/`ClaimAssistantRun`), tenant-scoped, optionally encrypted.
  This is already the "durability as a library on Postgres" pattern (DBOS-style).
- **Tools**: providers do not embed MCP SDKs as clients; they call the hub's
  aggregate MCP virtual endpoint
  (`/services/mcpserver/{cluster}/.../mcpservers/{name}/mcp`), which federates
  edge clusters, SSH servers, and other providers' MCP servers. app-studio
  already discovers and calls `infrastructure__provision` etc. through it.
- **Tenant LLM credentials**: workspace Secret (`kedge-projects-llm`), BYO
  OpenAI-compatible or Gemini endpoint per tenant.
- **Scheduling**: nothing. The only cron primitive in the repo is the
  infrastructure `cron-job` Template (k8s CronJob via kro,
  `concurrencyPolicy: Forbid`). There is no in-process Go scheduler anywhere.
- **Skeleton**: `providers/quickstart/` is the documented copy-me starting
  point; `provider-sdk/install` bootstraps APIExport/schemas/CatalogEntry.

## What OpenClaw actually is

[OpenClaw](https://github.com/openclaw/openclaw) (Peter Steinberger,
ex-Clawdbot/Moltbot) is a self-hosted personal assistant in TypeScript/Node
(Node 24 recommended). Its core is a **local-first, always-on Gateway daemon**:
a single control plane for sessions, messaging channels, tools, and events,
designed to run as a per-user launchd/systemd service. Cron and session
management are built-in Gateway tools; the scheduler lives inside the daemon
and fires only while it runs. It compensates with application-level
reliability: 3 retries with 30s/60s/5m backoff, extended backoff for
consecutively failing recurring jobs, disable on permanent errors, 60-minute
watchdog on detached runs.

**Verdict**: do not embed it. One Node daemon per tenant (via the `worker`
Template) would demo quickly, but it is single-user by design, its scheduler
dies with the pod, memory is local files, and the control plane cannot reach
its internals. OpenClaw is the *product checklist* (always-on assistant,
cron & wakeups, heartbeats, channels, session tools), not the runtime.

## Framework options (verified)

| Option | Maturity (verified 2026-07) | Assessment |
|---|---|---|
| cloudwego/eino | ~12.2k★, 210 releases, v0.9.12, Apache-2.0, ByteDance/CloudWeGo | Already kedge's stack. ADK with ReAct `ChatModelAgent`, multi-agent `DeepAgent`, checkpoint interrupt/resume with framework-managed state persistence |
| google/adk-go | Announced 2025-11-07, 1.0 | Vendor-backed, but parity-with-Python claims were **refuted** by verification (tool ecosystem/A2A overstated). Would be a second framework beside Eino for no gain |
| langchaingo, genkit | usable | No advantage over Eino here |
| modelcontextprotocol/go-sdk | **v1.0.0 2025-09-30, v1.6.1 stable, semver-stable**, official, Google-maintained | The canonical MCP library; infrastructure's MCP server already uses its API surface |
| mark3labs/mcp-go | 8.9k★, v0.56.0, active but pre-1.0 with explicit maintainer caveats | Skip for new code |

## Claude Code headless from Go (verified)

The Claude Agent SDK ships Python and TypeScript only — no Go SDK. The agent
loop is still fully drivable from Go via `claude -p`:

- `--output-format json` / `stream-json` (parseable result, per-run cost,
  real-time events) and `--json-schema` for structured output.
- `--resume <session_id>` / `--continue` for durable multi-turn sessions;
  session state is JSONL on the filesystem and lookup is scoped to the working
  directory → needs a PVC and per-agent workdirs.
- The TS SDK bundles a platform-native Claude Code binary, so a container does
  not need Node/npm (the "must bundle Node" claim was refuted 0-3).
- Anthropic-locked — no model swapping, unlike Eino + tenant BYO credentials.
- Community Go wrapper exists (`Roasbeef/claude-agent-sdk-go`: subprocess +
  line-delimited JSON, sessions, iterate-until-done loop).

## Durable execution and scheduling (verified)

- **Temporal**: deterministic replay conflicts with nondeterministic LLM/tool
  steps; everything crossing the workflow/activity boundary must serialize;
  worker versioning of long-lived workflows is an operational burden. Overkill.
- **Restate** (single self-hostable binary) and **DBOS** (durability as a
  library: step progress in Postgres, resume from last step, workflow-ID
  idempotency) are lighter; neither is Go-first.
- Strong practitioner counter-current: Armin Ronacher's *Absurd* (durable
  execution as one SQL file on plain Postgres) and Hatchet's Go-on-Postgres
  engine write-ups show the whole pattern — task queue, durable event log,
  non-determinism tracking — is buildable in a single Go binary with pgx.
  app-studio's `AssistantRun` checkpoints are already this pattern in-tree.
- **k8s CronJobs** suit container workloads; an agent run is an LLM turn
  against the provider's engine, so CronJob-per-agent-tick is a pod schedule
  that just curls the provider. In-provider cron is simpler and matches the
  single-Go-binary provider convention.
- Adjacent k8s-native agent runtimes, mined for patterns but rejected as
  dependencies (each is its own control plane, neither is Go-first, **neither
  ships scheduled agent runs** — which makes `AgentSchedule` a differentiator):
  - **kagent** (CNCF Sandbox, Solo.io): Agent/Session CRDs, Go+Python runtimes,
    MCP-native, Postgres sessions + vector memory, Slack/Discord/Telegram.
  - **ARK** (McKinsey): Agent/Model/Team CRDs reconciled by a controller,
    MCP-native, pluggable memory; majority TypeScript, Python/TS SDKs, pre-1.0.

## Architecture options

- **A. Native Go `agents` provider (Eino)** — quickstart skeleton; APIExport
  declares `Agent` (persona, model ref, memory config, MCP tool selection),
  `AgentRun` (one execution: status, transcript ref), `AgentSchedule` (cron
  spec → runs). Chat surface reuses app-studio's streaming pattern; tools via
  the hub aggregate MCP endpoint (agents can provision infrastructure on day
  one). Scheduling: first in-repo cron loop — leader-elected, Postgres
  row-claim, OpenClaw-grade retry/backoff and run watchdog. Durability: the
  `AssistantRun` checkpoint pattern.
- **B. Claude Code headless as the runner** — Go control plane; each run
  spawns `claude -p --resume` in a per-agent pod (infrastructure `worker`
  Template + PVC). Best agent quality, whole tools/hooks/skills surface for
  free; Anthropic lock-in, filesystem session state, pod-per-agent footprint.
- **C. OpenClaw per tenant** — rejected (single-user daemon fleet).
- **D. Adopt kagent/ARK** — rejected as dependencies; reference for CRD shapes.

## Recommendation

**A as the spine, B as a pluggable runner.**

1. New `agents` provider from the quickstart skeleton; APIExport with
   `Agent`, `AgentRun`, `AgentSchedule`.
2. Extract/adapt app-studio's Eino engine and store (natural second consumer
   of the provider-sdk refactor). Tenant LLM creds via the workspace-Secret
   pattern.
3. MCP: hub aggregate endpoint as client; official `modelcontextprotocol/go-sdk`
   for the provider's own MCP tools (e.g. `agents__create_schedule`, so the
   app-studio assistant can create cron agents).
4. In-provider Postgres-claimed cron scheduler with OpenClaw-grade
   retry/backoff and a watchdog. No Temporal/Restate — Postgres checkpointing
   suffices at this scale and keeps the provider a single Go binary.
5. A `runner` interface with two implementations: `eino` (in-process, default,
   BYO model) and `claude-code` (pod via the `worker` Template) for long
   autonomous tasks. Preserves the BYO-compute direction from the app-studio
   runtime decoupling work.

## Sources

Primary sources verified during research: cloudwego/eino, google/adk-go
announcement (developers.googleblog.com), modelcontextprotocol/go-sdk,
mark3labs/mcp-go, code.claude.com/docs (headless, agent-sdk overview),
Roasbeef/claude-agent-sdk-go, openclaw/openclaw, kagent.dev, ARK (McKinsey),
Temporal docs and critiques, Restate, DBOS, Absurd (Ronacher), Hatchet
engineering posts.
