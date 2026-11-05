# Bring your own provider

An organization can run a customer-authored Railgrid provider on its own connected
Kubernetes Edge. It does not need a platform catalog entry or Railgrid approval.
Register it through the Hub API, deploy its Helm chart using the hosting cluster's
credentials, and enable it in selected workspaces through the ordinary console.
Existing platform-provider self-hosting remains available.

Open implementation findings are tracked in the
[BYO provider review issue inventory](./byo-provider-review-issues.md).
Their remediation is currently deferred.

## Trust boundary

The organization accepts the provider's behavior within its own organization.
Railgrid must prevent its native credentials, API declarations, backend routing,
and scoped identities from acquiring another organization's or platform authority.
Provider SDK checks help well-behaved implementations; server-side enforcement
must not depend on customer code calling those checks.

**Browser limitation accepted for MVP:** provider JavaScript runs as trusted code
in the console document, just like platform-provider JavaScript. It can access
the signed-in user's portal authority, including other organizations and platform
administration if the user has those permissions. The supported
`railgridContext.fetch` helper and SRI do not isolate malicious code executing in
that document. Browser isolation and organization-specific console origins are
post-MVP work; the existing organization/workspace URL paths remain the console
experience. Revoking a registration cannot
unload already executed JavaScript; close or reload its console document.

**Storage quota gap accepted for MVP:** the pinned kcp dependency has a reported
cluster-scoped ResourceQuota accounting defect. This implementation does not fix
upstream kcp, introduce a fork, change dependency pins, or require that defect to
be fixed before enrollment. Reliable containment of persistent storage exhaustion
is not claimed. There are no per-organization request, body or concurrency
limits inside kcp either: the in-process organization guard that carried them
was removed, so a shard's protection against a noisy customer provider is kcp's
own request limits and priority-and-fairness, plus whatever the ingress
enforces. Operators must account for shared availability risk when permitting
customer-authored providers.

## Manual installation

Registration follows the existing organization `catalogEntryCreation` policy:
organization admins by default, or organization members where an administrator
explicitly enabled that existing setting. Installing on an Edge also requires
access to the selected hosting Edge. A workspace-admin role alone does not grant
organization-admin privileges.

1. Author a provider using the existing SDK, CatalogEntry manifest, generated
   APIExport and APIResourceSchemas. Declare foreign access under `spec.requires`.
   Start with `providers/quickstart` for APIs, controllers, custom subresources,
   readiness, heartbeat, and a portal custom element.
   Build the image with the SDK version containing organization bootstrap support;
   upgrading the Hub cannot change an older image that publishes APIs directly.
2. List eligible hosting targets using
   `GET /api/orgs/{org}/providers/install-targets`.
3. Register through `POST /api/orgs/{org}/providers` with this body:

   ```json
   {"name":"my-provider","edge":{"workspace":"hosting-workspace-uuid","name":"my-cluster"}}
   ```

   Authenticate with the caller's normal Hub bearer and organization selection.
   The name must match the CatalogEntry. `sourceProvider` is optional and only
   selects an existing platform provider's installation instructions; an authored
   provider does not need it or any platform counterpart.
4. Save the response's `kubeconfig` string to a private file. It is already YAML,
   not base64. This is a restricted Railgrid provider credential, not a credential
   for the hosting Kubernetes cluster. Do not log or commit the response.
5. Using the hosting cluster's kubeconfig, create a namespace and a Secret with
   that file under the key `kubeconfig`. Mount it in the provider init and runtime
   containers as `RAILGRID_PROVIDER_KUBECONFIG`. For example:

   ```sh
   kubectl --kubeconfig "$EDGE_KUBECONFIG" -n "$PROVIDER_NAMESPACE" \
     create secret generic railgrid-provider-kubeconfig \
     --from-file=kubeconfig="$PROVIDER_KUBECONFIG"
   helm --kubeconfig "$EDGE_KUBECONFIG" upgrade --install my-provider "$CHART" \
     --namespace "$PROVIDER_NAMESPACE" --version "$CHART_VERSION" \
     --set providerKubeconfig.secretName=railgrid-provider-kubeconfig \
     --set-string hub.url="$HUB_URL"
   ```

   The sample values match Railgrid's reference chart; an authored chart owns its
   value names. Configure its hub URL, provider name, CatalogEntry file and generated
   kcp directory. Keep production TLS verification enabled.
