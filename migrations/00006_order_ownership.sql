-- +goose Up
-- NULL is reserved for pre-authentication historical orders. No backfill to a
-- normal user is performed. Preserve old-key uniqueness for that legacy set.
ALTER TABLE orders
    ADD COLUMN user_id BIGINT UNSIGNED NULL,
    ADD COLUMN legacy_idempotency_key VARBINARY(64)
        GENERATED ALWAYS AS (IF(user_id IS NULL, idempotency_key, NULL)) STORED,
    ADD CONSTRAINT fk_orders_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    ADD CONSTRAINT uk_orders_user_idempotency UNIQUE (user_id, idempotency_key),
    ADD CONSTRAINT uk_orders_legacy_idempotency UNIQUE (legacy_idempotency_key),
    DROP INDEX uk_orders_idempotency_key;

-- +goose Down
-- Atomic ALTER refuses rollback if different users legitimately share a key.
-- It must fail rather than delete orders to recreate the old global index.
ALTER TABLE orders
    ADD CONSTRAINT uk_orders_idempotency_key UNIQUE (idempotency_key),
    DROP INDEX uk_orders_user_idempotency,
    DROP INDEX uk_orders_legacy_idempotency,
    DROP FOREIGN KEY fk_orders_user,
    DROP COLUMN legacy_idempotency_key,
    DROP COLUMN user_id;
