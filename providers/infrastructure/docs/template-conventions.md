# Template authoring conventions

How an infrastructure `Template` exposes the things its instances can configure —
container images, versions, sizes, ports — and where platform-global values come
from. The guiding rule:

> **Configurable inputs are `spec.schema` fields with sane defaults. They are
> never injected via `${kedge.*}` environment-substitution tokens.**

A template must produce a valid workload out of the box, with no deployment-time
env required. A missing env variable must never be able to bake an empty or
invalid field into a materialized resource.

## The three kinds of value

| Kind | How to express it | Example |
|---|---|---|
| **Per-instance, configurable** (image, version, size, replicas) | `spec.schema` field **with a `default`**; the resource references `${schema.spec.<field>}` | `simple-webapp.spec.image` (`default: "nginx:latest"`); `sandbox-runner.spec.runnerImage` (`default: "ghcr.io/faroshq/kedge-sandbox-runner:latest"`); `postgres-database.spec.version` |
| **Fixed sidecar / tooling image** (not user-facing) | **hardcoded literal** in the resource | the control-token `bitnami/kubectl` job (sandbox-runner, database, redis, application); `quay.io/oauth2-proxy/oauth2-proxy:v7.6.0` |
| **Platform-global, no universal default** | a reserved `${kedge.*}` substitution token, resolved by the kro backend from env | the exposure Gateway parent: `${kedge.gatewayName}` / `${kedge.gatewayNamespace}` (the **only** tokens that exist) |

## Why not `${kedge.*}` env tokens for images?

They were tried for the sandbox runner and removed. The failure modes:

- **Empty → invalid.** An unset `KEDGE_SANDBOX_RUNNER_IMAGE` substitutes to `""`,
  and the kro backend bakes `image: ""` into the Deployment/Job — which the API
  server rejects (`spec.template.spec.containers[0].image: Required value`). A
  schema `default` cannot be empty.
- **Substitution-type traps.** A token substitutes a *string* into the JSON, so
  an integer field (`backendRefs[].port`) becomes `"8080"` and kro rejects the
  graph (`expected integer type … got string`); and a value meant to be a CEL
  expression (`includeWhen`) becomes a bare literal kro won't accept. Schema
  refs (`${schema.spec.…}`) carry their type from the schema and dodge all of
  this.
- **Inconsistency.** Every other template uses schema fields + hardcoded sidecar
  images; an env-token outlier is one more thing to wire (chart env, operator
  passthrough, dev Makefile) and one more thing to forget.

`${kedge.*}` tokens remain only for the exposure Gateway, which is genuinely one
platform-wide value with no sane universal default and is referenced identically
by every app's `HTTPRoute`.

## Checklist for a new template

- [ ] Each container image is either a `spec.schema` field with a sane `default`
      (user-overridable) or a hardcoded literal (fixed tooling). Never an env
      token.
- [ ] Integer/boolean fields are schema fields (so their type is carried), not
      string substitutions.
- [ ] The template renders a valid graph with **zero** deployment env set —
      verify with the `backend/kro` seed-template tests
      (`buildRGD` + “no unsubstituted `${kedge.*}`”), and against a real kro
      cluster (`GraphAccepted=True`).
- [ ] A per-deployment platform value with no universal default? Reconsider —
      if it truly has none, raise it for a new reserved `${kedge.*}` token
      rather than reaching for an env override in one template.
