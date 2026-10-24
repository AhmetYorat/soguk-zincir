-- +goose Up
CREATE TYPE shipment_status AS ENUM ('planned', 'loading', 'in_transit', 'delivered', 'cancelled');

CREATE TABLE shipments (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code              text            NOT NULL UNIQUE,
    vehicle_id        bigint          NOT NULL REFERENCES vehicles (id),
    customer_id       bigint          NOT NULL REFERENCES customers (id),
    origin_id         bigint          NOT NULL REFERENCES locations (id),
    status            shipment_status NOT NULL DEFAULT 'planned',
    planned_departure timestamptz     NOT NULL,
    -- expected door-to-door time; lots with less remaining shelf life are not loaded
    transit_s         int             NOT NULL CHECK (transit_s > 0),
    created_by        bigint          NOT NULL REFERENCES users (id),
    created_at        timestamptz     NOT NULL DEFAULT now(),
    departed_at       timestamptz,
    delivered_at      timestamptz
);
CREATE INDEX shipments_customer_idx ON shipments (customer_id, id);
CREATE INDEX shipments_vehicle_idx ON shipments (vehicle_id, status);

CREATE TABLE shipment_items (
    shipment_id bigint      NOT NULL REFERENCES shipments (id),
    unit_id     bigint      NOT NULL UNIQUE REFERENCES units (id),
    loaded_at   timestamptz NOT NULL DEFAULT now(),
    loaded_by   bigint      NOT NULL REFERENCES users (id),
    PRIMARY KEY (shipment_id, unit_id)
);

CREATE TYPE alarm_rule AS ENUM ('limit_high', 'limit_low', 'cumulative', 'window', 'gap');
CREATE TYPE alarm_status AS ENUM ('open', 'acknowledged', 'closed');

CREATE TABLE alarms (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dedup_key         text         NOT NULL,
    sensor_id         int          NOT NULL REFERENCES sensors (id),
    location_id       bigint       NOT NULL REFERENCES locations (id),
    product_id        bigint REFERENCES products (id),
    rule              alarm_rule   NOT NULL,
    status            alarm_status NOT NULL DEFAULT 'open',
    escalation_level  smallint     NOT NULL DEFAULT 0 CHECK (escalation_level BETWEEN 0 AND 3),
    peak_cc           smallint,
    excursion_s       int          NOT NULL DEFAULT 0,
    message           text         NOT NULL,
    opened_at         timestamptz  NOT NULL DEFAULT now(),
    last_seen_at      timestamptz  NOT NULL DEFAULT now(),
    escalated_at      timestamptz,
    acknowledged_at   timestamptz,
    acknowledged_by   bigint REFERENCES users (id),
    closed_at         timestamptz,
    CONSTRAINT alarms_ack CHECK ((status = 'open') OR acknowledged_at IS NOT NULL OR closed_at IS NOT NULL)
);
-- one live alarm per condition: the evaluator upserts against this
CREATE UNIQUE INDEX alarms_dedup_live_uq ON alarms (dedup_key) WHERE status <> 'closed';
CREATE INDEX alarms_status_idx ON alarms (status, id DESC);
CREATE INDEX alarms_location_idx ON alarms (location_id, opened_at);

CREATE TABLE nonconformities (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lot_id      bigint      NOT NULL REFERENCES lots (id),
    alarm_id    bigint REFERENCES alarms (id),
    description text        NOT NULL CHECK (length(description) >= 3),
    status      text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    opened_by   bigint      NOT NULL REFERENCES users (id),
    opened_at   timestamptz NOT NULL DEFAULT now(),
    closed_at   timestamptz
);
CREATE INDEX nonconformities_lot_idx ON nonconformities (lot_id);

CREATE TYPE quality_decision AS ENUM ('accept', 'discount', 'destroy');

CREATE TABLE quality_decisions (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    lot_id            bigint           NOT NULL REFERENCES lots (id),
    nonconformity_id  bigint REFERENCES nonconformities (id),
    decision          quality_decision NOT NULL,
    discount_pct      smallint         NOT NULL DEFAULT 0,
    reason            text             NOT NULL CHECK (length(reason) >= 3),
    decided_by        bigint           NOT NULL REFERENCES users (id),
    decided_at        timestamptz      NOT NULL DEFAULT now(),
    CONSTRAINT quality_discount CHECK (
        (decision = 'discount' AND discount_pct BETWEEN 1 AND 99) OR
        (decision <> 'discount' AND discount_pct = 0)
    )
);
CREATE INDEX quality_decisions_lot_idx ON quality_decisions (lot_id);

