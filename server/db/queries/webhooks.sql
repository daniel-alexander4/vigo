-- name: CreateWebhook :one
INSERT INTO webhooks (url, secret, events)
VALUES (?, ?, ?)
RETURNING id, url, secret, events, created_at, active;

-- name: ListActiveWebhooks :many
SELECT id, url, secret, events, created_at, active
FROM webhooks
WHERE active = true
ORDER BY created_at DESC;

-- name: GetWebhookByID :one
SELECT id, url, secret, events, created_at, active
FROM webhooks
WHERE id = ?;

-- name: DeleteWebhook :exec
DELETE FROM webhooks
WHERE id = ?;

-- name: DeactivateWebhook :exec
UPDATE webhooks
SET active = false
WHERE id = ?;
