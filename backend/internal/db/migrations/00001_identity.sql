-- +goose Up
CREATE TYPE user_role AS ENUM (
    'warehouse_operator',
    'driver',
    'quality_manager',
    'operations_manager',
    'customer',
    'auditor'
);

CREATE TABLE customers (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code       text        NOT NULL UNIQUE,
    name       text        NOT NULL CHECK (length(name) > 0),
    address    text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         text        NOT NULL CHECK (position('@' IN email) > 1),
    full_name     text        NOT NULL,
    password_hash text        NOT NULL,
    role          user_role   NOT NULL,
    customer_id   bigint REFERENCES customers (id),
    active        boolean     NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    -- a customer account always belongs to exactly one market, staff never do
    CONSTRAINT users_customer_scope CHECK ((role = 'customer') = (customer_id IS NOT NULL))
);
CREATE UNIQUE INDEX users_email_lower_uq ON users (lower(email));

-- Refresh tokens are stored hashed. Tokens issued from the same login share a
-- family; presenting an already-rotated token revokes the whole family.
CREATE TABLE refresh_tokens (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family      uuid        NOT NULL,
    token_hash  bytea       NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family);

-- +goose Down
DROP TABLE refresh_tokens;
DROP TABLE users;
DROP TABLE customers;
DROP TYPE user_role;
