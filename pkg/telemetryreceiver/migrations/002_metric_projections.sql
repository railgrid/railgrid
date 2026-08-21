-- +goose Up

-- Anonymous daily metric aggregates. These rows deliberately contain no
-- installation, tenant, identifier, event source, or raw event payload.
CREATE TABLE faros_telemetry_metric_aggregates (
    bucket_start DATE NOT NULL,
    metric_key TEXT NOT NULL,
    funnel_step TEXT NOT NULL DEFAULT '',
    labels_key TEXT NOT NULL,
    labels JSONB NOT NULL,
    value BIGINT NOT NULL DEFAULT 0 CHECK (value >= 0),
    PRIMARY KEY (bucket_start, metric_key, funnel_step, labels_key),
    CHECK (jsonb_typeof(labels) = 'object'),
    CHECK (octet_length(labels::text) <= 2048)
);

-- Pseudonymous daily uniqueness is raw data: tenant and keyed hashes are
-- retained for at most 90 days and removed by tenant erasure.
CREATE TABLE faros_telemetry_metric_uniques (
    bucket_start DATE NOT NULL,
    metric_key TEXT NOT NULL,
    funnel_step TEXT NOT NULL DEFAULT '',
    labels_key TEXT NOT NULL,
    labels JSONB NOT NULL,
    tenant_id TEXT NOT NULL,
    unique_kind TEXT NOT NULL,
    unique_hash TEXT NOT NULL CHECK (unique_hash ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (bucket_start, metric_key, funnel_step, labels_key, tenant_id, unique_kind, unique_hash),
    CHECK (jsonb_typeof(labels) = 'object'),
    CHECK (octet_length(labels::text) <= 2048)
);

CREATE INDEX faros_telemetry_metric_uniques_tenant_created_idx
    ON faros_telemetry_metric_uniques (tenant_id, created_at);
CREATE INDEX faros_telemetry_metric_uniques_window_idx
    ON faros_telemetry_metric_uniques (metric_key, funnel_step, bucket_start);

CREATE TABLE faros_telemetry_metric_catalog (
    metric_key TEXT NOT NULL,
    metric_kind TEXT NOT NULL CHECK (metric_kind IN ('counter', 'funnel')),
    funnel_step TEXT NOT NULL DEFAULT '',
    step_order INTEGER NOT NULL CHECK (step_order > 0),
    window_days INTEGER NOT NULL CHECK (window_days IN (0, 7, 28)),
    PRIMARY KEY (metric_key, funnel_step)
);

CREATE VIEW faros_telemetry_metric_daily AS
SELECT bucket_start, metric_key, NULLIF(funnel_step, '') AS funnel_step, labels, value
FROM faros_telemetry_metric_aggregates;

-- "all" counters mean all retained aggregate buckets (13 months by default),
-- not privacy-unsafe exact all-time distinct subjects.
CREATE VIEW faros_telemetry_counter_current AS
SELECT c.metric_key, a.labels, SUM(a.value)::BIGINT AS value,
       c.window_days
FROM faros_telemetry_metric_catalog c
JOIN faros_telemetry_metric_aggregates a
  ON a.metric_key = c.metric_key
 AND a.funnel_step = c.funnel_step
 AND (c.window_days = 0 OR a.bucket_start >= CURRENT_DATE - (c.window_days - 1))
WHERE c.metric_kind = 'counter'
GROUP BY c.metric_key, a.labels, c.window_days;

-- Windowed distinct values use the raw uniqueness rows so the same subject is
-- counted only once across a 7d/28d window. Daily aggregate sums can count one
-- subject again on another day and are intentionally not used by this view.
CREATE VIEW faros_telemetry_funnel_current AS
SELECT c.metric_key, c.funnel_step, c.step_order, c.window_days,
       (COUNT(DISTINCT (u.tenant_id, u.unique_hash)) FILTER (WHERE u.unique_hash IS NOT NULL))::BIGINT AS value
FROM faros_telemetry_metric_catalog c
LEFT JOIN faros_telemetry_metric_uniques u
  ON u.metric_key = c.metric_key
 AND u.funnel_step = c.funnel_step
 AND (c.window_days = 0 OR u.bucket_start >= CURRENT_DATE - (c.window_days - 1))
WHERE c.metric_kind = 'funnel'
GROUP BY c.metric_key, c.funnel_step, c.step_order, c.window_days;

CREATE VIEW faros_telemetry_activation_current AS
SELECT metric_key, funnel_step, step_order, window_days, value
FROM faros_telemetry_funnel_current
WHERE metric_key IN ('activation_funnel', 'app_studio_activation_funnel', 'agents_activation_funnel');

CREATE VIEW faros_telemetry_pipeline_health AS
SELECT bucket_start, event_type, event_count
FROM faros_telemetry_aggregates
WHERE source = 'faros-hub';
