-- +goose Up

-- Nullable columns omit the NULL keyword: sqlc's SQLite parser generates
-- interface{} instead of *time.Time when it is present.

-- checks is the cache and the queue. Work is a staleness query -- an unchecked
-- domain is a row with fresh_until in the past, same as a lapsed result -- so
-- one code path serves both and resuming a sweep is free.
CREATE TABLE checks (
  -- NOT NULL because a non-INTEGER PRIMARY KEY accepts NULL in SQLite.
  domain        TEXT NOT NULL PRIMARY KEY,
  -- CHECK rather than a comment: a typo becomes a write error instead of a
  -- fifth status that nothing queries and nobody notices.
  status        TEXT NOT NULL CHECK (status IN ('unchecked','registered','not_found','unknown')),
  source        TEXT CHECK (source IS NULL OR source IN ('dns','rdap','both')),
  checked_at    DATETIME,
  -- past means "work to do"
  fresh_until   DATETIME NOT NULL,
  -- RDAP expiration event
  expiration    DATETIME,
  registered_at DATETIME,
  -- full answering URL, for provenance. see servers.origin
  server        TEXT,
  -- rdap.Result.Stale: bootstrap registry could not be refreshed
  stale         BOOLEAN NOT NULL DEFAULT 0,
  -- 100 live request | 50 expiring soon | 10 manual boost | 0 background sweep
  priority      INTEGER NOT NULL DEFAULT 0,
  queued_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- STORED, not VIRTUAL: Postgres supports only STORED. Cannot be added by
  -- ALTER TABLE, cannot be changed in place, cannot be written to -- so writes
  -- must list columns explicitly.
  --
  -- instr finds the first dot, so foo.co.uk yields co.uk. Hence suffix, not tld.
  suffix    TEXT    GENERATED ALWAYS AS (substr(domain, instr(domain, '.') + 1)) STORED NOT NULL,
  label_len INTEGER GENERATED ALWAYS AS (instr(domain, '.') - 1) STORED NOT NULL
);

-- Scheduler read:
--   SELECT domain FROM checks WHERE fresh_until < datetime('now')
--   ORDER BY priority DESC, queued_at ASC LIMIT ?;
--
-- Ordering first, deliberately. Leading with fresh_until gives an indexed range
-- scan but then sorts every due row -- LIMIT does not bound that sort, so with
-- a freshly seeded table (everything due) it degrades with table size:
-- 53ms at 100k rows, 611ms at 2M, and LIMIT 100 costs the same as LIMIT 1000.
--
-- Leading with the ordering lets the scan stop after LIMIT rows. fresh_until
-- becomes a filter during the walk rather than a seek, which measured faster in
-- every case tried, including the steady state where few rows are due:
-- 234us at 2M rows, flat as the table grows.
CREATE INDEX idx_checks_work ON checks(priority DESC, queued_at, fresh_until);

-- Filter read: "four-letter .dev domains that are not taken".
CREATE INDEX idx_checks_filter ON checks(suffix, label_len, status);

-- Failures land here, not in checks, so a timeout cannot become an answer.
-- That conflation is what banned 42k domains in November.
CREATE TABLE attempts (
  domain       TEXT NOT NULL REFERENCES checks(domain) ON DELETE CASCADE,
  attempted_at DATETIME NOT NULL,
  -- rdap.ErrorKind
  error_kind   TEXT NOT NULL,
  retryable    BOOLEAN NOT NULL,
  -- from the server's Retry-After. Recorded for diagnosis only: the scheduler
  -- reads fresh_until, which the writer pushes past this instant, because
  -- consulting attempts from the sweep query measured 1460x slower.
  retry_after  DATETIME,
  PRIMARY KEY (domain, attempted_at)
);

-- Newest-first per domain: "why did this keep failing" and any retention sweep
-- both read this way, and the primary key sorts attempted_at ascending.
CREATE INDEX idx_attempts_recent ON attempts(domain, attempted_at DESC);

-- For deleting old rows. attempts grows without bound otherwise -- one row per
-- transient failure, forever -- and at sweep volume becomes the largest table.
CREATE INDEX idx_attempts_age ON attempts(attempted_at);

-- Written from an allowlist, NOT from !rdap.IsRetryable(err). Non-retryable
-- also covers ErrUnknown (the zero value), ErrTooManyRedirects,
-- ErrRedirectRefused, ErrRedirectLoop and ErrNotSupported -- all facts about a
-- server, not the domain.
--
-- Only these three describe the domain:
--   ErrNoServer     -- no RDAP server published for this suffix
--   ErrInvalidQuery -- malformed name
--   ErrRefused      -- deliberate rejection; retrying makes it worse
CREATE TABLE blocked (
  domain     TEXT NOT NULL PRIMARY KEY REFERENCES checks(domain) ON DELETE CASCADE,
  reason     TEXT NOT NULL,
  blocked_at DATETIME NOT NULL
);

-- Per-server throttling state. A rate limit applies to every query against that
-- server, and without somewhere to put it that knowledge dies with the worker.
CREATE TABLE servers (
  -- Origin (scheme://host), not the answering URL -- rdap.Result.Server
  -- includes the object path, which would give one row per domain.
  origin               TEXT NOT NULL PRIMARY KEY,
  last_success         DATETIME,
  last_failure         DATETIME,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  rate_limited_until   DATETIME,
  -- a 501 is permanent per server
  supports_search      BOOLEAN
);

-- +goose Down
DROP TABLE servers;
DROP TABLE blocked;
DROP TABLE attempts;
DROP TABLE checks;
