# Edge add-ons

An **add-on** is a managed service that the railgrid agent runs on the host next
to itself. The first one is the **coding runner**: the loopback
`runner/v1` service described in [local-runner.md](local-runner.md), but
declared in the tenant's workspace instead of installed by hand.

The API is `edges.railgrid.ai/v1alpha1` kind `Addon`, cluster-scoped like
`Service`.

## The model: declare → materialize → report

Add-ons use the loop the workload plane already uses (`Workload` → `Placement`
→ agent), not an imperative channel:

1. A tenant **declares** an `Addon` in their workspace. Nothing happens on any
   machine yet.
2. The agent on the referenced edge **materializes** it: the add-on manager
   (`pkg/agent/addons`) watches `Addon` objects, keeps the ones whose
   `spec.edgeRef` names this edge, and supervises a child process for each.
3. The agent **reports** on `status`: `observedGeneration`, a phase, and the
   conditions `Allowed`, `Configured` and `Running`.
4. The edges provider's add-on controller (`providers/edges/internal/addonctrl`)
   watches the same objects and, once the agent has reported `Allowed=True` and
   published the add-on's token Secret, derives the `Service` through which the
   add-on is reachable and sets `Published`.

There is no way for the hub to tell a host to run something. An agent that
disagrees with an `Addon` simply says so and does nothing.

## The two-key trust model

Creating an `Addon` is privileged: it asks a specific machine to become a host
for arbitrary code execution. It takes **two independent keys**, and neither one
alone runs anything.

| Key | Held by | How it is exercised |
| --- | --- | --- |
| The `Addon` object | A tenant with `create` on `addons` in the workspace | `kubectl apply` |
| The local opt-in | Whoever installed the agent on the machine | `--allow-addon=runner --addon-user=<account>` at install time |

- Without the object, the agent has nothing to materialize.
- Without `--allow-addon`, the agent refuses: `Allowed=False`, reason
  `NotAllowedOnEdge`, with a message naming the flag. **No process is started and
  no Secret is written.** The provider therefore never publishes a `Service`:
  hub-side intent on its own produces no reachable endpoint anywhere.

The machine's advertised opt-in is visible before anyone writes an `Addon`. The
agent's heartbeat publishes it on the edge:

```console
$ kubectl get linuxserver build-01 -o jsonpath='{.status.allowedAddons}'
["runner"]
```

An empty list means the machine accepts no add-on. The list is republished on
every heartbeat, so dropping `--allow-addon` and restarting the agent clears it.

### Credential exchange

Scoped edge identities have no core Secret or Namespace permissions. The agent
uses the provider-declared `addon-credentials` verb on its own LinuxServer or
MacOSServer. The provider checks both ordinary data-plane gates, verifies the
Addon belongs to that edge, reads only its configured harness auth reference,
and publishes only `default/<addon>-runner-token`, pinned to the Addon's UID.
The auth Secret must carry `railgrid.ai/owner: edges`, explicitly placing it
within the provider's existing label-scoped permission claim. Only the active
harness's credential keys are returned. A token Secret with another owner is
never overwritten.

Older saved enrollment bundles discover the new route by refreshing through
`agent-token`. Upgrade the provider and agent together; older agents still
attempt direct Secret access and receive a Forbidden error.

### RBAC: who may create an Addon

Creating an `Addon` is **not** granted implicitly anywhere.

- **Workspace admins** already hold `cluster-admin` in their workspace
  (`pkg/hub/kcp/bootstrap.go`, `ensureWorkspaceAdmin`), so the wildcard covers
  `addons`. No new default role was added.
- **The edges provider's** Enable-time grant
  (`EnsureProviderEdgeProxyGrant`) enumerates its resources explicitly and does
  not include `addons`.
- **The edge agent** gets `get/list/watch` on `addons` and
  `get/update/patch` on `addons/status` — and nothing else
  (`desiredAgentRules()` in `providers/edges/internal/edgectrl/rbac_reconciler.go`).
  An agent that could create an `Addon` could enrol itself, which would make the
  tenant's key meaningless.
