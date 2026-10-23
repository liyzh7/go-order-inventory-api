-- +goose Up
CREATE TABLE inventories (
    product_id BIGINT UNSIGNED NOT NULL,
    available_qty INT UNSIGNED NOT NULL DEFAULT 0,
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (product_id),
    CONSTRAINT chk_inventories_available_qty_nonnegative CHECK (
        available_qty >= 0
    ),
    CONSTRAINT fk_inventories_product
        FOREIGN KEY (product_id) REFERENCES products (id)
        ON UPDATE RESTRICT
        ON DELETE RESTRICT
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE inventories;
