CREATE TABLE run_results (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id         UUID NOT NULL REFERENCES runs (id),
    module_name    TEXT NOT NULL,
    resource_type  TEXT NOT NULL,
    resource_name  TEXT NOT NULL,
    action_taken   TEXT NOT NULL,
    changed        BOOLEAN NOT NULL DEFAULT false,
    error_message  TEXT,
    duration_ms    INT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_run_results_run_id ON run_results (run_id);