- **MCPServer tokens** are explicitly refused. The generated MCPServer role
  normally grants read+write on every resource the tenant has bound, and widens
  itself as a provider's APIExport grows; `addons` is on a deny list
  (`privilegedResources` in `pkg/hub/controllers/mcpserver/rbac.go`) so no AI
  client can turn a machine into a code-execution host as a side effect of a
  tool call. There is no MCP tool for add-ons.

To give a **non-admin** the ability to create add-ons, grant exactly this in the
tenant workspace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: railgrid:edge-addon-operator
rules:
  # The Addon itself. "create" is the privileged verb here.
  - apiGroups: ["edges.railgrid.ai"]
    resources: ["addons"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["edges.railgrid.ai"]
    resources: ["addons/status"]
    verbs: ["get"]
  # The Codex session Secret the runner add-on reads. Scope it by name if you
  # can; the agent only ever reads the one named in spec.runner.codex.authSecretRef.
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "create", "update", "patch"]
  # Reading the edge is needed to see status.allowedAddons before declaring.
  - apiGroups: ["edges.railgrid.ai"]
    resources: ["linuxservers", "macosservers"]
    verbs: ["get", "list", "watch"]
  # The derived Service is created by the provider, but the operator needs to
  # see it to find the name a Factory Worker enrols.
  - apiGroups: ["edges.railgrid.ai"]
    resources: ["services"]
    verbs: ["get", "list", "watch"]
