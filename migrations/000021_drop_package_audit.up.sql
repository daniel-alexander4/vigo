-- 000021: drop the dead package_audit table.
--
-- package_audit (migration 000001) was never written to — no INSERT path ever
-- existed in the codebase, so it has been permanently empty since inception.
-- It backed a read surface (the ListPackages/ListPackageDrift VigoAdmin RPCs,
-- the `vigocli traits packages` commands, an AI tool, and dead UI handler code)
-- that always rendered empty; all of that is removed alongside this. Live
-- package inventory flows through the `packages.installed` trait
-- (envoy_traits), which is unaffected.
DROP TABLE IF EXISTS package_audit;
