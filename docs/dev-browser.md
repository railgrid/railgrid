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

Save PNG/JPEG evidence under `artifacts/screenshots/` in the runner's task
checkout. Include captioned Markdown links to those local files in the final
response/PR description. Keep at most eight files, 2 MiB each and 8 MiB total.
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

The `screenshotsDirectory` value in `.kcp/dev-browser.json` is an absolute
path at the Git checkout root. Use it even when running tools from a provider
subdirectory. PR image links must be root-relative
`artifacts/screenshots/name.png`; `providers/<name>/artifacts/screenshots/`
is not exported by the runner.