```

Bind it with a `ClusterRoleBinding` in the tenant workspace. Note that the
holder can also make the machine stop hosting an add-on (`delete`), which is the
correct pairing: whoever may turn it on may turn it off.

## Threat notes

**A root agent launching a non-root code-execution child.** On Linux the agent
runs as root (the systemd unit has no `User=`). The add-on child never does:

- `--addon-user` is **required** whenever `--allow-addon` is set and the agent
  is root. `railgrid agent install` / `agent join` refuse at install time; the
  agent refuses at startup.
- The account is resolved through `pkg/util/localuser`, which rejects uid 0, the
  name `root`, and a home that is relative or `/`.
- The supervisor sets `SysProcAttr.Credential` to that uid/gid plus `Setpgid`,
  and refuses a uid-0 or gid-0 target at construction
  (`pkg/agent/addons/supervisor`).
- On macOS the LaunchDaemon already runs as a non-root worker account, so
  `--addon-user` is neither required nor accepted there; the child runs as that
  same worker.

**What the child can reach.**

- Everything the add-on account can reach on the filesystem, including the
  runner's own clones and any enrolled checkout. The add-on's own state
  directory is `0700` and its
  files `0600`, and every write is symlink-hardened (`pkg/util/safeio`) so the
  add-on account cannot redirect a root-side write by pointing `~/.railgrid`
  somewhere else.
- Its own loopback port. The runner refuses a non-loopback listener.
- Whatever the Codex harness is allowed: workspace-write restricted to the task
  worktree, network disabled, sanitized environment. See
  [local-runner.md](local-runner.md).

**What the child cannot reach.**

- The agent's environment. The child's environment is built **from scratch** —
  `HOME` and `PATH`, nothing else. The hub bearer token, the path to the agent
  kubeconfig, API keys and SSH-agent sockets present in the agent's environment
  are not inherited. (The Codex adapter's `safeEnv` subtracts from
  `os.Environ()`; the supervisor adds to an empty slice, which is strictly
  stronger.)
- The agent's credential files. The agent kubeconfig lives under the AGENT's
  home with `0600`; the add-on account is a different user.
- The tunnel. The child speaks only to its own loopback listener; the reverse
  tunnel is the agent's socket, not the child's.
- The hub. The child has no kcp client and no token for one.

**The model credential is only as protected as the Secret.** This is worth
stating plainly, because the Claude Code harness makes it concrete: an
`oauthToken` or `apiKey` in a tenant Secret is readable by **anyone who can read
Secrets in that workspace** and by **the edge agent's ServiceAccount**, which
has core `secrets get` (it has to, in order to materialize the credential). It
then sits in a `0600` file on the edge host, readable by the add-on account and
by root.

Consequences to weigh before creating one:

- The credential is a **billable, tenant-wide identity**. Whoever can read the
  Secret can use it outside railgrid entirely.
- An `apiKey` is typically long-lived and broadly scoped; an `oauthToken` from
  `claude setup-token` is tied to a subscription. Prefer whichever your
  organization can rotate and revoke fastest, and scope the RBAC on that Secret
  by name (see the ClusterRole above).
- The agent never logs it, never puts it on a command line, and the adapter
  redacts it from every event, blocker and error before it leaves the host. That
  protects the transcript, not the Secret.
- Rotating the Secret is picked up within a resync and restarts the child; the
  old value is overwritten on disk. Revoking at the provider is still the
  authoritative action.

Codex is not exempt — a login session is a credential too — but it is a session
file rather than a bearer value, which is the difference discussed under
"Credential models" above.

**Residual risks, stated plainly.** The add-on account is an ordinary local
account: it can read world-readable files on the host, and there is no cgroup or
resource limit on the child today (see *Not done*). Anyone who can create an
`Addon` can cause code execution on the allowed machine — that is what the
feature is. The machine owner's control is the `--allow-addon` flag and the
account they name.

**The Claude Code harness has no network or filesystem sandbox.** The Codex
adapter runs its turn with `workspace-write` access and network disabled.
Claude Code offers no equivalent flag: `--restricted` would remove Bash along
with network-capable tools, which makes a coding runner useless, so it is not
used. File tools are confined to the task worktree (no `--add-dir` is passed),
but Bash can reach anything the add-on account can, and the turn has network
access. Every project-controlled *code and configuration* path is still off
(`--safe-mode`, `--strict-mcp-config`, `--setting-sources ""`,
`--disable-slash-commands`, `--permission-prompts none`). Choose the add-on
account accordingly; see docs/local-runner.md for the flag-by-flag list.

## The runner add-on

`spec.type: runner` supervises `<agent binary> runner run …` — the same
behaviour as the standalone `railgrid-runner` binary (both call
`pkg/runner/runnercli`).

### What the agent materializes

Under `<addon-user home>/.railgrid/addons/runner/<addon-name>/` (mode `0700`):

| Path | Mode | Contents |
| --- | --- | --- |
| `token` | `0600` | 32 random bytes, hex. Generated once; **never** rotated on re-reconcile. |
| `runner.json` | `0600` | The enrollment rendered from `spec.runner`. `runnerID` is `<edge>-<addon>`; `listen` is pinned to `127.0.0.1:<port>`. |
| `codex-home/auth.json` | `0600` | The Codex login session, copied from the referenced Secret. Removed when the harness is `claude`. |
| `codex-home/` | `0700` | The runner's dedicated `CODEX_HOME`. Kept across a harness switch. |
| `claude-credential` | `0600` | The Claude Code credential value. Removed when the harness is `codex`. |
| `claude-home/` | `0700` | The runner's dedicated `CLAUDE_CONFIG_DIR`. Kept across a harness switch. |
| `state/` | `0700` | The runner's own state journal and task worktrees. |
| `runner.log` | `0600` | The child's stdout and stderr, truncated at 8 MiB. |

### How the two credentials cross the boundary

Neither Secret is touched by the agent. An edge agent's scoped identity holds
**no core group at all** — the hub's identity policy mints no `secrets` rule
for anyone — so both directions run through declared, gated data-plane verbs on
the agent's own edge, and the **provider** performs the read and the write. See
[edges-agent-credentials.md](./edges-agent-credentials.md) §"A managed runner's
credential".

- **The harness credential** (`spec.runner.{codex,claude}.authSecretRef`): the
  agent POSTs `{"addon": "<name>", "authSecretRef": {…}}` to `{resource}/addon-credentials` on every
  reconcile, never caching. The provider confirms the Addon is hosted on the
  calling edge, reads the Secret **the Addon's own spec references**, and
  returns only the keys that harness can use. The agent never names a Secret.
- **The runner's bearer**: the agent POSTs
  `{"addon": "<name>", "uid": "<addon uid>", "token": "…"}` to the same
  `{resource}/addon-credentials` verb. The provider writes Secret
  `<addon-name>-runner-token` (key `token`) in namespace `default` of the
  tenant workspace, labelled `railgrid.ai/owner: edges` and carrying an
  `ownerReference` to the `Addon`, so deleting the `Addon` garbage-collects it.

Both Secrets must carry `railgrid.ai/owner: edges`, including the one a tenant
(or a portal) hand-writes for the harness credential: the edges provider's
`secrets` permission claim is scoped to that label, and an unlabelled Secret is
not merely unreadable — kcp's APIExport virtual workspace filters it out of
LIST/WATCH and answers a GET with `404`, so it does not exist as far as the
provider is concerned.

Health is a real probe, not "a process exists": the agent GETs
`http://127.0.0.1:<port>/runner/v1/capabilities` with the bearer token and sets
`Running=True` only when the answer reports protocol `runner/v1`. The reported
`version` is copied into `status.version`.

