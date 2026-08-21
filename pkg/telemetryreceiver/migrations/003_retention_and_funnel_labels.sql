-- +goose Up

-- Bind uniqueness rows to their source event so catalog-specific retention
-- can remove both the raw event and every pseudonymous projection derived from
-- it. Existing funnel rows already carry the event type as funnel_step;
-- counters use their catalog selection (current pre-v3 counters each have one).
ALTER TABLE faros_telemetry_metric_uniques ADD COLUMN event_type TEXT;
UPDATE faros_telemetry_metric_uniques u
SET event_type = COALESCE(
    NULLIF(u.funnel_step, ''),
    (SELECT MIN(c.event_type)
     FROM faros_telemetry_metric_catalog c
     WHERE c.metric_key = u.metric_key)
);
ALTER TABLE faros_telemetry_metric_uniques ALTER COLUMN event_type SET NOT NULL;
ALTER TABLE faros_telemetry_metric_uniques DROP CONSTRAINT faros_telemetry_metric_uniques_pkey;
ALTER TABLE faros_telemetry_metric_uniques
    ADD PRIMARY KEY (bucket_start, metric_key, event_type, funnel_step, labels_key, tenant_id, unique_kind, unique_hash);

DROP VIEW faros_telemetry_activation_current;
DROP VIEW faros_telemetry_funnel_current;

CREATE VIEW faros_telemetry_funnel_current AS
SELECT c.metric_key, c.funnel_step, c.step_order, c.window_days,
       COALESCE(u.labels, '{}'::jsonb) AS labels,
       (COUNT(DISTINCT (u.tenant_id, u.unique_hash)) FILTER (WHERE u.unique_hash IS NOT NULL))::BIGINT AS value
FROM faros_telemetry_metric_catalog c
LEFT JOIN faros_telemetry_metric_uniques u
  ON u.metric_key = c.metric_key
 AND u.event_type = c.event_type
 AND u.funnel_step = c.funnel_step
 AND (c.window_days = 0 OR u.bucket_start >= CURRENT_DATE - (c.window_days - 1))
WHERE c.metric_kind = 'funnel'
GROUP BY c.metric_key, c.funnel_step, c.step_order, c.window_days, COALESCE(u.labels, '{}'::jsonb);

CREATE VIEW faros_telemetry_activation_current AS
SELECT metric_key, funnel_step, step_order, window_days, labels, value
FROM faros_telemetry_funnel_current
WHERE metric_key IN ('activation_funnel', 'app_studio_activation_funnel', 'agents_activation_funnel');

-- +goose Down

DROP VIEW faros_telemetry_activation_current;
DROP VIEW faros_telemetry_funnel_current;

ALTER TABLE faros_telemetry_metric_uniques DROP CONSTRAINT faros_telemetry_metric_uniques_pkey;
ALTER TABLE faros_telemetry_metric_uniques DROP COLUMN event_type;
ALTER TABLE faros_telemetry_metric_uniques
    ADD PRIMARY KEY (bucket_start, metric_key, funnel_step, labels_key, tenant_id, unique_kind, unique_hash);

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
