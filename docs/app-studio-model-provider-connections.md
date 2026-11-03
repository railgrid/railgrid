# App Studio model provider connections

App Studio separates a provider account from the models selected through it.
**Models** manages model bindings and the workspace default. **Usage & cost**
(`~models/usage`) has its own tab rather than appearing below the model list;
it retains the explicit unavailable state until reporting is supported. **Provider
connections** manages provider endpoints and workspace credentials. The staged
setup follows the Agents flow: connect a provider once, then select and verify
each model that will use it.

A workspace-scoped `ModelProviderConnection` in `ai.railgrid.ai/v1alpha1` holds
provider configuration, an optional Anthropic workspace ID, and a reference to
the generated credential Secret. The key or service-account JSON stays in an
immutable Secret in the workspace's `default` namespace; the connection object
contains no key material or model selection. Model bindings remain in
`Studio.spec.llm.models`, with a `connectionRef`, stable model ID, display name,
reasoning preference, and immutable revision history. Keeping logical IDs
preserves project selections and the workspace default. Google API keys and
Vertex service-account JSON remain supported. The UI generates a stable
resource name and offers an editable display label; when left blank, the label
defaults to the provider name, with an ordinal when multiple connections use the
same provider. The label is stored as metadata, separately from connection
configuration.

## Verification and credentials

Discovery checks provider access; it does not prove that a model can complete
inference. For a draft provider connection, `discover-models` may confirm access
and save the connection without adding a model binding or sending an inference
request. If the provider does not support discovery, the user can enter an exact model ID
and run `test-model`; the subresource call itself does not persist the draft,
although a successful check lets the UI save the connection. Vertex service-account
JSON does not use automatic model discovery in the setup form, so it requires an exact
model ID check before the connection can be saved. Adding a model verifies the
exact selected model before saving its binding. Editing provider settings or
rotating its key verifies every active model binding that uses the connection
before the new configuration is activated. A connection with no active model
bindings can be saved after discovery alone. Changing only a connection label
updates its metadata annotation and skips discovery and inference.

Draft checks use the Studio's `discover-models` and `test-model` custom
subresources. Saved-connection checks use the `discover` and `test` subresources
on the named `ModelProviderConnection`; both require `use` permission before
the API reads its Secret. Rejected credentials and other discovery failures keep the draft at the credential field; only unsupported discovery opens manual model verification. Typed credentials are transient request inputs. The
portal never reads Secret data. Draft `discover-models` and `test-model` calls
do not persist the draft; the UI saves a connection after successful discovery
or explicit model verification.
Reusing a saved credential requires the original provider configuration and
the server's current connection fingerprint; changing provider, endpoint, or
Anthropic workspace, or observing a stale fingerprint, requires fresh
verification.

Legacy-shaped model entries cannot reuse generated provider keys or keys
referenced by saved connections. New turns and draft probes require the
connection binding and its authorization path; migration does not turn those
keys into another connection with a different endpoint.

Portal reads and writes have separate modules. `llmRegistryRead.ts` reads Studio
and the public connection/status views through the tenant kube client; it never
reads credential bytes. `llmRegistry.ts` owns connection and Studio mutations,
including generated Secret writes and model revision snapshots. `api.ts` loads
each module on demand.

Changing provider, endpoint, credential mode, or Anthropic workspace clears a
typed credential and invalidates earlier verification. In-flight results are
scoped to the current workspace and draft. Failed checks retain the draft for
correction and do not activate its endpoint or key. Rejected replacement keys
leave the current connection configuration active. Keys are never included in
browser draft storage. The connection controller resolves the Secret and
reconciles `SecretResolved`, `Reachable`, and `Ready` conditions and a non-secret
connection fingerprint. It never sends credentials to a provider endpoint.
Only saved `discover` / `test` calls, or Studio checks reusing a saved connection,
can publish probe results after the caller passes `use` authorization. Results
carry `status.lastProbeFingerprint` and are discarded if the configuration,
Secret version, or connection identity changed during the request. Typed draft
checks do not publish saved-account status. Before an explicit saved check,
the connection shows **Not verified**. `Ready` reports a resolved Secret and a
current successful authorized check, or intentionally unavailable discovery;
it does not claim that every model can complete inference. Admitted snapshots
remain separate from this reachability observation.

