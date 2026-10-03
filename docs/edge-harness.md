# Edge harnesses

A **harness** is a coding agent — headless Claude Code or the Codex app-server —
that the railgrid agent supervises on an edge host and publishes as an ordinary
edge `Service` speaking the `runner/v1` protocol.

A machine that has a harness installed and is joined to a workspace is a harness
host for that workspace. There is no second step on the machine and none on the
hub. The opt-out is one field.

This page replaces `edge-addons.md`. The `Addon` kind, the `--allow-addon` flag
and the `addon-credentials` verb no longer exist.

## The two halves

Harness support is split so that neither side holds the other's half:

| | Decided by | Where it lives |
| --- | --- | --- |
| **Which harnesses this machine can run** | the machine, and whoever can update the edge | `spec.harness` on the edge; the agent applies it live |
| **Whose model account a turn is billed to** | the caller | sent with every `runner/v1` attempt; nothing on the host |

The machine says what it can run. The caller says who is paying. A host that is
running a harness holds no model credential, authenticates to no model provider
on its own, and cannot run a turn that nobody asked for.

## Turning it on, and off

```yaml
apiVersion: edges.railgrid.ai/v1alpha1
kind: LinuxServer
metadata:
  name: build-01
spec:
  harness:
    mode: auto            # auto (default) | none | explicit
```

- **`auto`** is the default and needs writing nowhere: the CRD default applies to
  every edge, including ones that existed before this feature. The agent offers
  every harness whose executable it finds on the runner account, re-checked on
  every start and every reconcile, so installing Claude Code on the host later
  is enough to make it available.
- **`none`** runs nothing. This is the opt-out.
- **`explicit`** offers exactly `spec.harness.enabled`, whether or not anything
  else is installed. A harness named here but not installed is reported
  `detected: false` rather than silently dropped, because "you asked for Codex
  and it is not on this machine" is more useful than an empty list.

Changing the field takes effect without restarting the agent. Switching to
`none` stops the children and keeps their state directories, so switching back
resumes the same sessions.

```console
$ kubectl patch linuxserver build-01 --type=merge -p '{"spec":{"harness":{"mode":"none"}}}'
```

In the portal, the edge page's **Harness** card is a switch plus a per-harness
checkbox list. It writes this field and nothing else: no verb, no runner token.

### What the machine reports

```console
$ kubectl get linuxserver build-01 -o jsonpath='{.status.harnesses}' | jq
[
  {"name":"claude","detected":true,"enabled":true,"ready":true,"version":"2.1.273","port":8787},
  {"name":"codex","detected":false,"enabled":false,"ready":false}
]
```

`detected` and `enabled` are separate facts. Together they distinguish a machine
that needs a binary installed from one whose owner switched the harness off.

### Install-time

`railgrid edge create --harness none` sets `spec.harness` on the object, so it is
an opt-out that sticks. `railgrid agent join --harness …` only seeds the agent's
local cache for the window before it has ever seen the edge object; the first
observed spec overwrites it. **Config wins over flags, always** — there is no
agent-side flag that can override the hub.

On Linux the harness child runs as a dedicated non-root account: `--runner-user`
names it, and when it is omitted the installer creates `railgrid-runner`. The
default path therefore never runs a harness as root and never refuses for want
of a flag. On macOS the LaunchDaemon's worker account is used.

## What the agent materializes

Per enabled harness, under `<runner-user home>/.railgrid/runner/<harness>/`,
mode `0700`:

| Path | Mode | Contents |
| --- | --- | --- |
| `token` | `0600` | The runner's bearer. Generated once, never rotated on re-reconcile, and it never leaves the host. |
| `runner.json` | `0600` | The enrollment. `runnerID` is `<edge>-<harness>`; the listener is pinned to `127.0.0.1:<port>`. |
| `claude-home/` or `codex-home/` | `0700` | The harness's dedicated home. Session state lives here; **no credential does.** |
| `state/` | `0700` | The runner's own journal and task worktrees. |
| `runner.log` | `0600` | The child's output, truncated. |

Ports are allocated from 8787 upward in a deterministic order, so a machine
offering both harnesses gets two runners without anybody assigning ports.

The child is the agent binary with a different subcommand
(`railgrid runner run --harness <name>`), so upgrading the agent upgrades the
harness supervisor and there is no second artifact to distribute.

## How the hub reaches it

The agent advertises each running runner over the discovery channel it already
uses for every other local service, and the edges provider publishes it as a
`Service` named `<edge>-<harness>`:

