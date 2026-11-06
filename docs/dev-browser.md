# Agent-driven live UI evidence

Start `make tilt` in the checkout and keep it running. In another shell run
`make dev-browser-ready`. This triggers the existing Tilt registration/init
resources, waits for the configured providers, logs in as the existing Tilt
development identity, and enables providers in its personal development
workspace. Dependencies are enabled first. Setup is repeatable and has a
20-minute provider initialization timeout (`DEV_BROWSER_TIMEOUT` in seconds).

The command writes `.kcp/dev-browser.json` with the portal URL, org/workspace,
provider list and a mode-0600 token-file reference. The static development token
is intentionally public in Tilt configuration; file privacy prevents accidental
artifact publication, not access by other local users. A browser agent opens the URL
and uses the normal token-login form. It chooses its own interactions and
screenshots based on the UI change; there is no predefined browser scenario.
The development stack uses self-signed TLS, so configure the browser context to
accept this checkout's development certificate. Do not disable TLS checks for
other environments.

No GitHub, LLM or Databricks credentials are required for this basic setup. The
existing `dev-tenant-setup.sh` remains an optional integration setup workflow.
Features invoking external services still require their actual integrations;
report missing integration prerequisites rather than replacing the backend
with a fixture.

External providers use the same `EXTERNAL_PROVIDERS_DIR` and
`EXTERNAL_PROVIDERS` arguments to `make tilt` as normal development. Bootstrap
discovers the live Tilt lifecycle resources, so it uses the source checkout and
environment selected at launch. A configured provider that fails readiness is a
setup failure, not a provider silently excluded from verification.

Save PNG/JPEG evidence to the absolute `screenshotsDirectory` from
`.kcp/dev-browser.json`. This is the top-level `artifacts/screenshots/` of the
checkout this command manages, including when `RAILGRID_CLEANUP_ROOT` selects
that checkout. Use this absolute path even when running browser tools from a
provider subdirectory. Include captioned, checkout-root-relative Markdown links
such as `![Changed screen](artifacts/screenshots/name.png)` in the final
response/PR description. `providers/<name>/artifacts/screenshots/` is not
exported by the runner. Keep at most eight files, 2 MiB each and 8 MiB total.
The screenshot publication pipeline must be deployed in Runner, Agents,
Factory and Code; this bootstrap command does not install that pipeline.

## Scoped cleanup

`make dev-browser-down` stops the process group recorded by the checkout's Tilt
launcher and deletes only its recorded, checkout-specific kind clusters and
containers. It validates process birth identity before signalling a PID. It
preserves checkout data, logs and screenshots. Interrupted
or failed startup retains an ownership record so cleanup can remove partial
resources. A failed cleanup retains the record for retry.

Repositories opt in to terminal runner cleanup with `.railgrid/runner.json`:

```json
{"cleanup": ["make", "dev-browser-down"]}
```

The runner reads that command and exports executable source from the approved
base commit into a temporary directory. It runs there with a restricted
environment (no inherited GitHub, LLM or runner credential variables), passing
`RAILGRID_CLEANUP_ROOT` as the retained task checkout to clean. Hooks must use
that path only for state, never execute modified files from it. This happens
after success, failure or cancellation, including cancellation while parked for
a question. It never runs merely because a question needs an answer or the
runner shuts down with a resumable attempt. POSIX process-group support is
required; Windows reports cleanup unsupported without launching the hook.

Cleanup has a five-minute deadline and process group independent of harness
cancellation. Output stays in the private `.railgrid-cleanup.log`; status/error
and attempt count are stored in runner state, with failures also in service
logs. No events are appended after the terminal event. Failed/interrupted
cleanup retries on restart, at most three attempts and two concurrent hooks.
Historical attempts created before this feature are not retroactively cleaned.
Hooks must be idempotent, since a crash can occur between deletion and persisting
success. They execute approved repository code with runner-host access.

Provider registration resources permit parallel scheduling so bootstrap does
not wait behind unrelated cold image builds in Tilt's global local-resource
queue. Bootstrap itself still orders register/init and waits for readiness.

