-- 0001_init: core schema for TitanEdge.

CREATE TABLE IF NOT EXISTS items (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    payload     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS items_created_at_idx ON items (created_at DESC);

-- Per-minute event counters written by the Kafka worker in batches.
CREATE TABLE IF NOT EXISTS event_rollups (
    bucket      TIMESTAMPTZ NOT NULL,
    event_type  TEXT        NOT NULL,
    count       BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, event_type)
);

INSERT INTO items (name, payload)
SELECT 'seed-' || g, jsonb_build_object('seed', true, 'n', g)
FROM generate_series(1, 1000) AS g
WHERE NOT EXISTS (SELECT 1 FROM items);
