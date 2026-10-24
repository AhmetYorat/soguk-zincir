-- +goose Up
-- Readings arrive time-ordered across 2 000 sensors, so one sensor's rows end
-- up on as many heap pages as it has rows. Once a partition is closed (no
-- more late data can arrive) it is rewritten in (sensor_id, ts) order with
-- CLUSTER; this table remembers which partitions are done.
CREATE TABLE partition_compactions (
    partition    text        PRIMARY KEY,
    compacted_at timestamptz NOT NULL DEFAULT now(),
    duration_ms  int         NOT NULL
);

-- +goose Down
DROP TABLE partition_compactions;
