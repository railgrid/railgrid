# Template authoring conventions

How an infrastructure `Template` exposes the things its instances can configure —
container images, versions, sizes, ports — and where platform-global values come
from. The guiding rule:

> **Configurable inputs are `spec.schema` fields with sane defaults. They are
> never injected via `${faros.*}` environment-substitution tokens.**

A template must produce a valid workload out of the box, with no deployment-time
env required. A missing env variable must never be able to bake an empty or
invalid field into a materialized resource.

## The three kinds of value

| Kind | How to express it | Example |
|---|---|---|
| **Per-instance, configurable** (image, version, size, replicas) | `spec.schema` field **with a `default`**; the resource references `${schema.spec.<field>}` | `simple-webapp.spec.port` (`default: 8080`); `database.spec.version` |
| **Fixed sidecar / tooling image** (not user-facing) | **hardcoded literal** in the resource | the control-token `bitnami/kubectl` job (database, redis, application); `quay.io/oauth2-proxy/oauth2-proxy:v7.6.0` |
| **Platform-global, no universal default** | a reserved `${faros.*}` substitution token, resolved by the kro backend from env | the exposure Gateway parent `${faros.gatewayName}` / `${faros.gatewayNamespace}`; the dev-overlay images `${faros.devImage.<toolchain>}` / `${faros.devAgentImage}`; the exposure-URL port suffix `${faros.appPublicPort}` (empty in prod, `:10443` on local kind) |

## Why not `${faros.*}` env tokens for images?

They were tried for the sandbox runner and removed. The failure modes:

- **Empty → invalid.** An unset `FAROS_SANDBOX_RUNNER_IMAGE` substitutes to `""`,
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

`${faros.*}` tokens are reserved for the handful of genuinely platform-wide
values that have no sane universal default and are referenced identically across
apps: the exposure Gateway (`${faros.gatewayName}` / `${faros.gatewayNamespace}`,
every `HTTPRoute`), the dev-overlay images (`${faros.devImage.<toolchain>}` /
`${faros.devAgentImage}`, injected only in development mode), and the exposure-URL
port suffix (`${faros.appPublicPort}`, empty in production). All of these are
platform config, never per-tenant inputs — which is exactly why they are env
tokens rather than schema fields. A per-instance value (an app's own image,
version, size) is never a `${faros.*}` token.

## Consuming another instance's credentials: `connections`

The standalone `database` and `redis-cache` templates publish their
credentials only as a Secret in the workspace's runtime namespace
(`<values.name>-db-credentials` and `<values.name>-credentials`, key `uri`).
Every instance of a workspace lands in that one namespace
(`<clusterID>-default`), so a workload can reference those Secrets directly.
The `env` map input can't carry them: it is rendered as a world-readable
ConfigMap consumed through `envFrom`.

The shipped workload templates (`simple-webapp`, `worker`, `cron-job`)
therefore take a `connections` input with fixed slots:

| Input | Value | Injected env | Source |
|---|---|---|---|
| `connections.database` | `values.name` of a `database` instance | `DATABASE_URL` (`postgres://appuser:<pw>@<name>-db:5432/appdb`, no `sslmode`; in-cluster TLS is off, so clients use `sslmode=disable`) | Secret `<name>-db-credentials`, key `uri` |
| `connections.cache` | `values.name` of a `redis-cache` instance | `REDIS_URL` (`redis://:<pw>@<name>:6379`) | Secret `<name>-credentials`, key `uri` |

```yaml
values:
  name: todo-api
  image: ghcr.io/acme/todo-api:1.0.0
  connections:
    database: todo-db   # a database instance in the same workspace
    cache: cache        # a redis-cache instance in the same workspace
```

Both slots default to `""`, meaning not connected. Each slot always renders
one literal container env entry:

```yaml
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: '${schema.spec.connections.database == "" ? schema.spec.name + "-unbound-db" : schema.spec.connections.database + "-db-credentials"}'
      key: uri
      optional: '${schema.spec.connections.database == ""}'
```

When a slot is unset, the entry points at a placeholder Secret that never
exists and has `optional: true`. The variable stays unset, and a value of
the same name from the `env` map still applies through `envFrom`. When a slot
is set, `optional` is `false`: the pod waits in `CreateContainerConfigError`
until the Secret and its `uri` key exist, so the slot must name a real
instance in the same workspace. Apps must retry the first connection and run
migrations idempotently inside that retry loop.

Why fixed slots rather than a list of references:

- `backend/kro/simpleschema.go` converts a list of objects to a kro `[]object`
  with no item properties, so a list input loses its shape on the kro side.
- The development overlay (`backend/kro/devoverlay.go`) copies the production
  container's `env` as a literal list. A CEL expression that builds the list
  would be dropped in development mode. Literal entries carry over, and the
  seed tests assert that they do.
- The slot patterns (`^$|^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`) admit `""` so the
  default is valid against the CRD's own pattern. kro gives the `connections`
  object `default: {}` because its children have defaults, so an instance
  that omits it still renders.

A new workload template that should consume databases or caches should copy
this shape exactly, including the input names, Secret names, and env names,
so that agents can use the same guidance for every template.

## Checklist for a new template

- [ ] Each container image is either a `spec.schema` field with a sane `default`
      (user-overridable) or a hardcoded literal (fixed tooling). Never an env
      token.
- [ ] Integer/boolean fields are schema fields (so their type is carried), not
      string substitutions.
- [ ] The template renders a valid graph with **zero** deployment env set —
      verify with the `backend/kro` seed-template tests
      (`buildRGD` + “no unsubstituted `${faros.*}`”), and against a real kro
      cluster (`GraphAccepted=True`).
- [ ] A per-deployment platform value with no universal default? Reconsider —
      if it truly has none, raise it for a new reserved `${faros.*}` token
      rather than reaching for an env override in one template.
