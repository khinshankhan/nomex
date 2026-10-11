-- +goose Up

-- NOTE: nullable columns omit NULL, sqlc's SQLite parser generates interface{}
-- instead of *time.Time with it.

-- checks is the cache and the queue. An unchecked domain and a lapsed answer
-- are both a row with fresh_until in the past.
CREATE TABLE checks (
  -- a non-INTEGER PRIMARY KEY accepts NULL in SQLite
  domain        TEXT NOT NULL PRIMARY KEY,
  status        TEXT NOT NULL CHECK (status IN ('unchecked','registered','not_found','unknown')),
  source        TEXT CHECK (source IS NULL OR source IN ('dns','rdap','both')),
  checked_at    DATETIME,
  -- past means due
  fresh_until   DATETIME NOT NULL,
  -- RDAP expiration event
  expiration    DATETIME,
  registered_at DATETIME,
  -- full answering URL, see servers.origin
  server        TEXT,
  -- bootstrap registry couldn't be refreshed
  stale         BOOLEAN NOT NULL DEFAULT 0,
  -- 100 live request | 50 expiring soon | 10 manual boost | 0 background sweep
  priority      INTEGER NOT NULL DEFAULT 0,
  queued_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,

  -- STORED since Postgres only supports STORED. Can't be written to, so writes
  -- list columns explicitly.
  -- NOTE: the first dot, so foo.co.uk gives co.uk, hence suffix not tld
  suffix    TEXT    GENERATED ALWAYS AS (substr(domain, instr(domain, '.') + 1)) STORED NOT NULL,
  label_len INTEGER GENERATED ALWAYS AS (instr(domain, '.') - 1) STORED NOT NULL
);

-- claim order across all suffixes. Leading with the ordering lets the scan stop
-- after LIMIT: 234us at 2M rows, against 611ms when it sorts every due row
CREATE INDEX idx_checks_work ON checks(priority DESC, queued_at, fresh_until);

-- eg four-letter .dev that aren't taken
CREATE INDEX idx_checks_filter ON checks(suffix, label_len, status);

-- failures land here, never in checks.status
CREATE TABLE attempts (
  domain       TEXT NOT NULL REFERENCES checks(domain) ON DELETE CASCADE,
  attempted_at DATETIME NOT NULL,
  -- rdap.ErrorKind
  error_kind   TEXT NOT NULL,
  retryable    BOOLEAN NOT NULL,
  -- the server's Retry-After, for diagnosis. The sweep reads fresh_until,
  -- which the writer pushes past it
  retry_after  DATETIME,
  PRIMARY KEY (domain, attempted_at)
);

-- newest first per domain
CREATE INDEX idx_attempts_recent ON attempts(domain, attempted_at DESC);

-- for pruning old rows
CREATE INDEX idx_attempts_age ON attempts(attempted_at);

-- only failures about the domain itself: no RDAP server for the suffix, a
-- malformed name, or a refusal
-- NOTE: not every non-retryable error, those also cover server problems
CREATE TABLE blocked (
  domain     TEXT NOT NULL PRIMARY KEY REFERENCES checks(domain) ON DELETE CASCADE,
  reason     TEXT NOT NULL,
  blocked_at DATETIME NOT NULL
);

-- per-server throttling state, shared by every worker
CREATE TABLE servers (
  -- scheme://host, not the answering URL
  origin               TEXT NOT NULL PRIMARY KEY,
  last_success         DATETIME,
  last_failure         DATETIME,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  rate_limited_until   DATETIME
);

-- +goose Down
DROP TABLE servers;
DROP TABLE blocked;
DROP TABLE attempts;
DROP TABLE checks;
