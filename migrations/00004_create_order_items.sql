-- +goose Up
CREATE TABLE order_items (
    order_id BIGINT UNSIGNED NOT NULL,
    product_id BIGINT UNSIGNED NOT NULL,
    sku_snapshot VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    name_snapshot VARCHAR(255) NOT NULL,
    unit_price_cents BIGINT UNSIGNED NOT NULL,
    quantity INT UNSIGNED NOT NULL,
    PRIMARY KEY (order_id, product_id),
    CONSTRAINT chk_order_items_sku_snapshot_uppercase CHECK (
        REGEXP_LIKE(sku_snapshot, '^[A-Z0-9][A-Z0-9._-]{0,63}$', 'c')
    ),
    CONSTRAINT chk_order_items_name_snapshot_not_blank CHECK (
        CHAR_LENGTH(TRIM(name_snapshot)) >= 1
    ),
    CONSTRAINT chk_order_items_unit_price_cents CHECK (
        unit_price_cents BETWEEN 1 AND 1000000000000
    ),
    CONSTRAINT chk_order_items_quantity CHECK (
        quantity BETWEEN 1 AND 1000
    ),
    CONSTRAINT fk_order_items_order
        FOREIGN KEY (order_id) REFERENCES orders (id)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT,
    CONSTRAINT fk_order_items_product
        FOREIGN KEY (product_id) REFERENCES products (id)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT,
    INDEX idx_order_items_product_id (product_id)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE order_items;
