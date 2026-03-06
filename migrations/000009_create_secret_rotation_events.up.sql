CREATE TABLE secret_rotation_events (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    secret_path      TEXT NOT NULL,
    old_version      TEXT,
    new_version      TEXT,
    rotated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    affected_clients INT NOT NULL DEFAULT 0
);
