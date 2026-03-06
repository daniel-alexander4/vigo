CREATE TABLE clients (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    hostname    TEXT NOT NULL,
    ip_address  TEXT NOT NULL,
    pubkey_pem  TEXT NOT NULL,
    enrolled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen   TIMESTAMPTZ,
    revoked     BOOLEAN NOT NULL DEFAULT false,
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX idx_clients_hostname ON clients (hostname);

-- Add foreign key from approved_patterns.used_by -> clients.id
ALTER TABLE approved_patterns
    ADD CONSTRAINT fk_approved_patterns_used_by
    FOREIGN KEY (used_by) REFERENCES clients (id);
