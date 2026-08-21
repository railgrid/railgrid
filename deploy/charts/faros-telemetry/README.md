# Faros telemetry receiver

This chart deploys the OSS Faros telemetry receiver. It accepts authenticated
CloudEvents batches and stores raw JSON events plus catalog-defined anonymous
metric aggregates in PostgreSQL. It has no cloud-vendor dependency and does not
create a database or a Kubernetes PVC.

Create three Secret keys before installing:

* `database-url`: PostgreSQL URL for the receiver database.
* `ingest-token`: bearer token accepted by `POST /v1/events`.
* `admin-token`: separate bearer token accepted by `POST /v1/erasure`.

For example, with a Secret named `faros-telemetry-secrets`:

```sh
helm install telemetry deploy/charts/faros-telemetry \
  --set secrets.database.name=faros-telemetry-secrets \
  --set secrets.ingestToken.name=faros-telemetry-secrets \
  --set secrets.adminToken.name=faros-telemetry-secrets
```

## Ingest contract

Send `Content-Type: application/cloudevents-batch+json` and
`Authorization: Bearer <ingest-token>` to `/v1/events`. Each CloudEvent must be
the hub's structured `Record`: its action equals the CloudEvent type, provider
equals both subject and catalog owner, and installation ID equals the `tenant`
extension. Every catalog identifier must be a 64-character lowercase hex keyed
hash; the payload must contain exactly the declared identifiers and bounded
properties. Unknown fields, raw IDs, and content are rejected. Duplicate events
are identified by `(tenant, source, id)`, are not written twice, and are reported
in the JSON response without incrementing aggregates. The supported producer
is the opt-in Faros hub runtime; callers should not construct payloads directly.

## Retention and erasure

Raw payloads default to 90 days (`retention.raw: 2160h`). Aggregate buckets
default to 13 months (`retention.aggregate: 9360h`) and contain only bucket,
the fixed `faros-hub` component class, a catalog-declared event action, and
count; they intentionally omit tenant, installation, and caller source
identity. `POST
/v1/erasure` with the admin token and `{"request_id":"...","tenant_id":"..."}`
deletes that tenant's raw rows and records an idempotency receipt. Repeating
the same request is safe. Because aggregate contributions are not tenant
keyed, erasure cannot subtract them; this is an explicit privacy boundary.

Catalog metric aggregates are anonymous daily buckets containing metric key,
funnel step, bounded labels, and value. Their pseudonymous uniqueness rows
contain tenant and keyed identifier hash, are erased with raw events, and share
the 90-day raw retention. Aggregates remain after erasure for up to 13 months.
Summing daily unique aggregates can count the same subject once on each day;
`faros_telemetry_funnel_current` instead counts distinct subjects from raw
uniqueness rows across each catalog 7d/28d window. It cannot provide exact
all-time distinct values after raw retention. Creation milestone counters are
naturally once per resource identity within a daily bucket.

```sh
curl -X POST https://telemetry.example.com/v1/erasure \
  -H 'Authorization: Bearer <admin-token>' \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"erase-2026-0001","tenant_id":"org-123"}'
```

## Grafana dashboard

This chart does not install Grafana. Set `grafana.dashboards.enabled=true` to
create a sidecar-discoverable dashboard ConfigMap. The dashboard declares a
`${DS_FAROS_POSTGRES}` PostgreSQL datasource input. Grant that datasource
read-only access to the receiver database and its `faros_telemetry_metric_daily`,
`faros_telemetry_counter_current`, `faros_telemetry_funnel_current`,
`faros_telemetry_activation_current`, and `faros_telemetry_pipeline_health`
views. Queries return only catalog metric keys, bounded labels, funnel steps,
and counts—never tenant, installation, source, or identifier hashes. An `all`
counter covers all retained aggregate buckets (13 months by default), not exact
all-time distinct subjects.

The ingest panel shows accepted persisted events. Receiver rejects, duplicates,
readiness, and janitor failures remain available on `/metrics` for a Prometheus
operational dashboard.

`/healthz` is process health, `/readyz` checks PostgreSQL, and `/metrics` is a
Prometheus text endpoint with fixed outcome labels and no tenant or payload
labels. Tune resource, retention, batch, and event-size values in `values.yaml`.