The companion private-provider repository provides the same commands and owns
its Railgrid checkout under `.railgrid-dev`. Screenshot files belong in the
provider task checkout, not inside that companion checkout.

Port reservations use the OS account’s home directory rather than the process’s
`HOME` or `XDG_STATE_HOME`. This lets an Edge harness retain its private home
while sharing reservations with ordinary checkouts on the same host. A saved
configuration that conflicts with another checkout fails explicitly; clean up
that old stack before removing its ignored checkout state and allocating anew.

## Warm environments for ticket follow-ups

A coordinator may supply `repository.environmentID` on an Agents repository run.
Factory uses a stable Task UID plus repository identity; Agents scopes that ID
by tenant and Agent UID. The runner advertises `warm-environments-v1`. This is
separate from attempt/session identity and never resumes a different agent's
process. Both the coordinator ID and an approved repository opt-in are required:

```json
{
  "cleanup": ["make", "dev-browser-down"],
  "prepare": ["sh", ".railgrid/prepare.sh"],
  "idleTimeoutSeconds": 3600
}
```

Omitting the ID or setting/omitting the timeout as zero keeps immediate terminal
cleanup. The timeout is capped at one day. These repositories opt in to one hour;
their preparation hook refreshes an existing browser environment, but does not
start a UI stack for a backend-only task. A cold UI task still starts its stack
and runs browser setup normally. Preparation runs from approved source with
`RAILGRID_PREPARE_ROOT` and `RAILGRID_CLEANUP_ROOT` pointing at the retained
checkout; it shares the new attempt's cancellation and has a five-minute limit.

`make tilt` runs Tilt attached to the terminal and records ownership in
`.kcp/tilt-owner.json`; when Tilt exits for any reason (Ctrl-C, a crash) the
launcher cleans up after itself: it removes the checkout's containers and
stops whatever is still listening on the ports the checkout allocated, so the
next `make tilt` starts clean. The kind clusters are kept for reuse.

For a warm UI stack that outlives the agent terminal, use
`make tilt TILT_DETACHED=1`, then `make dev-browser-ready`. The detached launch
records ownership, checks for an early startup failure, and returns; logs
append to private `.kcp/tilt.log`. Nothing cleans up when a detached stack
dies on its own: `make dev-browser-down` is its stop, and it is also the full
teardown for either mode (it stops the recorded process, reaps the listeners
Tilt's own process groups left behind, and deletes the kind clusters and
containers). A later `make tilt` that finds the record of a stopped stack
reaps that stack's leftovers before checking ports. Readiness checks report a
stopped or replaced owned process with restart instructions.

A warm checkout lives below the runner's `worktrees/.environments/` directory.
Only one attempt owns it. Waiting for a user reply retains that ownership;
success, failure, and cancellation release it only after agent execution stops.
The runner's launch instructions explicitly take responsibility for cleanup, so
the agent leaves the stack running. At most one idle environment is retained.
Starting another ticket first evicts the idle stack; active/parked stacks are
never evicted. Expiry and cleanup retry state survive runner restarts.

Reuse requires the same repository/remote and approved policy. The new base
must be either the preceding approved base or the exact snapshot exported by
the preceding attempt. The runner records HEAD and the contents of tracked and
nonignored files on release and rejects reuse if they change while idle. It
never discards those edits. For the exact exported snapshot, only HEAD/index are
updated; working files remain. Other branch/base changes require a fresh
checkout after scoped cleanup. Ignored dev data, build outputs, assigned ports,
and tenant state survive compatible reuse.

Published artifacts remain immutable. Before reuse, original screenshot files
move to `environment-history/<previous-attempt>/screenshots` under runner state,
so the next run cannot accidentally export stale evidence. New UI changes still
require new live inspection and screenshots. Logs and checkout contents survive
expiry. For immediate manual teardown, run the checkout's `make dev-browser-down`;
do not delete ownership records or infer targets from the active kube context.
A later prepare failure is reported and the stale lease is cleaned up.

