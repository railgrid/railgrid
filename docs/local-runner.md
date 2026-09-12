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
source to one commit. An optional operator-only `fetchRemoteURL` permits the
runner to fetch a missing approved commit into the isolated task clone.

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
startup and the source directory must exist. Without `fetchRemoteURL`, the
requested full commit must already exist in that source. With it, the runner
keeps the source as the enrolled local checkout and may fetch the exact
requested commit only into the task-owned clone; it never updates the source.

The runner stores durable state as a protected file below `stateDir`, takes a
single-process lock for that directory, and places task workspaces below
`stateDir/worktrees/<taskID>/<attemptID>`. Keep this directory on durable
storage on the same machine. Do not share one state directory between runner
instances.

### Allow an enrolled repository to fetch a missing commit

Set `fetchRemoteURL` on the repository enrollment when the local `source` may
not contain every approved commit. This field is operator configuration; a
start request cannot supply or change it. The existing `baseCommit` pin is
still enforced when it is set, and every start request must still name one
full 40-character commit.

The remote can be a local path, a `file://` URL, public `https://`, or an
ordinary SSH URL (`ssh://...` or `user@host:path`). Embedded credentials,
HTTPS query or fragment options, and interactive HTTPS authentication are not
allowed. For example:

```json
{
  "repositories": {
    "app": {
      "source": "/srv/repos/app",
      "baseCommit": "<40-character-commit>",
      "fetchRemoteURL": "ssh://git@example.com/acme/app.git"
    }
  }
}
```

Before enrolling an SSH remote, the operator should test access and the
expected host key with the same key and `known_hosts` configuration. Fetch
uses strict host-key checking, batch mode, and disables agent forwarding and
other forwarding. It may use an operator-provided SSH key or agent for this
fetch operation only. The coding harness does not receive the SSH agent,
GitHub tokens, API keys, or other Git credentials, and its execution remains
network-disabled.

The runner advertises `git-fetch-v1` in `verificationCapabilities` when any
enrolled repository has a `fetchRemoteURL`. The capability is an availability
signal; the per-repository opt-in and remote validation still apply. Git fetch
command failures return fixed bounded messages (`git fetch failed`, `git fetch
timed out`, or `git fetch canceled`) instead of remote command output.

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
The approved commit must resolve exactly in the enrolled source or, when that
repository has opted in with `fetchRemoteURL`, be fetched exactly into the
isolated task clone. The runner clones the local source with fixed,
non-interactive Git settings into the task-owned worktree, performs that
operator-configured fetch only if the clone lacks the approved commit, and
checks out the commit detached. It does not use `git worktree add` and does
not modify the enrolled source checkout.

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

For an active attempt, an explicit cancel request first returns
`cancelling`; it is complete only after the harness exits and the receipt or
event reports `cancelled`. That cancellation remains terminal across process
shutdown. A graceful shutdown drains active child processes and persists a
shutdown interruption as `needs_input`, retaining the recorded session and
worktree for same-session recovery. Adapter failures and approved output or
duration limits retain failure precedence when shutdown overlaps them.

Runners advertising `cancel-unseen-v1` also accept cancellation for an attempt
not present in their journal. They persist a terminal cancellation fence before
returning `cancelled`; the record has no accepted execution, worktree, or session.
A delayed start for that attempt ID is rejected, including after restart. The
cancel identity must match the task and epoch on replay; stale or foreign
identities remain errors. A persistence failure never acknowledges cancellation.
Operators may use the authenticated cancellation endpoint to retire a dispatched
request with an unknown outcome. Coordinators must validate the returned identity
and persist the terminal receipt before releasing their own reservations.

Interactive approval, authentication, a missing Codex session, or another
operator decision moves an attempt to `needs_input`. Resume requires the same
attempt epoch, the same session ID and task worktree, and an explicit
resolution. It cannot amend the approved input or instructions. A missing or
unrecoverable checkpoint is a blocker; the runner never silently starts a new
session as a substitute.

A genuine harness `request-user-input` interaction may additionally populate
`receipt.clarification` with a bounded `{id,text}` value and emit a matching
`needs_input` event. The Codex adapter derives a stable ID from the session,
turn, and interaction item, preserves the bounded question and options, and
rejects secret, malformed, empty, or oversized input. Authentication, approval,
restart, and generic error blockers never acquire a clarification value.

Codex asynchronous questions are recognized from completed `agentMessage`
items carrying `delivery: "async"` and a structured `questions` array. These
use the same bounded, session-bound clarification flow as synchronous native
requests. Ordinary prose and incomplete item notifications do not authorize a
clarification; malformed structured questions remain operator-held.

`clarification-v1` is advertised in the capabilities response when this
support is available. A resume for such a receipt must include the same
`clarificationID`; the runner rejects a missing, stale, or foreign ID. The
resolution is explicit text passed to the existing session, while approved
input and instructions remain immutable. The durable operation record makes a
repeated identical resume return the newer receipt without launching another
turn, including when the first response was lost.

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

