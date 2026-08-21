-- +goose Up

-- A durable tombstone prevents receiver retries or compromised old producers
-- from recreating pseudonymous data after an installation erasure completes.
CREATE TABLE faros_telemetry_erased_installations (
    installation_id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL,
    erased_at TIMESTAMPTZ NOT NULL
);

INSERT INTO faros_telemetry_erased_installations (installation_id, request_id, erased_at)
SELECT installation_id, MIN(request_id), MIN(created_at)
FROM faros_telemetry_erasure_requests
GROUP BY installation_id
ON CONFLICT (installation_id) DO NOTHING;

-- +goose Down

DROP TABLE IF EXISTS faros_telemetry_erased_installations;
