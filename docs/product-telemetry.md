# Product telemetry

Faros product telemetry is a small, explicit event system for understanding
managed-SaaS usage: organization and workspace activation, provider adoption,
edge connectivity, App Studio project progress, and Agents activity. It is not
application logging, crash reporting, a user-content export, or a general
analytics SDK.

The event catalog is part of the OSS source tree so that every proposed event,
owner, identifier, property, retention period, and privacy classification can
be reviewed in the open. Catalog reviewability is an OSS governance boundary,
not a surveillance mechanism.

## Defaults and opt-in boundaries

The hub defaults to `telemetry.mode=off`. In `off` mode it creates no telemetry
worker, exposes no provider-ingest route, and makes no outbound receiver
requests. Self-hosted/OSS installations therefore have no telemetry receiver
traffic unless an operator explicitly opts in.

Managed SaaS mode is explicit: set `telemetry.mode=saas` (or the hub
`--telemetry-mode=saas` flag), provide an HTTPS `telemetry.endpoint`, a stable
opaque `telemetry.installationID`, and references to an existing Secret holding
the sink token and HMAC secret. Credentials are read from the Secret, not put
in Helm values. The hub chart's mode remains `off` unless changed.

Provider telemetry is a separate opt-in. The `agents`, `app-studio`, and
`edges` charts each default `telemetry.enabled=false`; when enabled they set
`FAROS_PRODUCT_TELEMETRY_ENABLED=true`. A provider still remains on its no-op
tracker if its configuration is invalid or its provisioned provider kubeconfig
does not contain a token. The heartbeat `FAROS_HUB_TOKEN` is not accepted as a
telemetry credential.

Enabled providers require an HTTPS hub URL because telemetry carries the
provisioned provider ServiceAccount bearer. `FAROS_HUB_INSECURE=true` is an
explicit local/development escape hatch that also permits HTTP; the provider
charts reject `telemetry.enabled=true` with an HTTP hub URL unless that escape
hatch is set. Both provider-to-hub and hub-to-receiver clients refuse redirects
so credentials and payloads cannot be forwarded to another authority or a
downgraded transport.

## What is cataloged

The current event inventory is:

| Event | Owner | Declared identifiers | Bounded properties |
| --- | --- | --- | --- |
| `organization_created` | platform | `org`, `actor` | `outcome`: `success` |
| `workspace_created` | platform | `org`, `workspace`, `actor` | `outcome`: `success` |
| `provider_enabled` | platform | `org`, `workspace`, `actor`, `resource` | `outcome`: `success`; `provider`: `agents`, `app-studio`, `code`, `databricks`, `edges`, `infrastructure`, `kuery`, `quickstart`, `vibe-studio` |
| `edge_first_ready` | edges | `scope`, `resource` | `edge_type`: `kubernetes_cluster`, `linux_server`; `outcome`: `ready` |
| `app_studio_project_created` | app-studio | `org`, `workspace`, `project`, `actor` | `outcome`: `success` |
| `app_studio_preview_ready` | app-studio | `org`, `workspace`, `project` | `outcome`: `ready`; `preview_kind`: `development` |
| `app_studio_project_published` | app-studio | `org`, `workspace`, `project`, `actor` | durable production-binding acceptance; `outcome`: `published`, `promoted` |
| `agents_agent_created` | agents | `org`, `workspace`, `actor`, `resource` | `outcome`: `success` |
| `agents_run_terminal` | agents | `org`, `workspace`, `resource`, `run` | `outcome`: `succeeded`, `failed`, `aborted` |

Every current event is `internal_events: true`, `pseudonymous`, declares
`no_raw_content: true`, and has a catalog retention declaration of 90 days.
The generated, reviewable table is
[`telemetry/generated/catalog.md`](../telemetry/generated/catalog.md).

The event data inventory is deliberately narrow:

- the declared action and occurrence time;
- only the identifiers declared for that action (`org`, `workspace`, `scope`,
  `project`, `resource`, `run`, and/or `actor`);
- the provider owner and a small, catalog-declared property set; and
- transport identity needed for delivery and deduplication (a random event ID,
  installation ID, provider, and CloudEvents metadata).

Provider code may supply stable internal identifier values to its local SDK
boundary, but it must not put names, URLs, request or response bodies, prompts,
messages, file contents, credentials, tokens, headers, paths, queries, IP
addresses, or stack traces in an event. The provider SDK rejects sensitive
property keys and unbounded values. The hub accepts only catalog actions owned
by the named provider, requires exactly the declared identifiers and
properties, and replaces identifiers with keyed pseudonyms before a receiver
can see them. The receiver rejects undeclared fields, raw identifiers, and
non-JSON or non-catalog property values.

## Catalog, schemas, and adding an event

Definitions live in these reviewable locations:

