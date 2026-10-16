# Agent-driven live UI evidence

Start `make tilt` in the checkout and keep it running. In another shell run
`make dev-browser-ready`. This triggers the existing Tilt registration/init
resources, waits for the configured providers, logs in as the existing Tilt
development identity, and enables providers in its personal development
workspace. Dependencies are enabled first. Setup is repeatable and has a
20-minute total timeout (`DEV_BROWSER_TIMEOUT` in seconds).

The command writes `.kcp/dev-browser.json` with the portal URL, org/workspace,
provider list and a private token-file reference. A browser agent opens the URL
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

The runner reads that command from the approved base commit and runs it in the
resulting checkout after success, failure or cancellation, including cancellation
while parked for a question. It never runs it merely because a question needs
an answer or the runner shuts down with a resumable attempt. Cleanup has its own
five-minute deadline and process group, independent of cancelled harness
contexts. Output stays in the private `.railgrid-cleanup.log`; completion or
failure is reported as a runner progress event. Failed/interrupted cleanup is
retried when the runner restarts. Cleanup must be idempotent and scope all
resources explicitly. It executes trusted repository code with runner-host
access; it is not a generic hook for untrusted ticket content.

The companion private-provider repository provides the same commands and owns
its Railgrid checkout under `.railgrid-dev`. Screenshot files belong in the
provider task checkout, not inside that companion checkout.