### Choosing a harness

`spec.runner.harness` selects the coding harness: `codex` (the default) or
`claude` for headless Claude Code. One runner process serves exactly one
harness — the capabilities response advertises a single entry, and a
coordinator dispatching work needs to know what it will get without
negotiating.

The block for the selected harness is required and the other one is **rejected**
(a CEL rule), so intent is never ambiguous: the two blocks reference different
credentials, and silently ignoring one would be the worst outcome.

Switching `spec.runner.harness` on an existing Addon restarts the child cleanly
and removes the other harness's credential from the host. Both harness homes
are kept, so switching back resumes with the session state that was there.

### Credential models: Codex vs Claude Code

They differ, and the difference is not cosmetic.

| | Codex | Claude Code |
| --- | --- | --- |
| Secret keys | `auth.json` | exactly one of `oauthToken` or `apiKey` |
| What the Secret holds | a **login session file** | a **bearer value** |
| How the harness receives it | written to `codex-home/auth.json` (0600) | injected as ONE environment variable into the harness child |
| Environment variable | none — API-key vars are stripped | `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` |
| Refusal reasons | `CodexAuthMissing` | `ClaudeAuthMissing`, `ClaudeAuthInvalid` |

**Why Claude Code uses an env-injected token.** Codex keeps a login session as
a file that can be produced once and copied to the machine that needs it.
Claude Code does not: its interactive login state is not a portable on-disk
artifact — on macOS it lives in the **keychain**, which is per-machine and
per-user and cannot be shipped to an edge. The supported headless path is
therefore a value handed to the process through the environment, produced by
`claude setup-token` (a long-lived OAuth token tied to a Claude subscription) or
an ordinary Anthropic API key. The agent materializes that value into a
runner-owned `0600` file and the adapter injects it into the harness child
alone; it never appears in an event, a blocker, a log line, a command line or
the capabilities response.

Setting **both** keys is rejected (`ClaudeAuthInvalid`) rather than resolved by
precedence: the two are different identities with different billing and
different scope, and which one the tenant meant is not a guess the agent will
make.

### Codex authentication

`spec.runner.codex.authSecretRef` names a Secret whose **`auth.json`** key holds
the Codex login session file. Without it the agent starts nothing and reports
`Configured=False` with reason `CodexAuthMissing`.

There is deliberately **no API-key field** on the Codex block. The Codex adapter
strips `OPENAI_API_KEY`, `CODEX_API_KEY` and `CHATGPT_API_KEY` from the harness
environment (`blockedEnvKey` in `pkg/runner/harness/codex/codex.go`), so a key
placed in the API could never be used — it would only be a credential sitting in
the tenant's API server. Do not add one. (Claude Code is the opposite case: an
environment variable is the *only* supported channel, which is why its block has
one.)

### Claude Code authentication

`spec.runner.claude.authSecretRef` names a Secret holding **exactly one** of:

- `oauthToken` — from `claude setup-token`, injected as `CLAUDE_CODE_OAUTH_TOKEN`
- `apiKey` — an Anthropic API key, injected as `ANTHROPIC_API_KEY`

Neither key present is `ClaudeAuthMissing`; both present, or a value with
embedded whitespace, is `ClaudeAuthInvalid`. In either case **nothing is
started and no token Secret is published**, so the provider never publishes a
Service for a runner that could not authenticate.

Rotating the Secret is picked up on the next resync (within a minute): the agent
re-reads it every reconcile and restarts the child only when the credential's
content actually changed. The same is true of the Codex `auth.json`.

To produce the session: on a machine you control, create a fresh owner-only
directory, run the supported Codex login flow with `CODEX_HOME` set to it, and
put the resulting `auth.json` into the Secret. Do not copy an interactive
developer home.

### Full example

