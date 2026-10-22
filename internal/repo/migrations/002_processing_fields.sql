-- +goose Up
ALTER TABLE avatars
    ADD COLUMN IF NOT EXISTS width INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS height INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS etag VARCHAR(64) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_avatars_pending ON avatars(processing_status)
    WHERE processing_status IN ('pending', 'processing');

CREATE TABLE IF NOT EXISTS processed_events (
    event_id UUID PRIMARY KEY,
    event_type VARCHAR(100) NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS processed_events;
DROP INDEX IF EXISTS idx_avatars_pending;
ALTER TABLE avatars
    DROP COLUMN IF EXISTS etag,
    DROP COLUMN IF EXISTS height,
    DROP COLUMN IF EXISTS width;
