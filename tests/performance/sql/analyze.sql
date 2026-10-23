USE order_api_perf_baseline;

SELECT 'products_list' AS analysis;
EXPLAIN ANALYZE
SELECT p.id,p.sku,p.name,p.price_cents,i.available_qty
FROM products p JOIN inventories i ON i.product_id=p.id
WHERE p.status='ACTIVE' AND p.id>500 ORDER BY p.id ASC LIMIT 100;

SELECT 'product_detail' AS analysis;
EXPLAIN ANALYZE
SELECT p.id,p.sku,p.name,p.price_cents,i.available_qty
FROM products p JOIN inventories i ON i.product_id=p.id
WHERE p.id=2 AND p.status='ACTIVE';

SELECT 'owned_order_detail' AS analysis;
EXPLAIN ANALYZE
SELECT o.id,o.order_no,o.status,o.total_amount_cents,o.created_at,
       oi.product_id,oi.sku_snapshot,oi.name_snapshot,oi.unit_price_cents,oi.quantity
FROM orders o LEFT JOIN order_items oi ON oi.order_id=o.id
WHERE o.id=1 AND o.user_id=1 ORDER BY oi.product_id ASC;

SELECT 'idempotency_lookup' AS analysis;
EXPLAIN ANALYZE
SELECT id,order_no,request_hash,status,total_amount_cents,created_at
FROM orders WHERE user_id=1 AND idempotency_key=CAST('perf-seed-cancel-000001' AS BINARY);

SELECT 'inventory_update_plan_only' AS analysis;
EXPLAIN FORMAT=JSON
UPDATE inventories SET available_qty=available_qty-1
WHERE product_id=1 AND available_qty>=1;

SELECT 'cancel_state_update_plan_only' AS analysis;
EXPLAIN FORMAT=JSON
UPDATE orders SET status='CANCELLED'
WHERE id=1 AND user_id=1 AND status='CREATED';

SELECT 'statement_digests' AS analysis;
SELECT LEFT(DIGEST_TEXT,180) AS digest_text,COUNT_STAR,
       ROUND(SUM_TIMER_WAIT/1000000000,3) AS total_ms,
       ROUND(AVG_TIMER_WAIT/1000000000,3) AS avg_ms,
       ROUND(SUM_LOCK_TIME/1000000000,3) AS lock_ms,
       SUM_ROWS_EXAMINED,SUM_ROWS_SENT,SUM_ROWS_AFFECTED
FROM performance_schema.events_statements_summary_by_digest
WHERE SCHEMA_NAME='order_api_perf_baseline'
ORDER BY SUM_TIMER_WAIT DESC LIMIT 20;

SELECT 'table_io' AS analysis;
SELECT OBJECT_NAME,COUNT_READ,COUNT_WRITE,
       ROUND(SUM_TIMER_READ/1000000000,3) AS read_ms,
       ROUND(SUM_TIMER_WRITE/1000000000,3) AS write_ms
FROM performance_schema.table_io_waits_summary_by_table
WHERE OBJECT_SCHEMA='order_api_perf_baseline'
ORDER BY SUM_TIMER_WAIT DESC;

SELECT 'current_lock_waits' AS analysis;
SELECT COUNT(*) AS data_lock_waits FROM performance_schema.data_lock_waits;

SELECT 'connection_state' AS analysis;
SELECT COUNT(*) AS app_connections,
       SUM(PROCESSLIST_COMMAND <> 'Sleep') AS active_connections
FROM performance_schema.threads
WHERE PROCESSLIST_DB='order_api_perf_baseline';

SELECT 'global_status' AS analysis;
SHOW GLOBAL STATUS WHERE Variable_name IN
('Threads_connected','Threads_running','Connections','Aborted_connects','Created_tmp_disk_tables','Created_tmp_tables','Innodb_deadlocks','Innodb_row_lock_current_waits','Innodb_row_lock_time','Innodb_row_lock_waits');
