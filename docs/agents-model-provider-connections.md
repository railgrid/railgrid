# Agents model provider connections

`ModelProviderConnection` is a cluster-scoped resource inside a tenant workspace.
It configures one provider account: provider, API endpoint, optional Anthropic
workspace ID, and a reference to a labelled Secret in namespace `default`.
The API key is stored only in that Secret. Connections contain no model ID.

A `ModelCredential` selects a model through `spec.connectionRef`, naming a
`ModelProviderConnection` in the same workspace, and `spec.model`. Agents keep
referencing ModelCredential names. Harness identities remain direct
ModelCredential objects; they have no model-provider connection.

The reference is a dependency, not an owner reference. Removing one model cannot
garbage-collect its connection or key. A model can have the same name as its
connection because they are different resource kinds.

## Readiness and verification

The connection controller watches connections and Secrets. It reports
`SecretResolved`, `Reachable`, and `Ready`, and retains bounded discovered model
IDs. Key rotation triggers reconciliation. Discovery is not proof that a
particular model can complete inference.

The model controller reports `ConnectionReady`, `ModelConfigured`, and `Ready`.
It watches connection changes and never reads or adopts the shared Secret.
These conditions mean a model is configured and its connection is available;
they do not claim a successful inference test. The portal's **Verify & connect**
action tests the selected model before saving. Connection `test` and `discover`
verbs are declared on `modelproviderconnections` in the provider manifest.

Editing a provider connection uses **Verify & save**. It verifies the configured
models, or discovers models when no bindings exist. An optional verification
model adds an inference check without creating or changing a model binding.

## Access

Listing or reading connection configuration does not permit inference. Attended
model requests and discovery require the separate Kubernetes RBAC verb `use` on
the named `modelproviderconnections` resource. The provider checks the shard's
stamped identity with SubjectAccessReview; caller-scoped MCP resolves through a
SelfSubjectAccessReview. Denial occurs before reading the key. Editing endpoint
or authentication configuration requires the ordinary write permissions, and
writing key material requires separate Secret permissions.

During migration, write access to legacy direct ModelCredential configurations
retains its prior credential-administration authority. The new connection kind
does not retroactively narrow those old RBAC grants; do not treat unrestricted
legacy credential writes as permission to configure model bindings only.

For example, a role may allow connection selection and use without editing it:

```yaml
apiGroups: [agents.railgrid.ai]
resources: [modelproviderconnections]
resourceNames: [production]
verbs: [get, use]
```

When listing all connections is forbidden, the model list reads only the named
connections referenced by visible models. A forbidden metadata read does not hide
the model or replace its observed readiness. Neither path reads Secrets.

Unattended Agents retain the existing delegation model: the provider executes
the models configured on the Agent with its own tenant-scoped identity. Granting
Agent configuration and automation permissions is therefore a trusted delegation
of provider-account use, just as configuring an Agent previously delegated use
of its direct ModelCredential. A connection reference does not grant the
attended caller access to the underlying Secret.

## Deletion

New portal connections carry the connection-protection finalizer at creation;
the controller also adds it to connections created by other clients before
adopting a key. The portal refuses to delete a connection with referencing
models. A direct Kubernetes DELETE marks it terminating, and the finalizer keeps
its owned Secret until the models have been removed or reassigned. Model
readiness becomes false while the connection is terminating. Model watches
resume finalization when the last reference is removed.

Only provider-created Secrets owned by that exact connection UID are deleted.
An externally supplied Secret is neither adopted nor deleted. Removing a
finalizer manually is an administrative override of these lifecycle guarantees.

Model deletion is idempotent: retrying an already deleted model never touches a
connection key. Legacy direct-model cleanup deletes only a Secret exclusively
owned by that exact ModelCredential UID, using deletion preconditions.

## Migration

A multicluster controller watches legacy direct API ModelCredentials. It creates
a same-name ModelProviderConnection, referencing the existing Secret without
reading, copying, or rotating key data. It transfers any existing Secret owner
reference from the legacy credential UID to the new connection UID before
changing the source object.

A legacy credential with a model becomes a model binding with the same name,
UID, and model ID. A connection-only legacy credential is removed after
ownership transfer. Existing bindings keep their connectionRef name, which now
addresses the new kind. Harness identities and temporary verification probes
are excluded. A migration annotation records the source UID so an interrupted
migration can resume. A same-name independently created connection is never
overwritten; the source is retained and the controller reports an error.
Reconciliation resumes ownership transfer and source conversion even when the
destination already exists or is waiting for deletion.

Direct legacy API credentials remain accepted for compatibility and are migrated
by the controller. New portal connections use the new kind. Deployment applies
regenerated schemas/APIExport and the CatalogEntry before starting controllers
that watch the new resource. All controllers use the existing APIExport virtual
workspace and multicluster manager; there is no tenant sweep or custom CRUD API.