```console
$ kubectl get service build-01-claude -o yaml
spec:
  edgeRef: {kind: LinuxServer, name: build-01}
  host: 127.0.0.1
  type: runner
  scheme: http
  port: 8787
  auth: none          # the AGENT injects the runner bearer on the host side
status:
  URL: /clusters/<cluster>/apis/edges.railgrid.ai/v1alpha1/services/build-01-claude/proxy
  harness: {name: claude-code, version: "2.1.273", ready: true}
```

A hub-side caller reaches the protocol at that URL plus `/runner/v1/...`, with
the ordinary `services/{name}/proxy` data-plane verb and ordinary RBAC. The
bearer is added by the agent when it recognises one of its own runner ports, so
the credential never appears in the workspace at all — which is why the Service
carries `auth: none`.

`status.harness` is copied from the runner's own capabilities response, so a
portal or a consuming provider can pick a ready runner without holding a token.
`status.runner` is the other half of that document — the **machine**: the
toolchains the runner detected on its host (`git`, `node`, …), its environment
and verification capabilities, and its capacity. A consumer that must match a
job to a machine — Factory refuses to assign a job that requires `git` to a
runner that does not advertise it — reads it here and never probes the runner.

The toolchains are **detected, not declared**. A hand-written `runner.json`
used to list them; a supervised runner's config is written by nobody, so the
agent resolves a short, named table of executables (`pkg/agent/harnessplane`
`knownToolchains`) the way the runner itself resolves a harness — `PATH`, then
the well-known install directories a service account's `PATH` lacks — and
advertises what it found. A runner that clones with git while advertising no
toolchains was reporting a machine that could not do what it was doing.

A runner exposes **no MCP tools** deliberately: a caller speaks `runner/v1`, not
MCP.

## The caller's credential

Every `runner/v1` start and resume must carry one:

```json
{
  "harnessCredential": {"kind": "claude-oauth", "value": "sk-ant-oat01-…"}
}
```

Kinds are `claude-oauth` (a `claude setup-token` value, injected as
`CLAUDE_CODE_OAUTH_TOKEN`), `claude-apikey` (an Anthropic API key, injected as
`ANTHROPIC_API_KEY`) and `codex-auth` (a Codex `auth.json`, written into the
harness home for the duration of one launch and removed afterwards).

It is **dispatch data**, handled exactly like the short-lived Git clone token:

- stripped before the request is fingerprinted or persisted, so it never reaches
  the runner's durable journal and a retry carrying a freshly minted value is
  still the same request rather than an idempotency conflict;
- held in memory for the life of the attempt, which means a resume after a
  runner restart must present it again — the restart parks the attempt as
  `needs_input`, and the resume that revives it carries the credential;
- never logged, never put on a command line, never in an event, a blocker or an
  error message.

There is **no fallback**. A start without a credential is refused with
`invalid_request`, and a credential of the wrong kind for the harness is refused
rather than tried. A runner that could fall back to something on the host would
be a shared billable identity, which is the thing this design exists to avoid.

## A harness has two names

This trips people up, and it has already caused two bugs, so it is worth
stating plainly:

| | Value for Claude Code | Value for Codex |
| --- | --- | --- |
| **Selector** — what a person writes: `--harness`, `spec.harness.enabled`, the edge's `status.harnesses[].name` | `claude` | `codex` |
| **Advertised** — what the harness calls itself in the runner's capabilities answer, and therefore what the Service's `status.harness.name` shows and what `StartRequest.RequiredHarness` is matched against | `claude-code` | `codex` |

They differ only for Claude Code, which is exactly why it goes wrong: code that
assumes the two are equal works for Codex and silently fails for Claude. Both
failures have happened — once as "no Claude runner is ever ready", once as a
dispatch refused for a harness mismatch that did not exist.

`pkg/runner/harness` holds the mapping (`AdvertisedName`, `SelectorFor`) and a
test that checks it against what the adapters actually report, so a harness that
renames itself breaks a test rather than breaking dispatch. Use it rather than
writing the mapping again.

## One lifecycle, shared

Reaching a runner is `pkg/runner/client`. What happens between a dispatch and
its answer is `pkg/runner/dispatch`, and it exists because that part had been
written twice — once in the agents provider, once in Factory — and the copies
drifted: one learned that a terminal *event* can arrive before the terminal
*receipt*, the other still believed the event; one refused a receipt whose
session id had changed, the other knew a harness forks its session on a resume.