```yaml
# The Codex login session. Keep it in the same workspace as the Addon.
apiVersion: v1
kind: Secret
metadata:
  name: codex-auth
  namespace: default
type: Opaque
stringData:
  auth.json: |
    { "…": "contents of the CODEX_HOME/auth.json produced by codex login" }
---
apiVersion: edges.railgrid.ai/v1alpha1
kind: Addon
metadata:
  name: code
spec:
  edgeRef:
    kind: LinuxServer
    name: build-01
  type: runner
  paused: false
  runner:
    port: 8787
    maximumCapacity: 1
    toolchains: ["go1.26", "node22"]
    verificationCapabilities: ["unit", "lint"]
    # No `repositories`. This is the normal shape: the control plane hands the
    # runner a clone URL and a short-lived credential with each attempt, and
    # the runner keeps its own clone under its state directory. Nothing has to
    # exist on the machine first. See "Enrolling a repository" below for the
    # exceptions.
    codex:
      binary: codex
      versionPin: "0.147.0"
      authSecretRef:
        name: codex-auth
        namespace: default
```

```console
$ kubectl get addon code
NAME   TYPE     HARNESS   EDGE       PHASE     SERVICE   AGE
code   runner   codex     build-01   Running   code      2m
```

### Full example — Claude Code

```yaml
# Exactly ONE of oauthToken or apiKey. Produce the token on a machine you
# control with `claude setup-token`; there is no portable login file to copy.
apiVersion: v1
kind: Secret
metadata:
  name: claude-auth
  namespace: default
type: Opaque
stringData:
  oauthToken: "sk-ant-oat01-…"
---
apiVersion: edges.railgrid.ai/v1alpha1
kind: Addon
metadata:
  name: code
spec:
  edgeRef:
    kind: LinuxServer
    name: build-01
  type: runner
  runner:
    harness: claude
    port: 8787
    maximumCapacity: 1
    toolchains: ["go1.26", "node22"]
    claude:
      binary: claude
      model: sonnet
      # versionPin is optional and unset by default: Claude Code self-updates,
      # so pinning it here would make the runner unready after every release.
      authSecretRef:
        name: claude-auth
        namespace: default
```

```console
$ kubectl get addon code
NAME   TYPE     HARNESS   EDGE       PHASE     SERVICE   AGE
code   runner   claude    build-01   Running   code      2m

$ kubectl get addon code -o jsonpath='{.status.harness}'
{"name":"claude-code","version":"2.1.273","ready":true}
```

### Enrolling a repository

Usually you do not. `spec.runner.repositories` is empty in both examples above
and that is the intended configuration: the control plane hands the runner a
clone URL and a short-lived credential with each attempt, and the runner keeps
its own clone under its state directory. A managed runner is never blocked on
a checkout somebody has to put on the machine first, and creating one asks for
no repository and no path.

Enrol an entry only for the two exceptions:

- **A checkout the machine owner already maintains.** Set `source` to its
  absolute path. The runner treats it as read-only — attempts are served from
  task-owned clones, and its branch and working tree never move.
- **A fixed remote the runner may fetch from without being told per attempt.**
  Set `fetchRemoteURL` and leave `source` out; the runner clones it itself.

Either half is enough, and both together are allowed — `source` as the local
checkout, `fetchRemoteURL` as the origin to fetch a missing approved commit
from. An entry that names neither is rejected by admission, and again by the
agent before it writes `runner.json`: it would give the runner nothing to check
out and nowhere to fetch from.

```yaml
  runner:
    repositories:
      # Cloned and fetched by the runner itself; nothing on the host.
      app:
        fetchRemoteURL: ssh://git@github.com/acme/app.git
      # A checkout the machine owner maintains, with an origin to top it up.
      vendored:
        source: /srv/repos/vendored
        fetchRemoteURL: ssh://git@github.com/acme/vendored.git
```

### Field reference

