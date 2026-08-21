# Faros telemetry receiver

This chart deploys the OSS Faros telemetry receiver. It accepts authenticated
CloudEvents batches and stores raw JSON events plus non-identifying aggregate
counts in PostgreSQL. It has no cloud-vendor dependency and does not create a
database or a Kubernetes PVC.

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
`Authorization: Bearer <ingest-token>` to `/v1/events`. Each CloudEvent must
be version `1.0`, include `id`, `source`, `type`, JSON `data`, and the Faros
extension `tenant` (a non-empty tenant identifier). Duplicate events are
identified by `(tenant, source, id)`, are not written twice, and are reported
in the JSON response without incrementing aggregates.

```json
[{"specversion":"1.0","id":"evt-1","source":"app","type":"run.completed","tenant":"org-123","datacontenttype":"application/json","data":{"ok":true}}]
```

## Retention and erasure

Raw payloads default to 90 days (`retention.raw: 2160h`). Aggregate buckets
default to 13 months (`retention.aggregate: 9360h`) and contain only bucket,
source, type, and count; they intentionally omit tenant identity. `POST
/v1/erasure` with the admin token and `{"request_id":"...","tenant_id":"..."}`
deletes that tenant's raw rows and records an idempotency receipt. Repeating
the same request is safe. Because aggregate contributions are not tenant
keyed, erasure cannot subtract them; this is an explicit privacy boundary.

```sh
curl -X POST https://telemetry.example.com/v1/erasure \
  -H 'Authorization: Bearer <admin-token>' \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"erase-2026-0001","tenant_id":"org-123"}'
```

`/healthz` is process health, `/readyz` checks PostgreSQL, and `/metrics` is a
Prometheus text endpoint with fixed outcome labels and no tenant or payload
labels. Tune resource, retention, batch, and event-size values in `values.yaml`.