## Optional Git result export

The runner advertises `git-result-v1` in `verificationCapabilities`. A caller
must explicitly set `exportGitResult: true` in the approved Start request to
request the export; ordinary completed attempts produce no Git result
artifacts. The export is intended for a separate coordinator that owns
publication. The runner itself never pushes Git or creates a pull request.

Example request field:

```json
{
  "exportGitResult": true
}
```

After a successful harness completion, the runner snapshots tracked and
nonignored files from the task worktree using a temporary index. It preserves
the worktree's normal HEAD and index, and writes owner-only artifacts below the
runner state directory:

- `git-result.json` is a bounded `git-result/v1` document containing Task and
  Attempt identity, the approved `baseCommit`, result commit/tree identity,
  and `noChanges`.
- `git-result.bundle` is present only when the snapshot differs from the base.
  It contains one deterministic sanitized snapshot commit whose sole parent is
  the approved base commit, with fixed runner identity, timestamp, and commit
  message. The bundle advertises only the runner-result ref.

The generated names are reserved. A harness cannot submit an artifact under
either name when export is enabled. If export fails, the attempt is failed and
does not report completion with a partial result. If the snapshot tree equals
the approved base tree, `noChanges` is true and the result omits commit, tree,
bundle digest, and bundle artifact. Unstaged deletions and file/directory
transitions are included; symlink ancestors are rejected.

The exporter includes the final tracked and nonignored worktree contents. A
caller that uses this export should instruct the harness not to place private
planning documents, transcripts, credentials, or verification logs in that
tree, but the exporter is not a content secret scanner. Callers must treat the
worktree and resulting bundle as private and apply their own publication
policy.

## Limits and verification boundary

The default bounds are 256 retained events per attempt, 64 KiB per event
payload, 2 MiB per JSON request body, and 32 MiB per artifact. Configuration
can lower or raise these limits within the implementation's accepted values;
callers should use the advertised protocol response and error code as the
authority. The runner supports at most one harness turn per execution and one
simultaneous execution by default.

Independent focused Git-fetch and race invocation tests passed. The full runner
executor checks (runner tests, race, lint, vet, and build) also passed, along
with Linux and Darwin arm64/amd64 builds. A local artifact-consumer fixture
accepted the real runner artifacts. These checks establish protocol, process,
harness, Git-fetch, and local artifact contracts. They do not establish live
GitHub publication or provide live Mac stage6B proof. Live acceptance still
needs an actual enrolled host, approved local repository, start/observe path,
interruption, restart, and same-session resume evidence.

## Managed local installation and upgrades

`faros-runner --version` prints JSON build, protocol, and host metadata without
loading enrollment, credentials, or Codex. The running capabilities response
reports the executable's build version; enrollment cannot override it.

`make package-runner-darwin` builds plain arm64/amd64 tar archives containing the binary,
manager, and `install.sh`, plus archive SHA-256 files. Verify the archive digest
from your trusted distribution before extracting and running `sh install.sh`.
This installs locally; it does not start the runner or enroll a worker.

The Python 3 helper `hack/runner-install/manage.py` installs a **raw binary**
from a trusted distribution with its separately verified SHA-256. It uses no
network, never changes runner configuration, and retains the previous binary.
Run it as your worker account, using the same install root for every command:

```sh
python3 manage.py install --binary ./faros-runner --sha256 EXPECTED_BINARY_SHA256
python3 manage.py run -- --config /absolute/path/runner.json \
  --codex-home /absolute/path/codex-home --version-pin YOUR_TESTED_CODEX_VERSION
```

Before upgrading, drain the Worker in its coordinating provider and wait until
its current attempt has stopped. Stop the runner process, install the new binary
with the same command, then launch with the same configuration and Codex home.
Managed runs hold the install lock for their lifetime. For the first transition
from a manually launched runner, stop that old process yourself. Keep the Edge
agent running. Do not run the disposable fixture setup script on an existing
worker: it writes fixture enrollment.

Confirm the running version, runner identity, readiness, exact harness version,
and required capabilities through the coordinating provider's published Edges
Service client before undraining. Installation success and `--version` are
local checks, not proof of live worker readiness. `status` inspects the selected
binary while the manager is stopped.

For a failed upgrade, keep the Worker drained, stop the runner, then:

```sh
python3 manage.py rollback
python3 manage.py run -- --config /absolute/path/runner.json \
  --codex-home /absolute/path/codex-home --version-pin YOUR_TESTED_CODEX_VERSION
```

Rollback switches only the executable. It does not roll back journals, harness
sessions, credentials, or configuration. Only roll back across versions whose
state formats were verified compatible; matching wire protocols alone does not
prove this. If the upgraded runner wrote an incompatible state format, keep it
stopped and recover from a tested backup instead. This helper does not install a
launchd service or automatically retry failed jobs.