| Field | Default | Notes |
| --- | --- | --- |
| `spec.edgeRef.kind` | `LinuxServer` | `LinuxServer` or `MacOSServer`. `KubernetesCluster` is in the enum but rejected by validation. |
| `spec.edgeRef.name` | — | The connectable's `metadata.name`. |
| `spec.type` | `runner` | Only `runner` today. |
| `spec.paused` | `false` | Stops the child; keeps state and repositories. |
| `spec.runner.port` | `8787` | Loopback only; the host is not configurable. |
| `spec.runner.maximumCapacity` | `1` | Must be `1`; the runner is single-execution. |
| `spec.runner.toolchains` | — | Advertised names. Declaring one does not install it. |
| `spec.runner.verificationCapabilities` | — | Advertised names. |
| `spec.runner.repositories` | — | Normally absent. The runner clones what it is sent per attempt; enrol an entry only for the exceptions below. |
| `spec.runner.repositories[id].source` | — | Optional. Absolute path to a checkout that already exists on the edge host. A missing approved commit is fetched into it from its own `origin` with the add-on account's Git credentials; its branch and working tree never move. Omit it and the runner clones the repository itself, under its own state directory. |
| `spec.runner.repositories[id].fetchRemoteURL` | — | Absolute path, `file://`, `https://`, `ssh://`, or `user@host:path`. Where the runner's own clone comes from when there is no `source`, and where a missing commit is fetched from when a `source` has no usable origin. Operator-only; a start request cannot supply it. An entry must carry a `source`, a `fetchRemoteURL`, or both. |
| `spec.runner.harness` | `codex` | `codex` or `claude`. The other harness's block is rejected. |
| `spec.runner.codex.binary` | `codex` | Looked up on the child's `PATH`. |
| `spec.runner.codex.versionPin` | `0.147.0` | Probed at startup. |
| `spec.runner.codex.authSecretRef` | — | Required for `harness: codex`; Secret key `auth.json`. |
| `spec.runner.claude.binary` | `claude` | Looked up on the child's `PATH`. |
| `spec.runner.claude.versionPin` | unset | No default: Claude Code self-updates. Set one to have it checked. |
| `spec.runner.claude.model` | account default | e.g. `sonnet`, or a full model name. |
| `spec.runner.claude.authSecretRef` | — | Required for `harness: claude`; exactly one of `oauthToken` / `apiKey`. |

### Status

`status.phase` is one of `Pending`, `Installing`, `Running`, `Degraded`,
`Blocked`, `Paused`. The conditions say why:

| Condition | Owner | False means |
| --- | --- | --- |
| `Allowed` | agent | `NotAllowedOnEdge` — the machine owner did not pass `--allow-addon`. Nothing was created. |
| `Configured` | agent | `CodexAuthMissing` — the Secret is absent or has no `auth.json`. `ClaudeAuthMissing` — the Secret is absent or carries neither credential key. `ClaudeAuthInvalid` — it carries both, or an unusable value. `ConfigError` — the spec cannot be rendered. In every case nothing was started. |
| `Running` | agent | `Starting` — the child is up but not answering yet. `ProbeFailed` — the child is not up. |
| `Published` | provider | `NotAllowedYet` / `TokenSecretMissing` — the provider is waiting for the agent. `UnsupportedEdgeKind` / `EdgeNotFound` — it will never publish. |

`status.version` is the RUNNER build (which is the agent build).
`status.harness` is the coding harness the runner reported — `{name, version,
ready, reasons}`, copied from its capabilities response — so a portal can show
"claude-code 2.1.273, ready" without holding a runner bearer token. A runner can
be `Running` while its harness is `ready: false`: it answers the protocol and
refuses every attempt, and `reasons` says why.

## How Factory consumes it

The provider publishes an edges `Service` named **exactly the `Addon`'s name**,
owned by the `Addon`:

```yaml
spec:
  edgeRef: {kind: LinuxServer, name: build-01}
  host: 127.0.0.1
  type: generic
  scheme: http
  port: 8787
  auth: secret
  authSecretRef: {name: code-runner-token, namespace: default}
```

That Service name is what a Factory Worker enrols — it is the address through
which the runner protocol is reached over the hub's Service proxy, exactly as a
hand-written `Service` pointing at a hand-installed runner would be (the shape
is the one exercised by `test/e2e/suites/edgesconn`). The difference is that
nobody created the Service, the token or the `runner.json` by hand.

`status.serviceRef.name` on the `Addon` is the same name, so a Worker can be
pointed at an `Addon` and resolve the Service from it. **No Factory changes live
in this repository.**

## Adding a second add-on type

Four steps, in this order:

