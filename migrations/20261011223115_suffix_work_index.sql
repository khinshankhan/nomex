-- +goose Up

-- The sweep claims work per suffix, so the claim needs an index that leads with
-- suffix and then the claim's order. Without it the query sorts every due row
-- of the suffix before applying LIMIT: 270-350ms per batch at 2M rows, against
-- 0.5ms with it.
CREATE INDEX idx_checks_suffix_work ON checks(suffix, priority DESC, queued_at, fresh_until);

-- +goose Down
DROP INDEX idx_checks_suffix_work;
