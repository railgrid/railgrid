# App Studio harness validation

This guide defines the repeatable checks for App Studio harness changes and a protocol for recording live runs. It contains no live smoke-test results or benchmark outcomes. Record results in an evidence directory and report only what those artifacts support.

## Keep the evidence populations separate

A completed turn does not establish that every tool or product requirement passed. Keep these populations distinct:

- Native audit tool attempts, including failed attempts later recovered.
- Failed and recovered activity items shown by the portal.
- Durable tool-call events and their outcomes.
- Application acceptance cases and their evidence.

The durable event field `failed` records a Go invocation error. Its `disposition` records the semantic tool outcome (`succeeded` or `failed`). A typed rejection can therefore be a semantic failure with no invocation error. Older events may lack `disposition`; use another persisted status when available and otherwise record the semantic outcome as unknown. Do not infer it from error text.

A recovered retry does not erase the original attempt failure. Preserve the recovery correlation and count the attempt in its own population. Aggregate counts can identify discrepancies, but matching counts do not prove that individual calls agree because the populations have different boundaries and may include wrapper calls.

The command receipt separates process outcome from source applicability. Preserve the exit code and dispatched source revision even when the source changed while the command ran; a zero exit code with a stale receipt is not proof that current source passes. The default local sandbox mode is `off`, which uses the shared development runtime. Commands in that runtime can observe concurrent workspace writes; a revision receipt identifies the checked source revision but does not promise an immutable filesystem. Record the effective `APP_STUDIO_RUN_SANDBOX_MODE` for each run because sandbox modes have different runtime and source-isolation behavior.

The portal parses the server-owned command disclosure through a strict allowlist. Unknown fields can cause an otherwise valid receipt to be omitted from the rendered activity feed. Keep a regression for the full current server envelope, including source revision metadata, and for a command-only turn with no commentary. Exercise blocked starts, zero and nonzero exits, cancellation with and without a confirmed process receipt, long arguments, and Unicode output at the supported limits. Execution-input byte bounds, redacted public-argument bounds, and server-truncated Unicode output bounds are separate contracts; do not impose a smaller portal limit that discards a valid receipt.

Saved thread activity does not prove the portal rendered it. For a live check, reopen the completed turn in the running portal, expand its command details, and compare the rendered status, exit code, source revision, and output with the audit and durable receipt.

Managed browser sessions are scoped to the tenant, caller, project UID, and assistant thread. They remain available for five idle minutes. A provider or browser restart can discard their page history.

## Produce a reproducible report

Save the benchmark inputs together in one evidence directory:

```text
<evidence-directory>/
  audits.json
  metrics.json
  tool-timings.json
  acceptance.json       # optional
  evidence files        # screenshots, logs, or other artifacts
```

Run the report with:

```bash
make app-studio-harness-report ARGS='<evidence-directory>'
```

The report requires audit and UI metrics to describe the same distinct runs, and rejects timing calls for unrelated runs. It reports native audit attempts, portal failed/recovered items, durable-event failure flags, and command timing coverage separately. An audit with 128 tool entries is marked as possibly truncated; collect a complete audit before using it as a denominator. Missing legacy diagnostics are `unknown`, not an inferred category.

Each input is JSON. These minimal one-run shapes belong in the three separate files shown above; add the full captured records for a real report:

`audits.json`:

```json
[{"runID":"run-1","metrics":{"tools":[]}}]
```

`metrics.json`:

```json
{"rows":[{"run":"run-1","failedItems":0,"recoveredItems":0}]}
```

`tool-timings.json`:

```json
{"calls":[{"runID":"run-1","callID":"call-1","toolName":"exec_command","admittedAt":"2026-10-12T12:00:00Z","finishedAt":"2026-10-12T12:00:01Z","failed":false,"disposition":"succeeded"}]}
```

The report prints JSON sections named `nativeAudit`, `ui`, `durableEvents`, `reconciliation`, and `commandTiming`; when acceptance is supplied it also prints `acceptance`. `nativeAudit.modelRetries` counts explicit model retry rollups and reports how many runs lack them; tool retries are not inferred. These are separate telemetry populations. The reconciliation section reports their counts with an explicit comparability limitation; it does not claim calls match one to one. Acceptance gaps report case IDs, evidence availability counts, and generic reasons without echoing evidence paths or free-text reasons.

Admission-to-result intervals include queueing. Their overlap can help locate concurrency pressure, but it does not prove that processes ran simultaneously and is not a throughput measurement. Missing or unfinished command intervals make timing coverage incomplete; the overlap then describes only complete intervals. Use process start/finish observations when a live check must establish how many commands were actually running.

An optional acceptance manifest declares the cases before a live check. Every `passed` case must reference at least one existing evidence file. Relative evidence paths resolve from the acceptance manifest's directory. File existence establishes provenance, not independent validation of the artifact's content. A starting template should keep unobserved cases unverified:

```json
{
  "cases": [
    {
      "id": "component-workdir-rejection",
      "status": "unverified",
      "reason": "No live observation recorded",
      "evidence": []
    },
    {
      "id": "browser-reconnect-does-not-replay-action",
      "status": "unverified",
      "reason": "No live observation recorded",
      "evidence": []
    }
  ]
}
```

