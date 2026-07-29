# Agents provider — multi-channel routing design

Status: **Implemented (2026-07-24).** Full named-channels + multi-inbound.
Backend (API types, outbound routing, multi-inbound `routeChannelAgent`, REST
handlers with inbound-uniqueness validation) and portal (Wiring-tab channel
editor, Flow-canvas multi-channel wiring, schedule/trigger channel dropdowns)
all built; module builds, vets, and tests pass; portal typechecks and builds.
Not yet driven end-to-end against a running hub.
Author: 2026-07-24
Related: [`agents-provider-architecture.md`](./agents-provider-architecture.md)
(the provider this extends), [`agents-provider-research.md`](./agents-provider-research.md).

## Summary

Today an agent binds to exactly **one** messaging channel. All proactive output
(schedule and heartbeat results, budget/error alerts, trigger output, approval
requests) is delivered through a single scalar field,
`AgentSpec.DefaultNotifyConnection`, and inbound messages are routed back to the
one agent whose `DefaultNotifyConnection` names the receiving Connection.

This design lifts an agent to **many named channels**. A user can give an agent
a *primary* comms channel (e.g. Telegram) plus secondary/tertiary channels (e.g.
a Discord `#incidents` room and a `#news` room), and route each schedule or
trigger to a specific channel. Example outcomes:

- A `daily-news` **cron schedule** posts to the `news` channel.
- An `incidents` **trigger** (webhook/GitHub) posts to a dedicated `incidents`
  channel with the on-call people.
- The user can **talk to the agent** from *both* Telegram and Discord, not just
  one.

The change is **additive and backward-compatible**: an existing agent with only
`DefaultNotifyConnection` set keeps working unchanged, treated as a single
`primary` channel.

## Why this is a small change

The hard plumbing already exists:

- **Multiple `Connection` resources are already first-class.** An agent can
  already have several telegram/discord/slack Connections in its workspace; it
  just can't *bind* more than one for messaging.
- **The executor already supports per-job destination override.**
  `executor.Job.ReplyTarget` is stamped by the Discord gateway so a channel run
  replies to the exact room the user typed in
  ([`background.go`](../providers/agents/api/background.go), the
  `KindChannel` branch). This proves per-run routing works end-to-end; we
  generalize it to schedules and triggers.

The single-channel assumption is concentrated in **three** places:

1. `AgentSpec.DefaultNotifyConnection` — a scalar, one Connection
   ([`types_agent.go`](../providers/agents/apis/v1alpha1/types_agent.go)).
2. `ScheduleSpec` / `TriggerSpec` carry **no** destination — output always
   funnels to that scalar
   ([`types_schedule.go`](../providers/agents/apis/v1alpha1/types_schedule.go),
   [`types_trigger.go`](../providers/agents/apis/v1alpha1/types_trigger.go)).
3. Inbound routing reverse-maps **one** agent per Connection via that scalar
   (`routeChannelAgent` in
   [`channels_inbound.go`](../providers/agents/api/channels_inbound.go)) — which
   is why an agent can't receive on a second channel today.

## Resource model

### Agent gains a named-channel list

```go
// AgentSpec
//
// Channels binds named messaging channels to the agent. The channel marked
// Primary (or the first entry) is the default notify target for output that
// does not name a channel. Schedules and Triggers may target any channel by
// name. When Channels is empty, DefaultNotifyConnection is treated as an
// implicit "primary" channel.
// +optional
Channels []AgentChannel `json:"channels,omitempty"`

// DefaultNotifyConnection is DEPRECATED in favor of Channels. When Channels is
// set it is ignored; when Channels is empty it is synthesized as the primary
// channel. Retained for backward compatibility.
// +optional
// +kubebuilder:validation:MaxLength=253
DefaultNotifyConnection string `json:"defaultNotifyConnection,omitempty"`

// AgentChannel binds one logical channel role to a messaging Connection.
type AgentChannel struct {
	// Name is the logical channel role referenced by schedules and triggers,
	// e.g. "primary", "incidents", "news".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// ConnectionRef names the messaging Connection that backs this channel.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	ConnectionRef string `json:"connectionRef"`

	// Primary marks the default notify target. Exactly one channel should be
	// primary; if none is marked, the first entry is primary.
	// +optional
	Primary bool `json:"primary,omitempty"`
}
```

### Schedules and Triggers gain a channel selector

```go
// ScheduleSpec / TriggerSpec
//
// ChannelRef names the agent channel this schedule/trigger delivers to. Empty
// means the agent's primary channel.
// +optional
// +kubebuilder:validation:MaxLength=63
ChannelRef string `json:"channelRef,omitempty"`
```

`ChannelRef` is a **logical channel name** (from `Agent.Spec.Channels[].Name`),
not a Connection name. This decouples automation from the underlying integration
— re-point the `incidents` channel from Discord to Slack by editing the agent,
and every trigger routed to `incidents` follows, with no schedule/trigger edits.

### Resolution helper

A single resolver used everywhere output is sent:

```go
// resolveChannel returns the Connection name for a channel role on an agent.
// role=="" resolves to the primary channel. Falls back to DefaultNotifyConnection
// when Channels is empty. Returns ("", false) when nothing is configured.
func resolveChannel(agent *Agent, role string) (connName string, ok bool)
```