`dispatch` holds the rules and nothing else: it follows an attempt from a
cursor to the end or to a park, reads the harness's stream into text, tool
calls and cost, tells a **question** (answered with words) from a **permission
prompt** (answered with a verdict on a named call), reconciles a dropped cursor
through `inspect` instead of failing, and does not report a cancel until the
receipt says `cancelled`. It persists nothing, decides nothing about who may
answer a park, and holds no credential past the call it was handed. A product
plugs in an `Observer` for the stream and keeps what is its own — an agent's
transcript and inbox, Factory's approved envelope and delivery.

The agents provider's harness backend is now an adapter over it. Factory
follows.

## Two attempt shapes

`runner/v1` serves both a coding coordinator and a conversational agent:

| | Repository attempt | Workspace attempt |
| --- | --- | --- |
| Names | `repositoryID` + a full `baseCommit` | `workspaceID` |
| Working directory | a fresh task clone at the approved commit | a directory the runner keeps across attempts |
| Used by | Factory | a harness-backed Agent |

They are mutually exclusive: a workspace has no commit to verify and a
repository attempt has no directory to keep. A workspace attempt may also not
export a Git result or name a clone source.

A start may carry `sessionID` to continue a harness session an EARLIER attempt
created, which is how consecutive turns of one conversation stay one session.
Each turn is a new dispatch of the same task, so the attempt epoch increments:
a conversational caller maps session to `taskID`, run to `attemptID`, and turn
number to `attemptEpoch`. The harness may fork the session on resume; the real
session id is always on the receipt, so the next turn chains onto the one that
exists rather than one that does not.

## Threat notes

**Who can cause code execution on the machine.** Whoever can set
`spec.harness` to something other than `none`, plus whoever can `create` on
`services/{name}/proxy` in the workspace. The machine owner's control is the
`--harness none` they can set at `edge create`, the account they name with
`--runner-user`, and simply not installing a harness.

**What the child can reach.** Everything its account can reach on the
filesystem, its own loopback port, and — for Claude Code — the network. Codex
runs with `workspace-write` and network disabled; Claude Code exposes no
equivalent flag, so `--restricted` is not used because it would remove Bash and
make a coding runner useless. Project-controlled CODE is off either way
(`--strict-mcp-config`, `--setting-sources ""`, `--disable-slash-commands`), so
a project `.claude/settings.json` hook does not run and a project `.mcp.json`
server does not start.

What differs is whether anyone can be asked:

| | A turn nobody can answer | A turn that can ask |
| --- | --- | --- |
| Flags | `--permission-prompts none --safe-mode` | `--permission-prompts host` with the runner's own permission tool |
| A request the mode does not pre-approve | denied silently | put to a human |
| Project prompt TEXT (`./CLAUDE.md`, `.claude/agents`) | not read | read |

The second row is why the first is not simply safer. The third is a real trade
and it is forced: measured against Claude Code 2.1.281, `--safe-mode` disables
`--mcp-config` servers too, so the permission tool cannot be found and the two
cannot coexist. What is lost is prompt text rather than code — it can steer the
model, and every action it could steer the model into that the mode does not
pre-approve now stops at a human instead of being denied without anyone seeing
it. Choose the runner account accordingly.

**What the child cannot reach.** The agent's environment (the child's is built
from scratch: `HOME` and `PATH`, nothing else), the agent's credential files,
the reverse tunnel, and the hub. It has no kcp client and no token for one.

**The model credential.** It is the caller's, it arrives per attempt, and it
lives in the caller's workspace rather than on the machine. On the host it
exists only in the runner process's memory, and for Codex in one `0600` file
inside the harness home for the length of a single launch. Whoever can start an
attempt can spend that credential's money; whoever owns the machine cannot.

## Not done

- **`KubernetesCluster` edges.** A harness on a cluster edge needs a Deployment,
  not a supervised child, and that story is not written.
- **Per-harness resource limits.** No cgroup, no memory or CPU cap, no disk
  quota beyond the bounded log. A runaway harness is bounded only by the
  runner's own execution limits and cancellation.
- **A network sandbox for Claude Code.** Codex has one; Claude Code does not
  expose an equivalent.
- **Interactive login on the host.** There is no flow to drive
  `claude setup-token` or `codex login` on an edge, and there does not need to
  be: credentials belong to the caller now.
- **Selector-based placement.** An Agent or a Worker names one edge. There is no
  capacity-based scheduling across a pool of harness hosts, and a runner's
  capacity is still pinned to one attempt.