(A new *harness* for the existing runner add-on is a smaller change than a new
add-on type: implement `harness.Adapter` under `pkg/runner/harness/`, add a
`--harness` value in `pkg/runner/runnercli`, add the enum value and its spec
block to `AddonRunnerSpec` with the paired CEL rules, and teach
`materializeHarness` in `pkg/agent/addons/runner.go` how to put its credential
on disk. The Claude Code harness is the worked example.)

1. **Implement** `addons.Addon` in `pkg/agent/addons` — `Type()`,
   `Reconcile(ctx, spec) (Status, error)`, `Stop(ctx)`. Use
   `pkg/agent/addons/supervisor` for the child process and `pkg/util/safeio` for
   the state directory. `Stop` must keep durable state and must never delete
   user data.
2. **Register** a `Factory` for it on the manager, next to
   `manager.Register(addons.TypeRunner, …)` in `pkg/agent/addonplane.go`, and add
   the type to `addons.KnownTypes` so `--allow-addon` accepts it.
3. **Extend the API**: add the value to the `AddonType` enum in
   `providers/edges/apis/v1alpha1/types_addon.go`, add its `spec.<type>` block
   with a CEL rule requiring it, and run `make codegen-edges-provider`.
4. **Hook the provider** if — and only if — the add-on has a hub-facing
   endpoint: add a branch in `providers/edges/internal/addonctrl` beside the
   runner one. A type with no endpoint needs no branch; the controller leaves
   its status alone.

Nothing else has to change. The manager's opt-in gate, status reporting,
supervision and the RBAC verbs are type-agnostic.

## Upgrade and rollback

The add-on child **is the agent binary** (`os.Executable()` plus a different
subcommand), so:

- Upgrading the agent upgrades the add-on. There is no separate artifact to
  distribute, verify or version-skew against.
- The supervisor re-resolves the executable path through symlinks, so an
  in-place replacement is picked up on the next child restart.
- Restarting the agent stops every add-on child (whole process group, SIGTERM
  then SIGKILL after a grace period) and the new agent starts them again on its
  first reconcile.

**What happens to an in-flight attempt.** The runner persists its receipt before
it returns, and a graceful shutdown drains child processes and records the
interruption as `needs_input` with a restart-reconciliation blocker. After the
upgrade the same attempt is therefore **not** silently re-run: it is reported as
`needs_input`, and a coordinator must inspect the receipt and explicitly resume
if the session and worktree still exist. See "Restart and recovery" in
[local-runner.md](local-runner.md). Drain the Worker in its coordinating
provider before upgrading if you care about the attempt.

**Rollback** is an agent rollback. The runner's state journal is versioned and a
v1 runner rejects a v2 journal, so do not roll the agent back across a runner
state-format change over an existing add-on state directory. The token,
`runner.json` and Codex home survive both directions; only the executable moves.

Deleting or pausing an `Addon` stops the child and keeps the state directory —
including the token and the runner's own clones, so unpausing resumes the same
identity — and **never** touches an enrolled checkout.

## Not done (follow-ups)

- **`KubernetesCluster` edges.** Rejected by validation. The enum keeps the
  value so supporting them is a rule change, not a schema migration; it needs a
  cluster-side materialization story (a Deployment, presumably) that this design
  does not have.
- **Interactive `codex login` / `claude setup-token`.** Both credentials have to
  be produced on a machine you control and put in a Secret. There is no flow to
  drive a login on the edge host.
- **A network sandbox for the Claude Code harness.** See the threat note; Codex
  has one, Claude Code does not expose an equivalent.
- **A turn limit for the Claude Code harness.** This Claude Code release has no
  `--max-turns`, so only the runner's execution limits and cancellation bound a
  turn.
- **More than one harness per runner.** `spec.runner.harness` selects one. Two
  harnesses on one edge means two `Addon`s on different ports.
- **Per-add-on resource limits.** No cgroup, no memory or CPU cap, no disk quota
  on the state directory beyond the bounded log file. A runaway harness is
  bounded only by the runner's own execution limits.
- **More than one add-on type.** The interface and the provider hook exist; only
  `runner` is implemented.
- **Multiple runners per edge.** Nothing prevents two `Addon`s on one edge, but
  they must be given different ports by hand; there is no port allocator.
