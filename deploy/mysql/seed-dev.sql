-- Local development data only. This script is never executed automatically.
START TRANSACTION;

INSERT IGNORE INTO products (sku, name, price_cents, status)
VALUES
    ('BOOK-GO-001', 'Go Programming Essentials', 5999, 'ACTIVE'),
    ('MOUSE-001', 'Wireless Mouse', 12900, 'ACTIVE'),
    ('KEYBOARD-001', 'Mechanical Keyboard', 35900, 'ACTIVE'),
    ('ARCHIVED-001', 'Archived Product', 9900, 'INACTIVE');

INSERT IGNORE INTO inventories (product_id, available_qty)
SELECT id,
       CASE sku
           WHEN 'BOOK-GO-001' THEN 50
           WHEN 'MOUSE-001' THEN 25
           WHEN 'KEYBOARD-001' THEN 10
           ELSE 0
       END
FROM products
WHERE sku IN ('BOOK-GO-001', 'MOUSE-001', 'KEYBOARD-001', 'ARCHIVED-001');

COMMIT;