The runner state `environments` map records owner, checkout, release/expiry,
compatibility data and eviction state; it stores no credentials. Cleanup errors
remain on the owning attempt. A cleanup that exhausts its retries blocks reuse
until an administrator addresses it, rather than silently abandoning resources.


### Runner trust and cleanup recovery

The runner has one authenticated coordinator bearer, not per-tenant callers.
Anyone holding it already controls all attempts and can read their artifacts.
Do not share this credential with mutually untrusted coordinators or tenants.
Tenant/Agent identity is verified by Agents and included in its lease key before
it calls the runner. An extra tenant field supplied by that same caller would
not create an independent authentication boundary; multi-principal runner
access would require a separate authentication/authorization design.

Cleanup that exhausts three attempts returns a non-retryable error naming the
owning attempt. For operator recovery, stop this runner service (not other
runners), back up its state.json, inspect that attempt's cleanupError and private
.railgrid-cleanup.log, and repair the reported prerequisite. In the stopped
runner's state.json, set only that attempt's cleanupAttempts to 0 and cleanupDone
to false; leave its environment's evicting flag true. Restart the runner to retry
the approved cleanup command. Do not delete the lease or ownership records to
bypass failed cleanup. Checkout contents and screenshot history are preserved.

Worktree fingerprints run outside the runner state lock under an exclusive
lease fence. They are limited to 30 seconds and 256 MiB of nonignored content.
Exceeding those bounds retires the warm lease with a diagnostic; it never falls
back to trusting size/mtime alone. Keep build outputs in .gitignore.

### Recover a failed warm stack during active work

Keeping a warm stack at completion does not prohibit repairing it while working.
If startup left a partial cluster or readiness reports a stopped/unhealthy stack,
inspect the checkout's logs first. You may make **one** scoped recreation attempt per runner attempt. Record it
in your progress log; do not repeat it after a context reset. A new run on a
reused checkout gets its own single attempt. This is agent guidance, not a
runner-enforced counter. Use these commands from the same checkout:

```sh
make dev-browser-down
make tilt TILT_DETACHED=1
make dev-browser-ready
```

Stop if any command fails. `dev-browser-down` must validate this checkout's
recorded ownership; never bypass a refusal, delete ownership files, infer targets
from the current Kubernetes context, or remove shared/unrelated resources.
Recreation can discard tenant state and data seeded earlier in the run.
Re-create any data the task needs before verification and disclose in the final
report that recovery reset the stack. Keep source,
logs and screenshots, and do not reset the checkout. After recovery, verify the
real UI and capture new screenshots; old readiness files and images are not
proof. If recovery fails, report the concrete blocker and incomplete verification
instead of looping. Leave the recovered stack warm when the run finishes; the
runner still owns idle cleanup. Do not perform recovery while awaiting a user
answer.

## Self-hosted providers on a local Edge

The Console browser origin is local to the checkout. A provider installed into
an Edge cluster must also reach the hub from its Pods. Set
`RAILGRID_HUB_EXTERNAL_URL` to an HTTPS address reachable from both the host
and that cluster when starting Tilt, for example the Docker network gateway
and the checkout's hub port. This does not change `DEV_HUB_URL` or the browser
origin. The launcher derives a native kcp virtual-workspace address using
that host and the checkout's kcp port. Tilt exposes that authenticated native
listener to the cluster and advertises it in APIExport endpoints. Native kcp
clients must reach this listener directly, preserving their authentication.
Set `KCP_SHARD_VIRTUAL_WORKSPACE_URL` explicitly if that listener needs a
different reachable address. Do not use a loopback-resolving hostname
for this setting unless cluster DNS explicitly maps it to the host.

Development install instructions set `hub.insecure=true` for the hub's
self-signed certificate. Production recipes keep TLS verification enabled.
External prerequisites such as a Postgres Secret still need operator
provisioning; the Review fields collect references, not credentials.

## AgentKit input geometry regression

