-- +goose Up

CREATE TABLE IF NOT EXISTS faros_telemetry_events (
    tenant_id TEXT NOT NULL,
    source TEXT NOT NULL,
    event_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    event_time TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL,
    data_content_type TEXT NOT NULL,
    data JSONB NOT NULL,
    PRIMARY KEY (tenant_id, source, event_id)
);

CREATE INDEX IF NOT EXISTS faros_telemetry_events_tenant_received_idx
    ON faros_telemetry_events (tenant_id, received_at);

CREATE INDEX IF NOT EXISTS faros_telemetry_events_received_idx
    ON faros_telemetry_events (received_at);

CREATE TABLE IF NOT EXISTS faros_telemetry_aggregates (
    bucket_start TIMESTAMPTZ NOT NULL,
    source TEXT NOT NULL,
    event_type TEXT NOT NULL,
    event_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket_start, source, event_type)
);

CREATE TABLE IF NOT EXISTS faros_telemetry_erasure_requests (
    request_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    deleted_raw BIGINT NOT NULL
);

-- +goose Down

DROP TABLE IF EXISTS faros_telemetry_erasure_requests;
DROP TABLE IF EXISTS faros_telemetry_aggregates;
DROP TABLE IF EXISTS faros_telemetry_events;
