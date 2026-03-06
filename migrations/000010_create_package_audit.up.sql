CREATE TABLE package_audit (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id         UUID NOT NULL REFERENCES clients (id),
    package_name      TEXT NOT NULL,
    installed_version TEXT,
    desired_version   TEXT,
    detected_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
