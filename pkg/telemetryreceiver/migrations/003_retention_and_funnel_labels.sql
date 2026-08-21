-- +goose Up

-- Migration 002 briefly shipped on this branch in three shapes: without
-- event_type anywhere, with it only in metric_catalog, and with it in both
-- catalog and uniqueness rows. IF NOT EXISTS plus a deterministic backfill
-- keeps all three upgrade paths valid.
ALTER TABLE faros_telemetry_metric_uniques ADD COLUMN IF NOT EXISTS event_type TEXT;
UPDATE faros_telemetry_metric_uniques
SET event_type = COALESCE(
    event_type,
    NULLIF(funnel_step, ''),
    CASE metric_key
      WHEN 'organization_created_total' THEN 'organization_created'
      WHEN 'workspace_created_total' THEN 'workspace_created'
      WHEN 'provider_enabled_total' THEN 'provider_enabled'
      WHEN 'edge_first_ready_total' THEN 'edge_first_ready'
      WHEN 'app_studio_project_created_total' THEN 'app_studio_project_created'
      WHEN 'app_studio_preview_ready_total' THEN 'app_studio_preview_ready'
      WHEN 'app_studio_project_published_total' THEN 'app_studio_project_published'
      WHEN 'agents_agent_created_total' THEN 'agents_agent_created'
      WHEN 'agents_run_terminal_total' THEN 'agents_run_terminal'
      ELSE metric_key
    END
)
WHERE event_type IS NULL;
ALTER TABLE faros_telemetry_metric_uniques ALTER COLUMN event_type SET NOT NULL;
ALTER TABLE faros_telemetry_metric_uniques DROP CONSTRAINT faros_telemetry_metric_uniques_pkey;
ALTER TABLE faros_telemetry_metric_uniques
    ADD PRIMARY KEY (bucket_start, metric_key, event_type, funnel_step, labels_key, tenant_id, unique_kind, unique_hash);

DROP VIEW faros_telemetry_activation_current;
DROP VIEW faros_telemetry_funnel_current;
DROP VIEW faros_telemetry_counter_current;

-- Old catalog rows cannot be recovered generically when the old schema did
-- not retain event_type. Clear them transactionally; SyncCatalog repopulates
-- the active generated plan immediately after migrations complete.
DELETE FROM faros_telemetry_metric_catalog;
ALTER TABLE faros_telemetry_metric_catalog ADD COLUMN IF NOT EXISTS event_type TEXT;
ALTER TABLE faros_telemetry_metric_catalog ALTER COLUMN event_type SET NOT NULL;
ALTER TABLE faros_telemetry_metric_catalog DROP CONSTRAINT faros_telemetry_metric_catalog_pkey;
ALTER TABLE faros_telemetry_metric_catalog
    ADD PRIMARY KEY (metric_key, funnel_step, event_type);

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

-- Refuse a lossy rollback if v3 has stored rows that the v2 keys cannot
-- represent without collapsing distinct event selections.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM faros_telemetry_metric_uniques
        GROUP BY bucket_start, metric_key, funnel_step, labels_key, tenant_id, unique_kind, unique_hash
        HAVING COUNT(*) > 1
    ) OR EXISTS (
        SELECT 1 FROM faros_telemetry_metric_catalog
        GROUP BY metric_key, funnel_step
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot safely roll back telemetry migration 003: event_type distinguishes existing rows';
    END IF;
END $$;
-- +goose StatementEnd

DROP VIEW faros_telemetry_activation_current;
DROP VIEW faros_telemetry_funnel_current;
DROP VIEW faros_telemetry_counter_current;

ALTER TABLE faros_telemetry_metric_uniques DROP CONSTRAINT faros_telemetry_metric_uniques_pkey;
ALTER TABLE faros_telemetry_metric_uniques DROP COLUMN event_type;
ALTER TABLE faros_telemetry_metric_uniques
    ADD PRIMARY KEY (bucket_start, metric_key, funnel_step, labels_key, tenant_id, unique_kind, unique_hash);

ALTER TABLE faros_telemetry_metric_catalog DROP CONSTRAINT faros_telemetry_metric_catalog_pkey;
ALTER TABLE faros_telemetry_metric_catalog DROP COLUMN event_type;
ALTER TABLE faros_telemetry_metric_catalog
    ADD PRIMARY KEY (metric_key, funnel_step);

CREATE VIEW faros_telemetry_counter_current AS
SELECT c.metric_key, a.labels, SUM(a.value)::BIGINT AS value, c.window_days
FROM faros_telemetry_metric_catalog c
JOIN faros_telemetry_metric_aggregates a
  ON a.metric_key = c.metric_key
 AND a.funnel_step = c.funnel_step
 AND (c.window_days = 0 OR a.bucket_start >= (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date - (c.window_days - 1))
WHERE c.metric_kind = 'counter'
GROUP BY c.metric_key, a.labels, c.window_days;

CREATE VIEW faros_telemetry_funnel_current AS
SELECT c.metric_key, c.funnel_step, c.step_order, c.window_days,
       (COUNT(DISTINCT (u.tenant_id, u.unique_hash)) FILTER (WHERE u.unique_hash IS NOT NULL))::BIGINT AS value
FROM faros_telemetry_metric_catalog c
LEFT JOIN faros_telemetry_metric_uniques u
  ON u.metric_key = c.metric_key
 AND u.funnel_step = c.funnel_step
 AND (c.window_days = 0 OR u.bucket_start >= (CURRENT_TIMESTAMP AT TIME ZONE 'UTC')::date - (c.window_days - 1))
WHERE c.metric_kind = 'funnel'
GROUP BY c.metric_key, c.funnel_step, c.step_order, c.window_days;

CREATE VIEW faros_telemetry_activation_current AS
SELECT metric_key, funnel_step, step_order, window_days, value
FROM faros_telemetry_funnel_current
WHERE metric_key IN ('activation_funnel', 'app_studio_activation_funnel', 'agents_activation_funnel');