CREATE TABLE haccp_checklists (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code      text NOT NULL UNIQUE,
    name      text NOT NULL,
    frequency text NOT NULL CHECK (frequency IN ('per_shift', 'daily', 'per_shipment'))
);

CREATE TABLE haccp_items (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    checklist_id bigint NOT NULL REFERENCES haccp_checklists (id),
    position     int    NOT NULL,
    ccp_code     text   NOT NULL,
    question     text   NOT NULL,
    kind         text   NOT NULL CHECK (kind IN ('boolean', 'temperature')),
    min_cc       int,
    max_cc       int,
    UNIQUE (checklist_id, position),
    CONSTRAINT haccp_item_limits CHECK ((kind = 'temperature') = (min_cc IS NOT NULL AND max_cc IS NOT NULL))
);

CREATE TABLE haccp_records (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    checklist_id bigint      NOT NULL REFERENCES haccp_checklists (id),
    location_id  bigint      NOT NULL REFERENCES locations (id),
    performed_by bigint      NOT NULL REFERENCES users (id),
    performed_at timestamptz NOT NULL DEFAULT now(),
    all_ok       boolean     NOT NULL,
    notes        text        NOT NULL DEFAULT ''
);
CREATE INDEX haccp_records_idx ON haccp_records (checklist_id, id DESC);

CREATE TABLE haccp_answers (
    record_id         bigint  NOT NULL REFERENCES haccp_records (id),
    item_id           bigint  NOT NULL REFERENCES haccp_items (id),
    bool_value        boolean,
    temp_cc           int,
    ok                boolean NOT NULL,
    corrective_action text,
    PRIMARY KEY (record_id, item_id),
    -- a failed control point must document what was done about it
    CONSTRAINT haccp_corrective CHECK (ok OR length(coalesce(corrective_action, '')) >= 3)
);

CREATE TABLE recalls (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code               text        NOT NULL UNIQUE,
    lot_id             bigint      NOT NULL REFERENCES lots (id),
    reason             text        NOT NULL CHECK (length(reason) >= 3),
    initiated_by       bigint      NOT NULL REFERENCES users (id),
    initiated_at       timestamptz NOT NULL DEFAULT now(),
    affected_units     int         NOT NULL,
    affected_customers int         NOT NULL,
    query_ms           int         NOT NULL
);

CREATE TABLE recall_items (
    recall_id   bigint NOT NULL REFERENCES recalls (id),
    unit_id     bigint NOT NULL REFERENCES units (id),
    shipment_id bigint REFERENCES shipments (id),
    customer_id bigint REFERENCES customers (id),
    PRIMARY KEY (recall_id, unit_id)
);

-- Events captured by the driver app, possibly offline. client_event_id makes
-- re-sync idempotent.
CREATE TABLE driver_events (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    client_event_id uuid        NOT NULL UNIQUE,
    driver_id       bigint      NOT NULL REFERENCES users (id),
    shipment_id     bigint      NOT NULL REFERENCES shipments (id),
    kind            text        NOT NULL CHECK (kind IN ('delivery', 'temperature_check')),
    recorded_at     timestamptz NOT NULL,
    received_at     timestamptz NOT NULL DEFAULT now(),
    temp_cc         int,
    receiver_name   text,
    note            text        NOT NULL DEFAULT '',
    outcome         text        NOT NULL CHECK (outcome IN ('applied', 'duplicate_delivery', 'rejected')),
    outcome_reason  text        NOT NULL DEFAULT ''
);
CREATE INDEX driver_events_shipment_idx ON driver_events (shipment_id, recorded_at);

-- +goose Down
DROP TABLE driver_events;
DROP TABLE recall_items;
DROP TABLE recalls;
DROP TABLE haccp_answers;
DROP TABLE haccp_records;
DROP TABLE haccp_items;
DROP TABLE haccp_checklists;
DROP TABLE quality_decisions;
DROP TYPE quality_decision;
DROP TABLE nonconformities;
DROP TABLE alarms;
DROP TYPE alarm_status;
DROP TYPE alarm_rule;
DROP TABLE shipment_items;
DROP TABLE shipments;
DROP TYPE shipment_status;
