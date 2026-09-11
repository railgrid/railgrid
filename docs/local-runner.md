# Local runner runbook

`faros-runner` is a single-execution runner for a host that is already enrolled
by its operator. It exposes the `runner/v1` protocol on a loopback-only HTTP
listener and uses a bearer token for every request. The default listener is
`127.0.0.1:8787`; the listener cannot be configured to a non-loopback address.

The runner is an explicit operations surface. It has no scheduler, automatic
cross-machine migration, Git publication, or deployment/publishing workflow.
The caller must provide an approved task envelope and must observe the durable
receipt and events for its outcome.

## Prepare the host

Run the binary as the dedicated non-root account that owns the runner state.
The command refuses to start as root. Build the runner for the host with:

```sh
make build-runner
```

For Darwin hosts, build both supported architectures and install the matching
binary:

```sh
make build-runner-darwin
# bin/faros-runner-darwin-arm64
# bin/faros-runner-darwin-amd64
```

For a disposable Mac acceptance check, place the matching binary under the name
`faros-runner` beside [setup-macos.sh](../hack/runner-acceptance/setup-macos.sh)
and run `sh setup-macos.sh`. It creates a tiny local Git source, token, and
configuration below `~/.faros-runner-preview`, then prints login and launch
commands. It requires Git, OpenSSL, Python 3, and Codex. It does not enroll a
Service, send credentials, or start a coding task. Its setup can be repeated
without replacing the token or resetting the fixture repository.

Install the Codex executable for this dedicated account. Create the runner's
Codex home as a new owner-only directory, then use the supported Codex login
flow with `CODEX_HOME` set to that directory. Do not copy an interactive
developer home into it. The runner's default expected Codex version is
`0.147.0`; override it only when the installed harness is intentionally pinned
to another version:

```sh
./bin/faros-runner --version-pin <codex-version> --help
```

The runner probes the executable and its app-server authentication state during
startup without making a model call. A failed version or authentication probe
leaves capabilities unready, so a start request cannot be accepted until the
host is repaired.

Generate one local bearer value, register that value with the tenant's existing
Service credential flow, and provide the same value to the runner through an
owner-only file. The tenant stores the Service credential in its auth Secret.
Do not put the token in the JSON configuration, a command-line argument, a
checked-in file, request instructions, or logs.

```sh
install -m 600 /path/to/locally-generated-token /path/to/runner-token
```

The exact Secret retrieval and mounting mechanism belongs to the tenant
deployment. The local binary only reads the token file and compares the
presented `Authorization: Bearer` value with it. The local runner has no
bootstrap endpoint for creating this credential.

## Enroll configuration

Use an absolute, private state directory and an absolute token-file path. A
configuration can enroll several named local Git sources, but each source must
be explicitly allowlisted. `baseCommit`, when set, further restricts that
source to one commit.

```json
{
  "protocolVersion": "runner/v1",
  "runnerID": "<runner-id>",
  "stateDir": "<absolute-private-state-directory>",
  "tokenFile": "<absolute-private-token-file>",
  "toolchains": ["<toolchain-name>"],
  "environment": ["<approved-environment-capability>"],
  "verificationCapabilities": ["<verification-capability>"],
  "maximumCapacity": 1,
  "repositories": {
    "<repository-id>": {
      "source": "<absolute-local-git-source>",
      "baseCommit": "<40-character-commit>"
    }
  },
  "resources": {
    "<resource-name>": {
      "kind": "<preconfigured-resource-kind>",
      "capacity": 1
    }
  }
}
```

`runnerID` and map keys use the identifier form accepted by the protocol. The
runner defaults `runnerID` to a platform-qualified value, `stateDir` to the
user configuration directory followed by `faros-runner`, and `maximumCapacity`
to one. It rejects a capacity greater than one. The source path is resolved at
startup; the source directory must exist and contain the requested full commit.