With the existing stack ready, `make test-agentkit-input-geometry` compares the
live Agent Skills activation, standalone skill library, composer and App Studio
project skill library/composer at 1440px, 900px and 390px in both themes. Set
`PLAYWRIGHT_MODULE` to an installed Playwright module when it is not on Node's
module path, plus `AGENTKIT_TEST_AGENT` and `AGENTKIT_TEST_PROJECT` to disposable
resources in the workspace from `.kcp/dev-browser.json`. The Agent must have a
usable model binding; App Studio does not need an API key for this check.
The test creates no resources and blocks model submissions. The caller owns
resource setup and cleanup. It verifies rendered icon/text clearance, vertical
centering, filled-search clear targets and focus return, Add menu viewport
bounds and keyboard dismissal, and a single composer focus frame. It also
simulates image/text upload rejections in both portals, checks visible accessible
error reasons and image action/status separation, and removes each failed upload
by keyboard to verify composer focus recovery. Image/text uploads are also
retried by keyboard, with transport-attempt and focus-recovery assertions. The
matrix also checks multi-term skill search, App Studio detail-dialog dismissal,
Agents create-form/resource controls, IME confirmation, and inline skill-chip
selection/removal. Native chooser and interrupted multi-file scope boundaries
are covered by mounted component tests. The matrix also verifies unmatched
command feedback, active-option ownership, reopening with an already-added
skill, explicit chip removal labels, and 44px chip targets on coarse pointers.
Separate-root ID collisions are covered by mounted palette/attachment tests.
Starter-prompt labels are also checked for clipping after returning to the
conversation pane. It writes at
most eight current screenshots to `screenshotsDirectory`; preserve earlier
evidence elsewhere before running when that directory already contains files.
Source assertions and copy parity cannot detect a missing generated CSS utility;
this regression inspects computed styles and actual element bounds.

Palette lifecycle coverage also exercises Shift+Tab to Back, Escape from Back,
and Tab dismissal from the external editor and filter. Mounted tests cover
rejected resource discovery, retry through Back, and late lookup failures.

The AgentKit matrix also clicks Add → Files and waits for a native file-chooser event, then clicks the command menu item and verifies its listbox. Mocked Agent history checks a sent image above and outside its text bubble without persisting a turn or making model calls.
Use `AGENTKIT_TEST_BROWSER=webkit` for the Safari-engine checks and `AGENTKIT_TEST_ORIGIN=https://console.dev.kyrosos.com` to test the public Tilt endpoint. `AGENTKIT_TEST_EXECUTABLE` can select a browser wrapper when the host needs an isolated runtime. Chromium remains the default.
The WebKit report retains nonfatal ResizeObserver deferrals and membership-fetch cancellation diagnostics during provider navigation; every other page error still fails the run.
The Add-menu regression also checks that the menu is body-mounted and remains interactive when the trigger’s local container clips overflow. Both providers retain the same synchronous file-input activation; provider-specific uploads begin only after selection.

Agent skill activation checks now target the Agent’s Skills workbench. Reusable package/editor checks target the standalone Skills library. Config no longer contains activation controls.

`hack/test-agent-skills-experience.mjs` exercises library import/create/edit/delete, read-only provider packages, per-Agent enable/disable and reload persistence, missing-reference cleanup, and browser Back/Forward at desktop and mobile sizes. It requires an authorized disposable review Agent, makes no model calls, and removes only UID-guarded test-owned skills and references.

`make test-agentkit-upload-retry` uses the same disposable Agent/Project variables as the geometry check. It persists real uploads in both portals, substitutes an error for the first successful upload response, retries the stable upload identity, and verifies stored bytes remain available until explicit removal. It submits no model turns and cleans up its exact receipts. The Skills experience check also imports quoted YAML keys and folded descriptions and delays a real save response to verify that editor controls stay locked until the write completes.

The upload check also delays the initial Agents session read, verifies that inputs wait for a session, and checks that New thread preserves its draft when the old read settles. The Skills check rejects unsupported root merge keys before storage and, on desktop, enables/selects/disables a skill without remounting the chat composer.
