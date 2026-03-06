-- name: CreateWebhook :one
INSERT INTO webhooks (url, secret, events)
VALUES ($1, $2, $3)
RETURNING id, url, secret, events, created_at, active;

-- name: ListActiveWebhooks :many
SELECT id, url, secret, events, created_at, active
FROM webhooks
WHERE active = true
ORDER BY created_at DESC;

-- name: ListActiveWebhooksByEvent :many
SELECT id, url, secret, events, created_at, active
FROM webhooks
WHERE active = true AND $1::text = ANY(events);

-- name: GetWebhookByID :one
SELECT id, url, secret, events, created_at, active
FROM webhooks
WHERE id = $1;

-- name: DeleteWebhook :exec
DELETE FROM webhooks
WHERE id = $1;

-- name: DeactivateWebhook :exec
UPDATE webhooks
SET active = false
WHERE id = $1;
