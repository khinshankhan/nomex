-- name: DueChecks :many
SELECT domain FROM checks
WHERE fresh_until < datetime('now')
ORDER BY priority DESC, queued_at ASC
LIMIT ?;

-- name: FilterChecks :many
SELECT domain FROM checks
WHERE suffix = ? AND label_len = ? AND status = ?;

-- name: GetCheck :one
SELECT * FROM checks WHERE domain = ?;
