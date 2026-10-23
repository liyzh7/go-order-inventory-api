-- Bypass MySQL's default metadata-stat cache; sizes are still allocation
-- estimates, not bytes on the host Volume or a physical disk quota.
SET SESSION information_schema_stats_expiry=0;
SELECT JSON_OBJECT(
  'threads_connected', (SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Threads_connected'),
  'threads_running', (SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Threads_running'),
  'row_waits', (SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Innodb_row_lock_waits'),
  'row_wait_ms', (SELECT CAST(VARIABLE_VALUE AS UNSIGNED) FROM performance_schema.global_status WHERE VARIABLE_NAME='Innodb_row_lock_time'),
  'deadlocks', (SELECT COUNT FROM information_schema.innodb_metrics WHERE NAME='lock_deadlocks'),
  'lock_timeouts', (SELECT COUNT FROM information_schema.innodb_metrics WHERE NAME='lock_timeouts'),
  'database_bytes', (SELECT COALESCE(SUM(DATA_LENGTH+INDEX_LENGTH),0) FROM information_schema.tables WHERE TABLE_SCHEMA='order_api_perf_baseline')
);
