CREATE TABLE file_snapshots (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id    UUID NOT NULL REFERENCES clients (id),
    file_path    TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    content      BYTEA,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    run_id       UUID REFERENCES runs (id)
);
