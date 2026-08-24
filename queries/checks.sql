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

-- name: ListChecks :many
-- The report query. Every filter is optional: passing the zero value for one
-- disables it, so "everything available" and "four-letter .dev that is not
-- taken" are the same query.
--
-- Deliberately not using idx_checks_filter -- a leading OR defeats it. This is
-- a human-triggered report over a table the sweep touches constantly, so a scan
-- is the right trade against maintaining several near-identical queries.
SELECT domain, status, checked_at, fresh_until, expiration,
       datetime(fresh_until) < datetime('now') AS stale
FROM checks
-- CAST so sqlc can infer a type: through a bare OR comparison it gives up and
-- generates interface{}.
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
  -- A success clears the streak and any throttle: the server is answering.
  consecutive_failures = 0,
  rate_limited_until   = NULL;

-- name: RecordServerFailure :exec
-- rate_limited_until is only extended, never shortened: a later failure with no
-- Retry-After must not cancel a longer wait the server explicitly asked for.
INSERT INTO servers (origin, last_failure, consecutive_failures, rate_limited_until)
VALUES (?, ?, 1, ?)
ON CONFLICT(origin) DO UPDATE SET
  last_failure         = excluded.last_failure,
  consecutive_failures = servers.consecutive_failures + 1,
  rate_limited_until   = MAX(
    COALESCE(servers.rate_limited_until, ''),
    COALESCE(excluded.rate_limited_until, '')
  );

-- name: ThrottledServers :many
-- Origins that asked us to wait, so the sweeper can skip their domains rather
-- than spending its rate budget on guaranteed failures.
SELECT origin, rate_limited_until FROM servers
WHERE rate_limited_until IS NOT NULL
  AND datetime(rate_limited_until) > datetime('now');
