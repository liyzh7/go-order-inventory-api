-- +goose Up
CREATE TABLE orders (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    order_no VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    idempotency_key VARBINARY(64) NOT NULL,
    request_hash BINARY(32) NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    total_amount_cents BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    CONSTRAINT uk_orders_order_no UNIQUE (order_no),
    CONSTRAINT uk_orders_idempotency_key UNIQUE (idempotency_key),
    CONSTRAINT chk_orders_order_no_not_blank CHECK (
        CHAR_LENGTH(TRIM(order_no)) >= 1
    ),
    CONSTRAINT chk_orders_idempotency_key_length CHECK (
        OCTET_LENGTH(idempotency_key) BETWEEN 1 AND 64
    ),
    CONSTRAINT chk_orders_status CHECK (
        status IN ('CREATED', 'CANCELLED')
    ),
    CONSTRAINT chk_orders_total_amount_cents CHECK (
        total_amount_cents BETWEEN 1 AND 9000000000000000
    ),
    INDEX idx_orders_status_created_id (status, created_at, id)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE orders;