6. Run the ordinary SDK `install.Bootstrap` in init. In an organization workspace
   it submits the declaration to the authenticated Hub bootstrap endpoint. The Hub
   validates and publishes it, including its own shard relay destination. The
   runtime credential cannot publish arbitrary APIExports, RBAC, conversion
   webhooks, or shard destinations directly. Provider-private CRDs are submitted
   as bootstrap declarations, not installed with an unrestricted runtime identity.
7. Wait for readiness and authenticated heartbeat. The provider appears in the
   organization's catalog. Enable it separately in each intended workspace and
   review the declared dependencies using the existing Enable flow.

Registration and bootstrap are retryable. Registration alone creates no tenant
APIBinding. The console installer for new customer-authored charts and a new CLI
convenience command are deferred; the documented Hub API is the MVP interface.

## Requests and API ownership

The Hub creates a provider workspace at
`root:railgrid:tenants:<org>:providers:<name>`. Restricted provider credentials
read their own runtime state and access enabled consumers through their APIExport
virtual workspace. Hub-owned enrollment metadata lives outside their authority.

**Who may bind an organization export is decided by kcp, not by Railgrid.**
kcp's APIBinding admission runs a SubjectAccessReview for the `bind` verb on
the APIExport, evaluated in the provider workspace as the user creating the
binding. Platform exports publish a `railgrid:providers:bind:<export>` grant
to `system:authenticated`, which is why anyone can bind them with kubectl.
Organization exports publish no bind grant: the SDK removes it in an
organization workspace, the Hub deletes any legacy copy before bootstrap, and
the restricted provider credential cannot write RBAC. The only identity that
passes that review is kcp-admin, which the Hub uses in the per-workspace
Enable flow and only for workspaces of the registering organization. A tenant
who can write APIBindings in their own workspace is still refused with
`no permission to bind to export`, in the same organization or any other.
Knowing the export path grants nothing. The `edgesconn` e2e suite proves the
kcp decision directly.

Custom subresource requests follow kcp authorization, then the authenticated
shard-to-Hub relay, then the Hub-selected Edge tunnel and Kubernetes Service.
The relay verifies the owning organization, actual APIBinding and declared verb.
The registration fixes the owning Edge and tunnel credential. The provider
selects the Service and namespace inside that Edge; the MVP does not isolate
mutually untrusted organizations sharing one Kubernetes cluster. The customer
backend receives the original caller identity without the Hub transport credential.

MCP and browser OAuth retain the ordinary backend contract and scoped delegated
credentials. The user's general Hub bearer is not forwarded to a customer backend.
This backend property does not remove the accepted trusted-browser limitation.

Runtime selection follows the workspace's current APIBinding and export identity.
A same-named organization catalog entry must not silently replace an existing
platform binding. Disabling a provider retains the current explicit deletion
policy; export data before removal when it must be preserved. No automatic
platform-to-organization resource migration is promised.

A customer-hosted copy of a platform provider may retain that provider's name,
API groups, and schemas. This is intentional compatibility with existing provider
charts, not ownership of the platform export: the copy has its own APIExport
identity and can serve only consumers in its organization. The workspace's actual
APIBinding selects the copy used for requests and console assets. An existing
platform binding continues to select the platform copy until explicitly changed;
publishing an organization copy does not migrate or combine their resources.

## Deployment requirements and lifecycle

