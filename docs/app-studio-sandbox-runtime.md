# App Studio development runtime

Status: **current runtime boundary**.

App Studio's development environment is **Template-backed**. A Project records
the selected infrastructure `Template`; the Template's development contract
declares its instance resource and one or more development components. App
Studio does not assume a `SandboxRunner` kind, a single container, or a fixed
toolchain. The selected Template is provisioned with `kedgeMode: development`
and its own graph owns the runtime namespace, workloads, services, routes, and
development-agent configuration.

The retained [`app-studio-runtime-decoupling.md`](./app-studio-runtime-decoupling.md)
document is a design proposal and historical rationale. It is not the current
API contract; this document describes the boundary implemented by App Studio.

## Product and provider responsibilities

App Studio owns the product-facing pieces:

- the Project and its tenant-scoped workspace files
- selecting or switching the development Template
- the `/api/projects/*` development endpoints and assistant runtime tools
- routing workspace files to the Template's declared component paths
- authorizing a preview URL and reporting edge readiness

The infrastructure provider owns the runtime graph and data plane. It resolves
the selected instance's declared data-plane contract and, using its own
runtime-cluster credential, serves control operations and preview proxying. App
Studio acts as the requesting tenant user; it does not hold a kubeconfig or
service credential for the infrastructure provider's runtime cluster.

## Development data plane

For a Project with `spec.template`, App Studio reads the Template and resolves
the instance resource (`spec.instanceCRD`) plus its development components.
Each workspace file is routed by the component's `workspacePath`; a component
sync is sent to the infrastructure provider through the hub backend proxy:

```text
POST /services/providers/infrastructure/dataplane/clusters/{workspace}/{resource}/{name}/sync
POST /services/providers/infrastructure/dataplane/clusters/{workspace}/{resource}/{name}/components/{component}/sync
```

The same caller-authenticated data-plane boundary serves `restart`, `log`,
`env`, and `process` operations. The provider authorizes the caller against
the published instance before reaching its private runtime services. Deleting
or switching a Template deletes the old instance; the provider's resource
graph owns runtime cleanup.

This is the platform
[provider-isolation rule](./providers.md#provider-isolation-the-cross-provider-boundary):
App Studio reaches infrastructure-owned workloads only through the published
instance API and data-plane subresources. It never resolves or calls a provider
backend URL directly, so a tenant can be backed by a different infrastructure
provider/runtime cluster without an App Studio-specific credential.

## Preview authorization and readiness

`POST /api/projects/{project}/authorize-development-preview` reads the selected
instance's `status.url`. A URL is a candidate only; App Studio probes the
public edge (DNS, TLS, and Gateway routing) before returning `ready: true`.
While the edge is provisioning it returns `ready: false`, a stable reason, and
a human-readable message. The portal retries that authorization until the edge
responds and then renders the Template's normal public route.

The current Template preview is **not** an App Studio-signed preview token or a
companion `SandboxPreviewHTTPRoute`. The URL is the instance's ordinary
exposure route, and browser traffic goes directly to that route. The
`APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY` setting only permits a local
self-signed Gateway during the readiness probe; it does not change production
certificate verification.

## Capability boundary

App Studio should depend only on the published capabilities it needs:

- component-aware `sync`
- `restart`, `log`, `env`, and `process` data-plane verbs where the Template
  declares them
- instance status from the tenant API
- preview URL authorization plus edge readiness

The infrastructure provider keeps runtime service names, namespaces, control
tokens, and runtime-cluster credentials private. Its data-plane resolver
validates status references and authorizes the caller before proxying, so stale
or forged status cannot redirect App Studio to arbitrary runtime services.

## Current security caveats

Development runs user-generated code. This remains a development-oriented
runtime, not a complete untrusted-code sandbox. Before broad production use,
add explicit runtime isolation (for example AppArmor or a hardened runtime
class), quota defaults, image provenance controls, and restrictive network
policy. File sync remains text-file oriented and skips binary or oversized App
Studio workspace files.
