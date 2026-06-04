-- Remove the runtime compliance-waiver table. Waivers are config: operators
-- declare them in waivers.vgo, the single config path (CLAUDE.md "Single config
-- path: YAML files only"). The DB write endpoints were orphaned (no UI trigger,
-- no vigocli caller) and the read path was redundant with ConfigStore.Waivers.
DROP INDEX IF EXISTS idx_compliance_waivers_active;
DROP INDEX IF EXISTS idx_compliance_waivers_framework;
DROP TABLE IF EXISTS compliance_waivers;
