-- +goose Up
CREATE TABLE products (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    sku VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    name VARCHAR(255) NOT NULL,
    price_cents BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    CONSTRAINT uk_products_sku UNIQUE (sku),
    CONSTRAINT chk_products_sku_uppercase CHECK (
        REGEXP_LIKE(sku, '^[A-Z0-9][A-Z0-9._-]{0,63}$', 'c')
    ),
    CONSTRAINT chk_products_name_not_blank CHECK (
        CHAR_LENGTH(TRIM(name)) >= 1
    ),
    CONSTRAINT chk_products_price_cents CHECK (
        price_cents BETWEEN 1 AND 1000000000000
    ),
    CONSTRAINT chk_products_status CHECK (
        status IN ('ACTIVE', 'INACTIVE')
    ),
    INDEX idx_products_status_id (status, id)
) ENGINE = InnoDB
  DEFAULT CHARACTER SET = utf8mb4
  COLLATE = utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE products;
