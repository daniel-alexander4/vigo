CREATE TABLE compliance_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    recorded_at DATE NOT NULL DEFAULT CURRENT_DATE,
    total INT NOT NULL DEFAULT 0,
    compliant INT NOT NULL DEFAULT 0,
    drifted INT NOT NULL DEFAULT 0,
    failing INT NOT NULL DEFAULT 0,
    stale INT NOT NULL DEFAULT 0,
    unknown INT NOT NULL DEFAULT 0,
    UNIQUE(recorded_at)
);
