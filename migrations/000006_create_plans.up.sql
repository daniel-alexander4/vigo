CREATE TABLE plans (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id  UUID NOT NULL REFERENCES clients (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    modules    JSONB NOT NULL DEFAULT '[]'
);
