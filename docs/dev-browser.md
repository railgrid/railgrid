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
preserves checkout data, logs and screenshots. Existing legacy/shared stacks
are deliberately not eligible for automatic bootstrap or deletion. Interrupted
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