Rules:
- `Channels` empty → `DefaultNotifyConnection` (legacy path).
- `role == ""` → the `Primary` channel, else the first entry.
- `role != ""` → the channel with that `Name`; if not found, fall back to
  primary and log (don't drop the message silently).

## Routing changes

### Outbound (schedule / trigger / notify)

1. **Carry the destination on the job.** Add `Job.NotifyConnection string` to
   [`executor.Job`](../providers/agents/executor/executor.go), mirroring the
   existing `ReplyTarget`. Set at enqueue time by resolving the firing
   Schedule/Trigger's `ChannelRef` through `resolveChannel`.
2. **`background.notify` honors the job.** The notify branch in
   [`background.go`](../providers/agents/api/background.go) (schedule/heartbeat/
   trigger output and failure alerts) sends through `job.NotifyConnection` when
   set, else the agent's primary.
3. **Direct resolvers use primary.** The `notify` and `ask` tools
   ([`tools/core.go`](../providers/agents/tools/core.go)), toolset approval
   delivery ([`toolset.go`](../providers/agents/api/toolset.go)), and
   `deliverToNotifyChannel` ([`triggers.go`](../providers/agents/api/triggers.go))
   resolve `role==""` (primary) — unchanged behavior for the common case, now via
   the shared helper.

Channel *conversation* replies (`KindChannel`) are unaffected: they already
reply to the source connection via `ReplyTarget`.

### Inbound (multi-channel receive)

Rework `routeChannelAgent`
([`channels_inbound.go`](../providers/agents/api/channels_inbound.go)) so an
inbound message on Connection `C` routes to the agent that lists `C` as a
channel's `ConnectionRef`, instead of matching the single
`DefaultNotifyConnection` scalar:

```go
// order of precedence:
//   1. conn.Spec.Config["agent"]  (explicit override, unchanged)
//   2. the agent whose Channels[].ConnectionRef == conn.Name
//   3. legacy: the agent whose DefaultNotifyConnection == conn.Name
```

The per-connection inbound gate at the top of the webhook handlers
(`conn.Spec.Channel` must match the sender) and the Discord gateway's
`inConfigured` check remain per-Connection and need no change.

## Validation

- **Inbound uniqueness (the one real constraint).** A given Connection may be
  the inbound `ConnectionRef` of **at most one** agent — an inbound message must
  resolve to a single agent. Enforced when saving an Agent (reject if another
  agent already claims the Connection) with `config["agent"]` as the escape
  hatch. *Outbound* sharing is unrestricted: many schedules/triggers/agents may
  send to the same channel.
- **Channel names unique per agent**; `ChannelRef` on a schedule/trigger must
  match an existing channel name on its agent (warn + fall back to primary at
  delivery time rather than hard-failing a firing).
- **At most one `Primary`** per agent (default the first entry when none set).

## REST + portal

- **REST** ([`api/agents.go`](../providers/agents/api/agents.go),
  [`api/schedules.go`](../providers/agents/api/schedules.go),
  [`api/triggers.go`](../providers/agents/api/triggers.go)): agent create/update
  accept `channels[]` (keep accepting the legacy `notifyConnection` and map it to
  a primary channel); schedule/trigger create/update accept `channelRef`.
- **Portal**: a channel-list editor on the Agent form (add/remove rows: name +
  Connection picker + primary radio); a channel dropdown on the Schedule and
  Trigger forms, populated from the selected agent's channels, defaulting to
  primary.

## Codegen

Regenerate deepcopy and CRD YAML for the **agents provider module only**
(`providers/agents/`). Do **not** run the repo-wide `make crds` — it regenerates
the core `core.faros.sh` APIExport and drops orphaned schemas, hanging hub
bootstrap (see the standing warning in project memory).

## Migration & compatibility

No data migration. Existing agents have `Channels` empty and keep delivering
through `DefaultNotifyConnection` via the legacy fallback in `resolveChannel`.
Existing schedules/triggers have empty `ChannelRef` → primary → same behavior as
today. The portal can lazily upgrade an agent to the `Channels` model on first
edit (seed a `primary` channel from `DefaultNotifyConnection`).

## Effort

~1.5–2 days, phased:

| Phase | Work | Est. |
|---|---|---|
| 1 | API types (`AgentChannel`, `ChannelRef`), deepcopy, CRD regen | 0.5d |
| 2 | Outbound: `Job.NotifyConnection`, enqueue resolves `ChannelRef`, `background.notify` per-job, direct resolvers via helper | 0.5–1d |
| 3 | Multi-inbound `routeChannelAgent` rework + inbound-uniqueness validation | 0.5d |
| 4 | REST handlers + portal channel editor / dropdowns | 0.5–1d |

## Out of scope (possible follow-ups)

- Fan-out: one schedule delivering to *several* channels at once (model is
  one `ChannelRef` per source; would become `ChannelRefs []string`).
- Per-channel autonomy/tool policy (e.g. read-only in a public channel).
- Channel-scoped filters on inbound (only fire triggers from certain rooms).
