-- +goose Up

-- The CloudEvents tenant extension is the producing hub installation, not a
-- Faros organization. Name the erasure receipt scope accordingly so operators
-- cannot mistake an installation-wide deletion for organization erasure.
ALTER TABLE faros_telemetry_erasure_requests RENAME COLUMN tenant_id TO installation_id;

-- +goose Down

ALTER TABLE faros_telemetry_erasure_requests RENAME COLUMN installation_id TO tenant_id;
