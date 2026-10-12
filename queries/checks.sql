-- NOTE: stored timestamps are always compared through datetime(). SQLite
-- compares DATETIME as text and ignores a "-04:00" offset otherwise.

-- name: GetCheck :one
SELECT * FROM checks WHERE domain = ?;

-- name: UpsertCheck :exec
-- generated columns can't be named
INSERT INTO checks (domain, status, fresh_until, priority)
VALUES (?, ?, ?, ?)
ON CONFLICT(domain) DO UPDATE SET
  status      = excluded.status,
  fresh_until = excluded.fresh_until,
  priority    = MAX(checks.priority, excluded.priority);

-- name: SeedCheck :execrows
-- OR IGNORE so reseeding leaves existing rows alone. execrows reports 1 for a
-- new row and 0 for an existing one.
INSERT OR IGNORE INTO checks (domain, status, fresh_until, priority)
VALUES (?, 'unchecked', ?, ?);

-- name: Progress :many
-- how far the sweep has got, per suffix
SELECT
  suffix,
  -- CAST, sqlc types a bare SUM() of a boolean as *float64
  COUNT(*)                                                    AS total,
  CAST(SUM(status = 'unchecked') AS INTEGER)                  AS unchecked,
  CAST(SUM(status = 'registered') AS INTEGER)                 AS registered,
  CAST(SUM(status = 'not_found') AS INTEGER)                  AS available,
  CAST(SUM(status = 'unknown') AS INTEGER)                    AS unknown,
  CAST(SUM(datetime(fresh_until) < datetime('now')) AS INTEGER) AS due
FROM checks
GROUP BY suffix
ORDER BY suffix;

-- name: CountChecks :one
SELECT COUNT(*) FROM checks;

-- name: RecordAttempt :exec
INSERT INTO attempts (domain, attempted_at, error_kind, retryable, retry_after)
VALUES (?, ?, ?, ?, ?);

-- name: BlockDomain :exec
INSERT INTO blocked (domain, reason, blocked_at)
VALUES (?, ?, ?)
ON CONFLICT(domain) DO UPDATE SET reason = excluded.reason, blocked_at = excluded.blocked_at;

-- name: DeferCheck :exec
UPDATE checks SET fresh_until = ? WHERE domain = ?;

-- name: PruneAttempts :execrows
DELETE FROM attempts WHERE datetime(attempted_at) < datetime(CAST(sqlc.arg(before) AS TEXT));

-- name: RecentAttempts :many
SELECT * FROM attempts WHERE domain = ? ORDER BY attempted_at DESC LIMIT ?;

-- name: UpsertCheckResult :exec
-- generated columns can't be named
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
SELECT COUNT(*) FROM attempts
WHERE domain = ? AND datetime(attempted_at) > datetime(CAST(sqlc.arg(since) AS TEXT));

-- name: IsBlocked :one
SELECT EXISTS (SELECT 1 FROM blocked WHERE domain = ?);

-- name: ListChecks :many
-- every filter is optional, a zero value disables it
-- NOTE: the ORs mean it scans rather than using idx_checks_filter, fine for a
-- report
SELECT domain, status, checked_at, fresh_until, expiration,
       datetime(fresh_until) < datetime('now') AS stale
FROM checks
-- CAST, or sqlc generates interface{} through the OR
WHERE (CAST(sqlc.arg(status) AS TEXT)      = '' OR status    = sqlc.arg(status))
  AND (CAST(sqlc.arg(suffix) AS TEXT)      = '' OR suffix    = sqlc.arg(suffix))
  AND (CAST(sqlc.arg(label_len) AS INTEGER) = 0 OR label_len = sqlc.arg(label_len))
  AND (CAST(sqlc.arg(fresh_only) AS INTEGER) = 0 OR datetime(fresh_until) >= datetime('now'))
ORDER BY label_len, domain
LIMIT sqlc.arg(lim);

-- name: ServerState :one
SELECT * FROM servers WHERE origin = ?;

-- name: RecordServerSuccess :exec
INSERT INTO servers (origin, last_success, consecutive_failures)
VALUES (?, ?, 0)
ON CONFLICT(origin) DO UPDATE SET
  last_success         = excluded.last_success,
  consecutive_failures = 0,
  rate_limited_until   = NULL;

-- name: RecordServerFailure :exec
-- rate_limited_until only ever grows. NULLIF keeps two NULLs NULL, not ''
INSERT INTO servers (origin, last_failure, consecutive_failures, rate_limited_until)
VALUES (?, ?, 1, ?)
ON CONFLICT(origin) DO UPDATE SET
  last_failure         = excluded.last_failure,
  consecutive_failures = servers.consecutive_failures + 1,
  rate_limited_until   = NULLIF(MAX(
    COALESCE(servers.rate_limited_until, ''),
    COALESCE(excluded.rate_limited_until, '')
  ), '');

-- name: ThrottledServers :many
SELECT origin, rate_limited_until FROM servers
WHERE rate_limited_until IS NOT NULL
  AND datetime(rate_limited_until) > datetime('now');

-- name: DueSuffixes :many
-- suffixes with work, most due first
SELECT suffix, COUNT(*) AS due
FROM checks c
WHERE datetime(c.fresh_until) < datetime('now')
  AND NOT EXISTS (SELECT 1 FROM blocked b WHERE b.domain = c.domain)
GROUP BY suffix
ORDER BY due DESC;

-- name: DueChecksForSuffix :many
-- due rows for one suffix, live requests first then oldest
-- NOTE: failures defer fresh_until instead of the query reading retry_after,
-- which can't use the index
SELECT domain FROM checks c
WHERE c.suffix = ?
  AND datetime(c.fresh_until) < datetime('now')
  AND NOT EXISTS (SELECT 1 FROM blocked b WHERE b.domain = c.domain)
ORDER BY c.priority DESC, c.queued_at ASC
LIMIT ?;

-- name: CountAttempts :one
SELECT COUNT(*) FROM attempts;