- platform events: `telemetry/events/platform/*.yaml`;
- provider-owned events: `providers/<name>/telemetry/events/*.yaml`;
- central metrics and funnels: `telemetry/metrics/*.yaml`;
- JSON Schemas: `telemetry/schema/event.schema.json` and
  `telemetry/schema/metric.schema.json`; and
- generated registry and documentation:
  `telemetry/generated/registry.go` and `telemetry/generated/catalog.md`.

`hack/telemetry-codegen` discovers the platform root and provider event roots,
requires globally unique action names, validates the JSON Schemas and semantic
rules, and generates the registry plus catalog Markdown. Run:

```sh
make telemetry-codegen
make verify-telemetry
```

`make codegen` includes telemetry generation. To add a provider event, add its
YAML definition under that provider's event root, add or update a central
metric definition when the event is used in product analytics, and add focused
provider tests. Standalone provider modules must not import the root
`telemetry/generated` package: keep the provider's action constants local (as
the first-party providers do), use the provider-SDK seam below, and let the
root code-generation step build the hub's registry. The hub remains the
authority for owner, identifier, property, and metric validation.

## Runtime contract and authentication

The provider SDK in `provider-sdk/telemetry` is intentionally independent of
the Faros hub module. It exposes a `Tracker` and a bounded `Event` shape. The
hub receives provider JSON at:

```text
POST /api/providers/<provider>/telemetry
```

When SaaS mode is enabled, this route is installed only for providers in the
platform provider registry. The hub resolves the provider's own workspace
logical cluster and performs a workspace-scoped Kubernetes `TokenReview` on
the request bearer token. Authentication succeeds only when the review is
authenticated as the exact provisioned ServiceAccount:

```text
system:serviceaccount:default:provider
```

The ServiceAccount is provisioned by the hub in the provider workspace. A
missing token, failed review, wrong subject, unknown provider, or provider
without a known platform workspace fails closed. Org-owned/BYO providers are
not accepted by this first-party telemetry route; a BYO provider cannot use a
platform provider name to bypass this check.

The hub normalizes each accepted event with an installation HMAC secret using
installation- and identifier-type-separated HMAC-SHA-256 inputs. The receiver
sees lowercase 64-character hashes, not the source identifiers. The HMAC secret
is installation-specific, so identical source identifiers in different
installations do not produce a shared pseudonym; the installation ID remains
part of the HMAC domain as a defense against accidental key reuse. The receiver
also binds the CloudEvents `tenant` extension and
`faros://installation/<id>/hub` source to the installation ID, preserving
installation isolation.

## Delivery format and failure isolation

The hub sends CloudEvents 1.0 batches over HTTPS to the configured receiver
endpoint (`POST /v1/events`, content type
`application/cloudevents-batch+json`). It uses the CloudEvents Go SDK. Each
event has a random ID, source
`faros://installation/<installation-id>/hub`, type
`dev.faros.telemetry.<action>`, provider subject, installation `tenant`
extension, occurrence time, and JSON data containing the normalized record.
The receiver verifies CloudEvents 1.0, catalog ownership, hashes, exact
properties, source, subject, tenant, and size bounds before persistence.

Both seams are bounded and asynchronous:

- provider SDK defaults: queue 64, enqueue wait 10ms, send timeout 2s, close
  timeout 2s, and up to 3 attempts with 50ms initial exponential backoff;
- hub defaults: queue 1,024, batches of 100, flush after 2s, enqueue wait
  25ms, send timeout 5s, shutdown drain 5s, and up to 3 attempts with an
  initial 100ms exponential backoff; and
- queue, body, property, batch, and retry limits are validated rather than
  allowed to grow without bound.

Telemetry is best effort. Provider call sites and platform handlers discard
tracker errors; queue saturation, receiver errors, and shutdown deadlines do
not change the product operation's result. Failed delivery can lose an event,
but it cannot make product serving depend on the receiver.

## Receiver, retention, erasure, and analytics

The receiver is `cmd/faros-telemetry` and its chart is
`deploy/charts/faros-telemetry`. The chart deploys the receiver only; it does
not create PostgreSQL, a PVC, or Grafana. Create an existing Secret with these
three keys before installing:

- `database-url`: `TELEMETRY_DATABASE_URL`;
- `ingest-tokens.json`: `TELEMETRY_INGEST_TOKENS_JSON`, a JSON object mapping
  each opaque installation ID to a unique event-ingest bearer; and
- `admin-token`: `TELEMETRY_ADMIN_TOKEN`, a distinct token for erasure.

