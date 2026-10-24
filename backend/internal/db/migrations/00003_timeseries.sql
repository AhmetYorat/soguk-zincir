-- +goose Up
CREATE TABLE sensors (
    id             int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code           text        NOT NULL UNIQUE,
    location_id    bigint      NOT NULL REFERENCES locations (id),
    -- per-device HMAC key, encrypted at rest with AES-GCM (DEVICE_KEY_ENC_KEY)
    key_ciphertext bytea       NOT NULL,
    key_nonce      bytea       NOT NULL,
    -- highest accepted message sequence; anything <= is a replay
    last_seq       bigint      NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    last_seen_at   timestamptz,
    battery_pct    smallint CHECK (battery_pct BETWEEN 0 AND 100),
    rejected_count bigint      NOT NULL DEFAULT 0,
    active         boolean     NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sensors_location_idx ON sensors (location_id);

-- Raw readings: 2 000 sensors x every 30 s = 5.76 M rows/day.
-- Daily range partitions, dropped after the raw retention period.
CREATE TABLE readings (
    sensor_id   int         NOT NULL REFERENCES sensors (id),
    ts          timestamptz NOT NULL,
    temp_cc     smallint    NOT NULL,
    humidity_pm smallint    NOT NULL CHECK (humidity_pm BETWEEN 0 AND 1000),
    PRIMARY KEY (sensor_id, ts)
) PARTITION BY RANGE (ts);

-- Rollups keep sum + count (not avg) so that coarser levels can be built from
-- finer ones exactly: 1m -> 15m -> 1h.
CREATE TABLE readings_1m (
    sensor_id    int         NOT NULL,
    bucket       timestamptz NOT NULL,
    n            int         NOT NULL CHECK (n > 0),
    temp_min_cc  smallint    NOT NULL,
    temp_max_cc  smallint    NOT NULL,
    temp_sum_cc  bigint      NOT NULL,
    hum_sum_pm   bigint      NOT NULL,
    PRIMARY KEY (sensor_id, bucket)
) PARTITION BY RANGE (bucket);

CREATE TABLE readings_15m (LIKE readings_1m INCLUDING ALL) PARTITION BY RANGE (bucket);
CREATE TABLE readings_1h  (LIKE readings_1m INCLUDING ALL) PARTITION BY RANGE (bucket);

-- Each level is complete up to (exclusive) its watermark.
CREATE TABLE rollup_watermarks (
    level text PRIMARY KEY CHECK (level IN ('1m', '15m', '1h')),
    upto  timestamptz NOT NULL
);

-- +goose Down
DROP TABLE rollup_watermarks;
DROP TABLE readings_1h;
DROP TABLE readings_15m;
DROP TABLE readings_1m;
DROP TABLE readings;
DROP TABLE sensors;
