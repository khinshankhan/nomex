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

-- name: UpsertCheck :exec
-- Generated columns are omitted deliberately: naming one is an error.
INSERT INTO checks (domain, status, fresh_until, priority)
VALUES (?, ?, ?, ?)
ON CONFLICT(domain) DO UPDATE SET
  status      = excluded.status,
  fresh_until = excluded.fresh_until,
  priority    = MAX(checks.priority, excluded.priority);

-- name: SeedCheck :execrows
-- OR IGNORE so re-running a narrower seed over a wider one is a no-op per row
-- rather than resetting status or priority on already-checked domains.
--
-- execrows rather than exec: the rows-affected count is 1 for a new row and 0
-- for one that was ignored, which is how the seeder reports what it added
-- without counting the table before and after every batch.
INSERT OR IGNORE INTO checks (domain, status, fresh_until, priority)
VALUES (?, 'unchecked', ?, ?);

-- name: CountBySuffixLen :many
SELECT suffix, label_len, status, COUNT(*) AS n
FROM checks
GROUP BY suffix, label_len, status
ORDER BY suffix, label_len, status;

-- name: CountChecks :one
SELECT COUNT(*) FROM checks;