Key rotation creates a new immutable Secret version and conditionally saves the
new desired Secret reference and endpoint. The controller writes every required
binding revision with a Studio resource-version check before publishing
`status.activeSnapshot`. Runtime calls use that admitted snapshot, rather than
an unadmitted desired spec. If concurrent edits fill revision history, the
previous admitted configuration remains usable; the connection shows **Changes
pending** until rollover can finish. If a concurrent spec edit races the
admission-status write, new turns can still select the matching admitted
revision from history. Portal capacity checks provide early feedback but are
not the authority that activates credentials. Editing a model
does not rewrite its provider credential. A model label-only edit changes the
Studio registry name while preserving the model ID and active revision; it
sends no discovery or inference request. Deleting a model does not delete a
shared connection. The UI disables connection deletion while active bindings
use it. New model selection refuses connections already marked for deletion.
Before releasing a connection finalizer, the controller transfers all exact-owned
generated key versions to Studio retention, including keys that an in-flight
Studio save might reference. It does not delete keys using a second, unrelated
Studio read during finalization.

Studio and Secret watches also drive collection after the last connection is
gone. A key is eligible only when its generated identity and Studio owner match,
no live connection retains it, no active or archived model snapshot references
its exact identity, and no other owner claims it. The collector first records
`Studio.status.retiredModelCredentials` with a resource-version CAS, then deletes
the exact Secret UID/resourceVersion. A concurrent Studio edit conflicts with
that retirement write; a later edit cannot reactivate the retired UID through
runtime selection or a resume. Full retirement history fails closed and retains
the key. External user-owned Secrets are never adopted or collected.

A resumable run still requires its registry model revision. Explicitly removing
an archived revision invalidates those resumes; the key collector does not extend
that revision's lifetime through an independent database audit reference.

## Existing models and resumable runs

The `modelproviderconnection` controller promotes legacy direct models
independently to reusable connections. Matching endpoints do not establish that
credentials belong to the same account. Migration preserves logical model IDs,
the workspace default, and old direct revisions. It also completes interrupted
migrations from their recorded source revision and identity, rather than
adopting a same-named connection. Before the first promotion, the controller
records the eligible original bindings and Secret UIDs in
`Studio.status.legacyModelMigrations`. This lets independent legacy models
sharing one Secret migrate safely, including different endpoints, while
rejecting caller-added entries that try to join the cohort afterward.
Missing or empty legacy keys remain parked without blocking later usable
models. Omitted legacy endpoints are resolved through the provider's established
default for both initial migration and retry comparison; the original provenance
remains unchanged. At the revision limit, migration can attach the admitted connection to the
existing logical revision in place. The original direct configuration and
Secret UID remain in controller-owned provenance, so pre-migration runs retain
their original transport without requiring another archive slot.

App Studio runs pin immutable model revisions. A connection edit must retain
the endpoint and credential snapshot for those revisions while creating the
new active configuration. Archived revisions therefore continue to resolve
their original settings. Secrets needed by retained revisions remain available.
External key identities are recorded in `Studio.status.protectedModelCredentials`
before controller discovery or admission. This bounded controller-owned history
survives rotations and connection deletion, so a caller cannot reuse an old key
through a direct legacy model or a new migration. Migration provenance also
protects keys admitted before this history was introduced. External Secret data,
metadata, and ownership are unchanged. Generated-key ownership checks derive from
the Secret itself as well as the connection annotation; omitting that annotation
cannot turn another connection's generated key into an external key.

New runs and resumed runs retain the workspace's `spec.llm.runtime` retry,
backoff, and stream idle timeout settings when resolving their model snapshots.


A Studio writer can change its spec, so a spec snapshot is not authorization.
Starting a new run requires `use` on the named connection and validates the
selected snapshot against that live connection before using its credential.
A binding awaiting rollover must be refreshed before starting a new run.
Checkpoint resumes recheck current `use` permission before claiming the paused
run and before reading its key. Interrupted continuations do the same, including
legacy audits without a connection snapshot: their authority is resolved through
the migrated connection and original revision. Denial leaves a paused checkpoint
unclaimed. Resumption uses server-recorded snapshots and controller migration
provenance, rather than accepting caller-written endpoint changes.

## UI and verification

The shared visual contract is
[Model connections](design/components/model-connections.md). Model discovery
and manual IDs, connection reuse, credential replacement, dependent-model
verification, defaults, and project return routes belong to the App Studio
adapter. Harness identities remain an Agents feature.

Run `make test-app-studio-model-connections` for the App Studio portal,
API, Studio and connection-controller suites; run `make codegen-app-studio-provider`
and `make verify-provider-contract` after API or manifest changes. The shared
cross-provider visual fixture checks the actual entry points at desktop and
mobile sizes in both themes. Live validation should use the actual Tilt hub,
kcp resources, and provider backend with a loopback synthetic OpenAI-compatible
endpoint, so rejected credentials and model responses can be exercised without
an external model account. Do not treat the fixture's mocked discovery as live
provider validation.
