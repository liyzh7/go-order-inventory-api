USE order_api_perf_baseline;

SELECT JSON_OBJECT(
  'captured_at_utc', DATE_FORMAT(UTC_TIMESTAMP(6), '%Y-%m-%dT%H:%i:%s.%fZ'),
  'innodb_row_lock_waits', COALESCE((SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Innodb_row_lock_waits'), 0),
  'innodb_row_lock_time_ms', COALESCE((SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Innodb_row_lock_time'), 0),
  'innodb_deadlocks', COALESCE((SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Innodb_deadlocks'), 0),
  'current_data_lock_waits', (SELECT COUNT(*) FROM performance_schema.data_lock_waits),
  'transaction_count', COALESCE((SELECT SUM(COUNT_STAR) FROM performance_schema.events_transactions_summary_global_by_event_name), 0),
  'transaction_time_ps', COALESCE((SELECT SUM(SUM_TIMER_WAIT) FROM performance_schema.events_transactions_summary_global_by_event_name), 0),
  'commit_count', COALESCE((SELECT SUM(COUNT_STAR) FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST_TEXT='COMMIT'), 0),
  'commit_time_ps', COALESCE((SELECT SUM(SUM_TIMER_WAIT) FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST_TEXT='COMMIT'), 0),
  'rollback_count', COALESCE((SELECT SUM(COUNT_STAR) FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST_TEXT='ROLLBACK'), 0),
  'rollback_time_ps', COALESCE((SELECT SUM(SUM_TIMER_WAIT) FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST_TEXT='ROLLBACK'), 0),
  'inventory_reads', COALESCE((SELECT COUNT_READ FROM performance_schema.table_io_waits_summary_by_table WHERE OBJECT_SCHEMA=DATABASE() AND OBJECT_NAME='inventories'), 0),
  'inventory_writes', COALESCE((SELECT COUNT_WRITE FROM performance_schema.table_io_waits_summary_by_table WHERE OBJECT_SCHEMA=DATABASE() AND OBJECT_NAME='inventories'), 0),
  'inventory_read_time_ps', COALESCE((SELECT SUM_TIMER_READ FROM performance_schema.table_io_waits_summary_by_table WHERE OBJECT_SCHEMA=DATABASE() AND OBJECT_NAME='inventories'), 0),
  'inventory_write_time_ps', COALESCE((SELECT SUM_TIMER_WRITE FROM performance_schema.table_io_waits_summary_by_table WHERE OBJECT_SCHEMA=DATABASE() AND OBJECT_NAME='inventories'), 0),
  'orders_reads', COALESCE((SELECT COUNT_READ FROM performance_schema.table_io_waits_summary_by_table WHERE OBJECT_SCHEMA=DATABASE() AND OBJECT_NAME='orders'), 0),
  'orders_writes', COALESCE((SELECT COUNT_WRITE FROM performance_schema.table_io_waits_summary_by_table WHERE OBJECT_SCHEMA=DATABASE() AND OBJECT_NAME='orders'), 0),
  'order_items_writes', COALESCE((SELECT COUNT_WRITE FROM performance_schema.table_io_waits_summary_by_table WHERE OBJECT_SCHEMA=DATABASE() AND OBJECT_NAME='order_items'), 0)
);
