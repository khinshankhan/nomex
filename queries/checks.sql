-- Every comparison of a stored timestamp wraps the column in datetime().
-- SQLite compares DATETIME text lexicographically and does not interpret the
-- "-04:00" a non-UTC time carries, so a raw comparison reads a timestamp an
-- hour in the future as hours stale. datetime() parses the offset.

-- name: DueChecks :many
-- Work is a staleness query: fresh_until in the past means "check this".
--
-- blocked is excluded here rather than by deleting the row, so a domain that
-- becomes servable again (a new RDAP endpoint published for its suffix) needs
-- one DELETE rather than a re-seed. Measured at 1M rows: 65us without the
-- exclusion, 8.65ms with it -- irrelevant against a rate limit measured in
-- queries per second.
--
-- retry_after is deliberately NOT consulted. Doing so measured 95ms, 1460x the
-- unfiltered query, because it cannot use the ordering index. The writer pushes
-- fresh_until past the retry instant instead, which costs nothing extra.
SELECT domain FROM checks c
WHERE datetime(c.fresh_until) < datetime('now')
  AND NOT EXISTS (SELECT 1 FROM blocked b WHERE b.domain = c.domain)
ORDER BY c.priority DESC, c.queued_at ASC
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

-- name: RecordAttempt :exec
INSERT INTO attempts (domain, attempted_at, error_kind, retryable, retry_after)
VALUES (?, ?, ?, ?, ?);

-- name: BlockDomain :exec
-- Only for the three kinds that describe the domain: ErrNoServer,
-- ErrInvalidQuery, ErrRefused. Everything else goes to attempts.
INSERT INTO blocked (domain, reason, blocked_at)
VALUES (?, ?, ?)
ON CONFLICT(domain) DO UPDATE SET reason = excluded.reason, blocked_at = excluded.blocked_at;

-- name: DeferCheck :exec
-- Push a row past a failure so the sweep stops returning it. This is what
-- stands in for consulting attempts.retry_after in the scheduler query.
UPDATE checks SET fresh_until = ? WHERE domain = ?;

-- name: PruneAttempts :execrows
DELETE FROM attempts WHERE datetime(attempted_at) < datetime(CAST(sqlc.arg(before) AS TEXT));

-- name: RecentAttempts :many
SELECT * FROM attempts WHERE domain = ? ORDER BY attempted_at DESC LIMIT ?;

-- name: UpsertCheckResult :exec
-- The full result path. Generated columns are omitted: naming one is an error.
INSERT INTO checks (domain, status, source, checked_at, fresh_until,
                    expiration, registered_at, server, stale)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(domain) DO UPDATE SET
  status        = excluded.status,
  source        = excluded.source,
  checked_at    = excluded.checked_at,
  fresh_until   = excluded.fresh_until,
  expiration    = excluded.expiration,
  registered_at = excluded.registered_at,
  server        = excluded.server,
  stale         = excluded.stale;

-- name: CountRecentFailures :one
-- Drives the backoff exponent. Bounded by time rather than counting the whole
-- history, so a domain that failed last year starts fresh.
SELECT COUNT(*) FROM attempts
WHERE domain = ? AND datetime(attempted_at) > datetime(CAST(sqlc.arg(since) AS TEXT));

-- name: IsBlocked :one
SELECT EXISTS (SELECT 1 FROM blocked WHERE domain = ?);
