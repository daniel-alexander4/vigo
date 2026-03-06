CREATE TABLE plan_results (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id        UUID NOT NULL REFERENCES plans (id),
    module_name    TEXT NOT NULL,
    resource_type  TEXT NOT NULL,
    resource_name  TEXT NOT NULL,
    planned_action TEXT NOT NULL
);
