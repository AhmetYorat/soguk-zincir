-- +goose Up
-- Temperatures are stored as integer centi-degrees (cc): 4.25 °C -> 425.
-- Humidity is stored as per-mille (pm): 65.3 %RH -> 653.
-- Money is stored as integer kuruş.

CREATE TABLE warehouses (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code text NOT NULL UNIQUE,
    name text NOT NULL,
    city text NOT NULL
);

CREATE TABLE vehicles (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plate            text NOT NULL UNIQUE,
    capacity_kg      int  NOT NULL CHECK (capacity_kg > 0),
    capacity_pallets int  NOT NULL CHECK (capacity_pallets > 0),
    driver_id        bigint REFERENCES users (id)
);
CREATE INDEX vehicles_driver_idx ON vehicles (driver_id);

CREATE TYPE location_kind AS ENUM ('cold_room', 'vehicle', 'customer');

CREATE TABLE locations (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code         text          NOT NULL UNIQUE,
    name         text          NOT NULL,
    kind         location_kind NOT NULL,
    warehouse_id bigint REFERENCES warehouses (id),
    vehicle_id   bigint UNIQUE REFERENCES vehicles (id),
    customer_id  bigint UNIQUE REFERENCES customers (id),
    CONSTRAINT locations_owner CHECK (
        (kind = 'cold_room' AND warehouse_id IS NOT NULL AND vehicle_id IS NULL AND customer_id IS NULL) OR
        (kind = 'vehicle'   AND vehicle_id   IS NOT NULL AND warehouse_id IS NULL AND customer_id IS NULL) OR
        (kind = 'customer'  AND customer_id  IS NOT NULL AND warehouse_id IS NULL AND vehicle_id IS NULL)
    )
);

CREATE TABLE products (
    id                          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sku                         text NOT NULL UNIQUE,
    name                        text NOT NULL,
    temp_min_cc                 int  NOT NULL,
    temp_max_cc                 int  NOT NULL,
    -- total time a lot may spend outside [min,max] over its whole life
    max_cumulative_excursion_s  int  NOT NULL CHECK (max_cumulative_excursion_s >= 0),
    -- sliding window rule: at most window_max_excursion_s outside limits in any window_s
    window_s                    int  NOT NULL CHECK (window_s > 0),
    window_max_excursion_s      int  NOT NULL CHECK (window_max_excursion_s >= 0),
    shelf_life_h                int  NOT NULL CHECK (shelf_life_h > 0),
    -- activation energy used for MKT and the Arrhenius shelf-life model
    activation_energy_j_mol     int  NOT NULL DEFAULT 83144 CHECK (activation_energy_j_mol > 0),
    gap_policy                  text NOT NULL DEFAULT 'uncertain' CHECK (gap_policy IN ('uncertain', 'interpolate')),
    max_gap_s                   int  NOT NULL DEFAULT 120 CHECK (max_gap_s > 0),
    unit_weight_g               int  NOT NULL CHECK (unit_weight_g > 0),
    list_price_kurus            bigint NOT NULL CHECK (list_price_kurus >= 0),
    created_at                  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT products_temp_range CHECK (temp_min_cc < temp_max_cc),
    CONSTRAINT products_window CHECK (window_max_excursion_s <= window_s)
);

CREATE TYPE lot_status AS ENUM ('released', 'on_hold', 'accepted', 'discounted', 'destroyed', 'recalled');

CREATE TABLE lots (
    id                     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code                   text        NOT NULL UNIQUE,
    product_id             bigint      NOT NULL REFERENCES products (id),
    produced_at            timestamptz NOT NULL,
    quantity               int         NOT NULL CHECK (quantity > 0),
    status                 lot_status  NOT NULL DEFAULT 'released',
    discount_pct           smallint    NOT NULL DEFAULT 0 CHECK (discount_pct BETWEEN 0 AND 99),
    -- cached result of the shelf-life model, refreshed by the evaluator job
    remaining_shelf_life_s bigint,
    shelf_life_computed_at timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX lots_product_idx ON lots (product_id);
CREATE INDEX lots_status_idx ON lots (status);

CREATE TYPE unit_status AS ENUM ('active', 'loaded', 'delivered', 'consumed', 'destroyed');

-- A unit is a pallet / handling unit. It can carry several lots after merges.
CREATE TABLE units (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code        text        NOT NULL UNIQUE,
    location_id bigint      NOT NULL REFERENCES locations (id),
    status      unit_status NOT NULL DEFAULT 'active',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX units_location_idx ON units (location_id) WHERE status = 'active';

CREATE TABLE unit_contents (
    unit_id  bigint NOT NULL REFERENCES units (id),
    lot_id   bigint NOT NULL REFERENCES lots (id),
    quantity int    NOT NULL CHECK (quantity >= 0),
    PRIMARY KEY (unit_id, lot_id)
);
CREATE INDEX unit_contents_lot_idx ON unit_contents (lot_id);

CREATE TYPE lineage_op AS ENUM ('receive', 'split', 'merge');

-- Directed edges parent -> child; a 'receive' edge has no parent and marks the
-- unit a lot was first booked onto. Recursive traversal over this table is the
-- source of truth for traceability.
CREATE TABLE unit_lineage (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_unit_id bigint REFERENCES units (id),
    child_unit_id  bigint      NOT NULL REFERENCES units (id),
    op             lineage_op  NOT NULL,
    lot_id         bigint      NOT NULL REFERENCES lots (id),
    quantity       int         NOT NULL CHECK (quantity > 0),
    at             timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT unit_lineage_parent CHECK ((op = 'receive') = (parent_unit_id IS NULL)),
    CONSTRAINT unit_lineage_no_self CHECK (parent_unit_id <> child_unit_id)
);
CREATE INDEX unit_lineage_parent_idx ON unit_lineage (parent_unit_id);
CREATE INDEX unit_lineage_child_idx ON unit_lineage (child_unit_id);
CREATE INDEX unit_lineage_receive_lot_idx ON unit_lineage (lot_id) WHERE op = 'receive';

-- Where a unit was, and when. Exactly one open interval per unit.
CREATE TABLE custody_events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    unit_id     bigint      NOT NULL REFERENCES units (id),
    location_id bigint      NOT NULL REFERENCES locations (id),
    from_ts     timestamptz NOT NULL,
    to_ts       timestamptz,
    CONSTRAINT custody_interval CHECK (to_ts IS NULL OR to_ts >= from_ts)
);
CREATE UNIQUE INDEX custody_one_open_uq ON custody_events (unit_id) WHERE to_ts IS NULL;
CREATE INDEX custody_unit_idx ON custody_events (unit_id, from_ts);
CREATE INDEX custody_location_idx ON custody_events (location_id, from_ts);

-- +goose Down
DROP TABLE custody_events;
DROP TABLE unit_lineage;
DROP TYPE lineage_op;
DROP TABLE unit_contents;
DROP TABLE units;
DROP TYPE unit_status;
DROP TABLE lots;
DROP TYPE lot_status;
DROP TABLE products;
DROP TABLE locations;
DROP TYPE location_kind;
DROP TABLE vehicles;
DROP TABLE warehouses;
