-- +goose Up

DROP VIEW faros_telemetry_activation_current;
DROP VIEW faros_telemetry_funnel_current;
DROP VIEW faros_telemetry_counter_current;

CREATE VIEW faros_telemetry_counter_current AS
WITH counters AS (
    SELECT DISTINCT metric_key, window_days
    FROM faros_telemetry_metric_catalog
    WHERE metric_kind = 'counter'
)
SELECT c.metric_key, a.labels, SUM(a.value)::BIGINT AS value, c.window_days
FROM counters c
JOIN faros_telemetry_metric_aggregates a
  ON a.metric_key = c.metric_key
 AND a.funnel_step = ''
 AND (c.window_days = 0 OR a.bucket_start >= (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date - (c.window_days - 1))
GROUP BY c.metric_key, a.labels, c.window_days;

CREATE VIEW faros_telemetry_funnel_current AS
SELECT c.metric_key, c.funnel_step, c.step_order, c.window_days,
       COALESCE(u.labels, '{}'::jsonb) AS labels,
       (COUNT(DISTINCT (u.tenant_id, u.unique_hash)) FILTER (WHERE u.unique_hash IS NOT NULL))::BIGINT AS value
FROM faros_telemetry_metric_catalog c
LEFT JOIN faros_telemetry_metric_uniques u
  ON u.metric_key = c.metric_key
 AND u.event_type = c.event_type
 AND u.funnel_step = c.funnel_step
 AND (c.window_days = 0 OR u.bucket_start >= (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date - (c.window_days - 1))
WHERE c.metric_kind = 'funnel'
GROUP BY c.metric_key, c.funnel_step, c.step_order, c.window_days, COALESCE(u.labels, '{}'::jsonb);

CREATE VIEW faros_telemetry_activation_current AS
SELECT metric_key, funnel_step, step_order, window_days, labels, value
FROM faros_telemetry_funnel_current
WHERE metric_key IN ('activation_funnel', 'app_studio_activation_funnel', 'agents_activation_funnel');

-- +goose Down

-- v6 changes only the session-timezone semantics of CURRENT_DATE. Retain UTC
-- semantics on rollback rather than deliberately reintroducing date drift.
SELECT 1;