The receiver binds each request's `X-Faros-Installation-ID` header to that
installation's configured bearer, then requires every CloudEvent in the batch
to carry the same installation ID. Tokens must be unique, so one hub cannot
forge another hub's stream. The receiver runs Goose migrations under a
PostgreSQL advisory session lock on startup. Its defaults
are raw retention 2,160 hours (90 days), anonymous aggregate retention 9,360
hours (13 months), a one-hour janitor interval, batches up to 1,000 events,
and 256 KiB per event. The chart's optional Grafana dashboard is provisioned
as a ConfigMap for an existing Grafana installation and is intended to use an
operator-configured, read-only PostgreSQL datasource.

There are two retention classes:

1. Raw pseudonymous event rows and pseudonymous metric-uniqueness rows are
   retained for the shorter of the operator limit and each event catalog's
   declaration (at most 90 days). Uniqueness rows retain installation/tenant
   scope and keyed identifier hashes so 7-day and 28-day funnels can count a
   subject once across a window.
2. Anonymous daily aggregate rows are retained for 13 months by default.
   They contain catalog metric keys, funnel steps, bounded labels, and counts,
   and do not contain tenant, installation, source, identifier hashes, or raw
   event payloads. An `all` counter means all retained aggregate buckets; it
   does not mean exact all-time distinct subjects.

Daily aggregate buckets use the receiver's trusted receipt time, not the
sender-controlled occurrence time. The original occurrence time remains only
in the 90-day raw row. The current catalog entries named `funnel` are
independently deduplicated stage-volume views; they do not claim ordered cohort
conversion. Their generated descriptions state this explicitly.

`POST /v1/erasure` with the admin token and a `request_id` plus
`installation_id` durably tombstones that hub installation, deletes its raw
events and pseudonymous uniqueness rows, and records
an idempotent erasure receipt. Repeating the same request is safe; reusing its
ID for another installation is rejected while the receipt remains within the
raw retention bound. Anonymous aggregate rows cannot be subtracted by
installation because they intentionally contain no installation dimension, so
they remain until aggregate retention expires.
Concurrent and later ingest for a tombstoned installation is rejected; an
erasure cannot be undone by retrying an older event batch.

Operational pipeline health and product analytics are separate surfaces:

- Prometheus metrics expose queue depth, enqueued/dropped/sent/failed hub
  events, receiver ingest outcomes, readiness, erasure outcomes, and janitor
  failures. Their labels are fixed outcome values, not tenant or payload data.
- Grafana and SQL product analytics read the catalog-defined views
  (`faros_telemetry_metric_daily`, `faros_telemetry_counter_current`,
  `faros_telemetry_funnel_current`, `faros_telemetry_activation_current`,
  and `faros_telemetry_pipeline_health`) and expose only bounded labels,
  funnel steps, metric keys, and counts. Grafana is not the event contract and
  is not installed by the receiver chart.

The implementation uses the CloudEvents SDK, `santhosh-tekuri/jsonschema`,
`pgx`, Goose, Prometheus client libraries, and a Grafana dashboard asset.
OpenTelemetry is a suitable future boundary for operational traces and
metrics; it is deliberately not the product-event contract or its catalog,
privacy, retention, or erasure mechanism.

## Current limitations

- Receiver erasure is currently installation-wide. The receiver cannot accept
  a raw organization identifier and does not possess the hub's HMAC key, so an
  organization-scoped erasure workflow must be added at the trusted hub boundary
  before the API can truthfully claim customer-level deletion within a shared
  SaaS installation.
- The edge background path currently has an opaque tenant logical-cluster ID,
  not a verified organization/workspace mapping. It reports that ID once as
  `scope`, and edge readiness is deliberately excluded from the cross-workspace
  activation metric until a trustworthy mapping exists.
- First-ready (Edges) and preview-ready (App Studio) deduplication is
  process-local. A provider restart can produce another observation. The
  catalog's analytics uniqueness rules deduplicate by keyed identity within
  their retention windows, but they do not make the raw event stream exactly
  once.
- CloudEvents delivery is best effort with bounded retry. Duplicate delivery
  of the same event ID is safe because persistence deduplicates on
  `(tenant, source, event_id)`; queue overflow or exhausted retries can drop an
  event, so this is not a guaranteed at-least-once pipeline.

## Focused verification

The catalog and runtime have focused unit tests in `telemetry/catalog`,
`pkg/hub/telemetry`, `pkg/telemetryreceiver`, and
`provider-sdk/telemetry`. Provider event and no-op/failure-isolation tests live
in the `agents`, `app-studio`, and `edges` standalone modules. Useful commands
are:

```sh
make verify-telemetry
go test ./telemetry/... ./pkg/hub/telemetry ./pkg/telemetryreceiver
(cd provider-sdk && go test ./telemetry)
(cd providers/agents && go test ./...)
(cd providers/app-studio && go test ./...)
(cd providers/edges && go test ./...)
```

These checks validate the catalog, generated artifacts, schemas, authentication
seam, privacy/shape rejection, queue behavior, projections, retention logic,
and provider event boundaries.
