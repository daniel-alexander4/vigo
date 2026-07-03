-- The webhooks feature was removed in favor of native integrations
-- (server/integrations/, configured in server.yaml). Drop the orphaned table;
-- its RPCs, queries, and generated model are removed in the same change.
DROP TABLE IF EXISTS webhooks;
