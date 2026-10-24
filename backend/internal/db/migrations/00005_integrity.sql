-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only (% rejected)', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;
-- +goose StatementEnd

CREATE TABLE audit_log (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at         timestamptz NOT NULL DEFAULT now(),
    actor_id   bigint REFERENCES users (id),
    actor_role text,
    action     text        NOT NULL,
    entity     text        NOT NULL,
    entity_id  text        NOT NULL,
    old_value  jsonb,
    new_value  jsonb,
    request_id text        NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_entity_idx ON audit_log (entity, entity_id, id);

CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

-- Excursion reports form a hash chain: hash_n = SHA-256(hash_{n-1} || canonical(payload_n)).
-- Triggers stop accidental edits; the chain detects deliberate ones (e.g. by a DBA
-- who disables the trigger).
CREATE TABLE excursion_reports (
    seq        bigint      PRIMARY KEY CHECK (seq > 0),
    lot_id     bigint      NOT NULL REFERENCES lots (id),
    created_by bigint      NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL,
    payload    jsonb       NOT NULL,
    prev_hash  bytea       NOT NULL CHECK (length(prev_hash) = 32),
    hash       bytea       NOT NULL UNIQUE CHECK (length(hash) = 32)
);
CREATE INDEX excursion_reports_lot_idx ON excursion_reports (lot_id);

CREATE TRIGGER excursion_reports_append_only BEFORE UPDATE OR DELETE ON excursion_reports
    FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER excursion_reports_no_truncate BEFORE TRUNCATE ON excursion_reports
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

-- Single-row chain head; appenders lock it with SELECT ... FOR UPDATE so that
-- concurrent reports get consecutive sequence numbers.
CREATE TABLE report_chain_head (
    id   smallint PRIMARY KEY CHECK (id = 1),
    seq  bigint   NOT NULL,
    hash bytea    NOT NULL
);
INSERT INTO report_chain_head (id, seq, hash) VALUES (1, 0, decode(repeat('00', 32), 'hex'));

CREATE TABLE idempotency_keys (
    user_id       bigint      NOT NULL REFERENCES users (id),
    key           text        NOT NULL CHECK (length(key) BETWEEN 8 AND 128),
    method        text        NOT NULL,
    path          text        NOT NULL,
    request_hash  bytea       NOT NULL,
    status_code   int,
    response_body bytea,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE report_chain_head;
DROP TABLE excursion_reports;
DROP TABLE audit_log;
DROP FUNCTION forbid_mutation();