The runner stores durable state as a protected file below `stateDir`, takes a
single-process lock for that directory, and places task workspaces below
`stateDir/worktrees/<taskID>/<attemptID>`. Keep this directory on durable
storage on the same machine. Do not share one state directory between runner
instances.

## Start the runner

```sh
./bin/faros-runner \
  --config <absolute-runner-config.json> \
  --codex-binary <codex-executable>
```

The command also accepts `--state-dir`, `--listen`, `--token-file`, and
`--codex-home`. If `--codex-home` is omitted, it is
`<stateDir>/codex-home`. The Codex home is created with owner-only permissions
and must be dedicated to the runner account. The adapter rejects symlinks and
interactive configuration entries in that home. The CLI permits a bounded
`config.toml` containing only project `trust_level` records (`trusted` or
`untrusted`) for existing task/attempt directories under its managed
`stateDir/worktrees` root. Parent paths, symlinks, and additional settings are
rejected. When this configuration exists, a launch also rejects `.codex`
entries from the managed worktree root through the task checkout, preventing
project trust from enabling local configuration or hooks. Library consumers
must explicitly supply `codex.Config.WorktreeRoot` to enable this exception.
The runner preserves the trust file and existing authentication/session state.
Codex-owned `plugins` cache and staging directories may remain in the dedicated
home. Every app-server launch explicitly disables apps, plugins, and hooks,
including readiness probes and resumed sessions. A symlink or non-directory
`plugins` entry is rejected; the runner does not remove cache files.
It runs Codex with a sanitized
environment: the runner-owned home is used for `HOME`, `CODEX_HOME`, and XDG
directories, while GitHub tokens, API keys, SSH-agent settings, and global Git
configuration are removed.

These boundaries isolate configuration and task workspaces, not the operating
system account or all filesystem reads. Use an appropriate worker account or
host boundary for production execution.

Check readiness through an authenticated loopback request:

```text
GET http://127.0.0.1:8787/runner/v1/capabilities
Authorization: Bearer <runner-token>
```

The response reports the protocol version, runner identity, OS and
architecture, harness readiness and version, configured capabilities, capacity,
and readiness reasons. A readiness response does not mean a task has completed.

## Run an operation

All JSON requests use `protocolVersion: "runner/v1"`. Mutation requests carry a
`requestID`, `taskID`, `attemptID`, and positive `attemptEpoch`. A repeated
request with the same identity and content returns the durable prior result; a
reused request ID with different content is an idempotency conflict. An older
attempt epoch is stale and cannot mutate the newer attempt.

| Operation | Request |
| --- | --- |
| Discover | `GET /runner/v1/capabilities` |
| Start | `POST /runner/v1/attempts` |
| Inspect | `GET /runner/v1/attempts/<attempt-id>` |
| Events | `GET /runner/v1/attempts/<attempt-id>/events?after=<cursor>` |
| Cancel | `POST /runner/v1/attempts/<attempt-id>/cancel` |
| Resume | `POST /runner/v1/attempts/<attempt-id>/resume` |
| Artifact | `GET /runner/v1/attempts/<attempt-id>/artifacts/<artifact-id>` |

A start request must include the enrolled `repositoryID`, the full 40-character
`baseCommit`, non-empty instructions, and a non-empty JSON `approvedInput`
object containing `provenance`, `manualAuthorization`, or `authorization`.
The base commit must resolve exactly in the enrolled local source. The runner
clones that source with fixed, non-interactive Git settings into the task-owned
worktree and checks out the commit detached. It does not use `git worktree add`
and does not modify the enrolled source checkout.

The request may additionally require configured capabilities, toolchains,
environment entries, a named ready harness, verification names or commands,
named resources, execution limits, and logical artifact paths. Resource values
are names only. A configured resource reserves capacity; the runner does not
provision ports, containers, clusters, or other infrastructure.

For a local fixture, use a repository enrolled by absolute path and authorize
one explicit start request. Keep the commit equal to the enrolled commit:

```json
{
  "protocolVersion": "runner/v1",
  "requestID": "<request-id>",
  "taskID": "<task-id>",
  "attemptID": "<attempt-id>",
  "attemptEpoch": 1,
  "repositoryID": "<repository-id>",
  "baseCommit": "<same-40-character-commit-as-config>",
  "instructions": "<task instructions>",
  "approvedInput": {
    "manualAuthorization": {
      "operator": "<operator-reference>",
      "scope": "<approved-task-scope>"
    }
  },
  "limits": {"maxTurns": 1},
  "verification": {"names": ["<verification-name>"]}
}
```

This example is an authorization envelope for a local fixture. It does not
activate a scheduler, create a publication, or grant the harness credentials
to another system.

The Codex adapter starts one app-server process, one thread, and one turn per
execution. Its turn runs with workspace-write access restricted to the task
worktree and with network access disabled. The runner's single execution slot
and any configured resource reservations are released when the attempt reaches
a terminal phase.

## Observe, cancel, and resume

`Start` durably persists an accepted receipt before returning. Follow the
receipt with `Inspect` and replayable server-sent events. Events have ordered
cursors; a cursor gap or `cursor_expired` response requires inspecting the
receipt before continuing. Progress is an observation, not proof of completion.

Cancellation has two states. An explicit cancel request first returns
`cancelling`; it is complete only after the harness exits and the receipt or
event reports `cancelled`. That cancellation remains terminal across process
shutdown. A graceful shutdown drains active child processes and persists a
shutdown interruption as `needs_input`, retaining the recorded session and
worktree for same-session recovery. Adapter failures and approved output or
duration limits retain failure precedence when shutdown overlaps them.

Interactive approval, authentication, a missing Codex session, or another
operator decision moves an attempt to `needs_input`. Resume requires the same
attempt epoch, the same session ID and task worktree, and an explicit
resolution. It cannot amend the approved input or instructions. A missing or
unrecoverable checkpoint is a blocker; the runner never silently starts a new
session as a substitute.

## Restart and recovery

The state journal, task worktree, and Codex home are same-machine state. On
startup the runner loads the journal and verifies its harness without a model
call. It does not automatically rerun an in-flight attempt. Attempts that were
active when the process stopped are reported as `needs_input` with a restart
reconciliation blocker. Inspect the receipt, verify that the enrolled source,
approved commit, worktree, and session still exist, then resume only when the
same session can be recovered. If that evidence is unavailable, leave the
attempt in `needs_input` with its blocker instead of claiming recovery.

When loading a v1 journal, the runner upgrades it to v2. The migration reopens
only the old shutdown receipt that was recorded as `cancelled` with the exact
legacy shutdown blocker, a valid session ID, and no `CancelPending`, cancel
operation, durable error, or limit-exceeded marker. It changes that receipt to
`needs_input` and records no execution; all other cancelled receipts remain
terminal. A v1 runner rejects the upgraded v2 journal, so do not downgrade the
binary over an upgraded state directory.

Do not copy the state directory to another machine or assume raw Codex session
files are portable. Cross-machine continuation, migration, scheduling, and
publication are outside this runner contract.

## Limits and verification boundary

The default bounds are 256 retained events per attempt, 64 KiB per event
payload, 2 MiB per JSON request body, and 32 MiB per artifact. Configuration
can lower or raise these limits within the implementation's accepted values;
callers should use the advertised protocol response and error code as the
authority. The runner supports at most one harness turn per execution and one
simultaneous execution by default.

Focused runner tests, race and vet checks, native builds, Darwin arm64 and
amd64 builds, and two no-model Codex `0.147.0` authentication probes have
passed for the current implementation. These checks establish protocol,
process, and harness contracts. They do not establish that a real Mac has
successfully completed a coding task. A real-Mac acceptance still needs an
actual enrolled host, approved local repository, start/observe path,
interruption, restart, and same-session resume evidence.
