-- +goose Up

-- Nullable columns omit the NULL keyword: sqlc's SQLite parser generates
-- interface{} instead of *time.Time when it is present.

-- checks is the cache and the queue. Work is a staleness query -- an unchecked
-- domain is a row with fresh_until in the past, same as a lapsed result -- so
-- one code path serves both and resuming a sweep is free.
CREATE TABLE checks (
  -- NOT NULL because a non-INTEGER PRIMARY KEY accepts NULL in SQLite.
  domain        TEXT NOT NULL PRIMARY KEY,
  -- unchecked | registered | not_found | unknown
  status        TEXT NOT NULL,
  -- dns | rdap | both
  source        TEXT,
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
-- Indexed range scan on fresh_until, then a sort -- SQLite reports USE TEMP
-- B-TREE FOR ORDER BY. A range scan cannot also provide the ordering. Fine at
-- LIMIT; revisit if the due set grows.
CREATE INDEX idx_checks_work ON checks(fresh_until, priority DESC, queued_at);

-- Filter read: "four-letter .dev domains that are not taken".
CREATE INDEX idx_checks_filter ON checks(suffix, label_len, status);

-- Failures land here, not in checks, so a timeout cannot become an answer.
-- That conflation is what banned 42k domains in November.
CREATE TABLE attempts (
  domain       TEXT NOT NULL,
  attempted_at DATETIME NOT NULL,
  -- rdap.ErrorKind
  error_kind   TEXT NOT NULL,
  retryable    BOOLEAN NOT NULL,
  -- from the server's Retry-After
  retry_after  DATETIME,
  PRIMARY KEY (domain, attempted_at)
);

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
  domain     TEXT NOT NULL PRIMARY KEY,
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
