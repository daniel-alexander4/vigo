CREATE TABLE runs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id     UUID NOT NULL REFERENCES clients (id),
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ,
    status        TEXT NOT NULL CHECK (status IN ('running', 'success', 'failure', 'partial')),
    module_count  INT NOT NULL DEFAULT 0,
    changed_count INT NOT NULL DEFAULT 0,
    failed_count  INT NOT NULL DEFAULT 0,
    skipped_count INT NOT NULL DEFAULT 0
);

CREATE INDEX idx_runs_client_id ON runs (client_id);