Embedded and separately deployed kcp run stock kcp: there is no Railgrid shard
binary and no in-kcp organization guard. What keeps an organization provider
inside its organization is kcp itself — ServiceAccount tokens scoped to their
issuing cluster, the restricted `railgrid:org-provider` role, no bind grant on
organization exports, and the relay's binding check on every custom
subresource. External deployments additionally need the authenticated shard
relay configuration. Use mTLS for the external relay and keep trusted platform
backend listeners private from tenant workloads.

An organization export's virtual workspace serves every workspace that has
bound it. Because only the Hub can bind one (kcp-admin, in the registering
organization's workspaces), that set stays inside the organization unless an
operator creates a bind grant by hand; there is no runtime recheck of the
binding set behind an open wildcard watch, so audit such grants before relying
on the boundary. Cross-shard binding ownership enforcement remains deferred.

Set `--provider-heartbeat-auth=enforce` (Helm:
`hub.security.providerHeartbeatAuth: enforce`) before registering organization
providers. Legacy `warn` mode allows unauthenticated writes to platform-provider
liveness and version status, so enrollment and credential issuance refuse that
configuration. Platform providers must send their own service-account bearer;
remove legacy `RAILGRID_HUB_TOKEN` user-token overrides so the SDK uses the
provider kubeconfig. This prerequisite applies to embedded and external kcp.

Every shard's advertised virtual-workspace endpoint must be reachable from the
customer provider Pods with valid DNS and TLS. An internal service DNS name does
not work outside the platform cluster. Multiple shards need distinct reachable
endpoints; one Hub relay address cannot identify all shards.

Use the existing credential retrieval and rotation APIs. Upgrade Helm in place to
preserve provider workspace and APIExport identity. Existing broad provider
credentials require an operator audit and explicit retirement before relying on
this boundary; an unmarked legacy registration is not silently certified safe.
The Hub removes conventional legacy `railgrid:providers:bind:<export>` grants
when ensuring the org identity and before bootstrap publication. Audit and
explicitly disable any previously created foreign APIBindings as well; deleting
a bind grant prevents new binds but does not remove existing consumer data.
Removing a registration does not uninstall its Helm release from the Edge.
Re-registration is a new identity and must not adopt old bindings or credentials.

## Provider TLS and credential recovery

Minted provider kubeconfigs verify the Hub certificate using system trust by
default. For a private Hub CA, set `--provider-ca-file` (Helm
`hub.providerCASecretName`, Secret key `ca.crt`); the bundle is embedded in the
kubeconfig. This must trust the provider-facing Hub origin, which can use a
different CA from KCP. The certificate must cover that origin's DNS name,
including `hub.internalURL` if used. Also install private roots in the provider
container trust store for its other Hub HTTP clients, such as heartbeat and
identity issuance. Each advertised shard virtual-workspace endpoint must likewise
have reachable DNS and trusted TLS. Explicit Hub `--dev-mode` retains insecure
kubeconfigs for local development only.

Credential rotation normally overlaps the old and new tokens for 24 hours to
allow a Helm rollout. `previousValidUntil` is the scheduled deletion time of the
retired Secret, not a JWT expiry; the Hub's sweeper must be running. For a
compromised credential, an operator must rotate and immediately delete the
compromised retired token Secret in the provider workspace, then deploy the new
kubeconfig. This can interrupt the provider until its Helm rollout completes.
Use the ServiceAccount's `providers.railgrid.ai/active-token-secret` annotation
to identify the replacement token; retired Secrets carry
`providers.railgrid.ai/delete-after`. Verify each Secret belongs to the provider
ServiceAccount before deleting it. Audit all token Secrets for earlier rotations
too; revoke every compromised or obsolete Secret while preserving the active
token needed by the replacement deployment. Deleting the old token Secret
revokes that legacy credential. A short-lived refresh system and an org-admin
emergency-revoke convenience API remain follow-up work.

Automatic deployment upgrades remain deferred. The Hub checks relay
configuration before enrolling organization providers. Cross-shard credential
ownership lookup remains a known limitation, so this MVP has been validated
with a single KCP shard.