Require every declared case to pass with evidence by adding `--acceptance <file> --require-acceptance`:

```bash
make app-studio-harness-report ARGS='<evidence-directory> --acceptance <evidence-directory>/acceptance.json --require-acceptance'
```

The report exits nonzero if a case failed or remains unverified, if a passing case has no existing evidence, or if the acceptance manifest is invalid. Use owned disposable fixtures with explicit ownership. Do not reset a customized application's database to make a benchmark pass.

## Local regression checks

Run the aggregate checks after changing the harness, runtime, or its validation scripts:

```bash
make test-app-studio-harness-scripts
make test-app-studio-harness
make lint-app-studio-harness
make verify-tilt-browser-deployment
```

`test-app-studio-harness-scripts` covers the host-health and evidence-report scripts. `test-app-studio-harness` also runs App Studio API/store tests, the nested infrastructure development-agent tests, portal execution/progress/trace tests, and Tilt sequence/sandbox checks. These local checks do not exercise a live browser, a deployed provider, or benchmark performance. `verify-tilt-browser-deployment` checks the managed Browser image and isolation wiring.

## Live regression protocol

These are manual checks for an owned development environment; they are not implied to have been run by the local targets above. Use authenticated App Studio turns and the application's private preview authorization. Do not bypass the gateway or replay a write whose result is uncertain. Record the commit, effective sandbox mode, provider replica/capacity settings, project UID, source revision, component source digests, run and call IDs, and relevant application state before and after each case. Store redacted logs and screenshots beside the JSON inputs.

1. Use a component-relative working directory. First request an invalid directory, then use the suggested correction. Confirm the rejected start has no process exit code and no process side effect; record the corrected run separately.
2. Run a successful command, a real nonzero command, and a cancellable command. Compare the command receipt, native audit, durable event, and reopened portal details. A cancellation without terminal process evidence is unknown, not a fabricated exit code. Include a stale-source result and verify it retains its actual process status while remaining unverified for current source.
3. In the browser workflow, verify a successful click with a later successful snapshot. Exercise navigation and Back, a viewport resize, a second turn in the same thread, and a separate thread. Check the actual URL and page state. Test a stream timeout during a multi-turn flow: the retained session should reconnect its read stream within the bounded recovery window without replaying an action. A browser or provider restart can lose history; restoring the last verified same-origin page does not reconstruct Back history or prove an uncertain interaction succeeded.
4. Browser actions require a current isolation proof for the managed Browser instance and Template, including the runtime-projected readiness status. A custom or drifted Browser configuration without that proof fails closed. Fill the configured per-replica session slots, then try one additional browser owner and confirm the visible capacity rejection. Under the default budget (8 sessions and one configured replica), that means eight active owners and a ninth attempt. `APP_STUDIO_BROWSER_SESSION_BUDGET` is divided across `APP_STUDIO_BROWSER_REPLICA_CAPACITY`; this is a process-local allocation, not a distributed quota. Managed sessions expire after five idle minutes. Record these values with the evidence rather than assuming the default.
5. Submit five concurrent command requests to one component. Observe process starts and finishes to verify the four-command running limit, cancel one owning request, and confirm queued and sibling work continues. Record admission, process-start, finish, and queue timing separately. This is a capacity smoke, not a speedup claim.
6. In the shared development runtime, edit project source from another thread while a command runs. Verify a command dispatched for the older revision cannot certify the new revision. Use a disposable project for this mutation test and preserve unrelated application work.
7. In an owned fixture, exercise startup synchronization while the runtime supervisor is temporarily unavailable. A refused connection must remain retryable; repeating the same source revision should finish persisted reload/restart work before reporting that revision ready. Do not induce an outage in an existing application.

For cancellation and interruption recovery, compare the durable result with both the native audit and the rendered, reopened conversation. A late receipt may repair a tool entry without resuming the turn. Verify repeated reads preserve the original call identity and outcome time. Include a failed initial projection append so a receipt is not lost when its first UI event could not be saved. Preserve failed smoke attempts as evidence; repeat only after identifying and changing the cause.

## Tilt host health and log retention

The `host-health` resource checks free space on the repository, the effective OS temporary filesystem, and configured `GOTMPDIR`, `TMPDIR`, and `GOCACHE` filesystems. It reports low disk space below 5 GiB and becomes ready again when space returns. It does not delete databases, volumes, caches, or project files.

For a headless Tilt console log, set `TILT_LOG_PATH` when launching Tilt or pass `--host-log-path=/absolute/path/to/tilt.log`. Only that explicit log is rotated. Above 256 MiB allocated, the monitor keeps its last 8 MiB in one `.previous` file and truncates the open file. Existing writers keep their file descriptor; a non-append writer can leave a sparse prefix, so rotation measures allocated size. The 10-second sampling interval allows temporary overshoot. This retains recent diagnostics, not an archive. Rotation rejects symlinks, hard links, non-regular files, and files owned by another user. A rotation/path error is reported while monitoring continues; a readiness check fails when any monitored path is inaccessible or below the free-space threshold. Keep logs on a local filesystem that supports advisory locking and atomic rename. Preview port-forward routine output is suppressed while errors remain visible.
