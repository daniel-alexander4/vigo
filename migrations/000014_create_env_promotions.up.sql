CREATE TABLE env_promotions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    module_name TEXT NOT NULL,
    from_env TEXT NOT NULL,
    to_env TEXT NOT NULL,
    diff JSONB NOT NULL,
    promoted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
