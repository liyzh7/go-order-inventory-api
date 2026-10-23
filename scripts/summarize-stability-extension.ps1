param([ValidatePattern('^[a-zA-Z0-9-]{1,30}$')][string]$RunID='s10-hotspot-600')
$ErrorActionPreference='Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$r=Get-Content -Raw ".tmp/stability/$RunID-result.json"|ConvertFrom-Json
$m=$r.k6.metrics
$success=[long]$m.business_success.count
if($r.scenario -ne 'order-create-hotspot' -or $r.formal_seconds -ne 600 -or -not $r.complete -or -not $r.consistent -or $r.per_product_stock_mismatches -ne 0 -or
    [long]$m.http_reqs.count -ne $success -or [double]$m.http_req_failed.value -ne 0 -or [double]$m.business_error_rate.value -ne 0 -or [long]$m.dropped_iterations.count -ne 0){throw 'Ten-minute extension failed validation'}
$out='tests/stability/results'
New-Item -ItemType Directory -Force $out|Out-Null
$windows=for($i=0;$i -lt 10;$i++){
    $d=$m."http_req_duration{window:$i}"
    [pscustomobject]@{window=$i;requests=[long]$m."http_reqs{window:$i}".count;success=[long]$m."business_success{window:$i}".count;rps=[double]$m."http_reqs{window:$i}".count/60;p50_ms=$d.med;p95_ms=$d.'p(95)';p99_ms=$d.'p(99)';http_error_rate=$m."http_req_failed{window:$i}".value}
}
$samples=foreach($s in $r.samples){[pscustomobject]@{utc=$s.utc;elapsed_s=$s.elapsed_s;cpu_seconds=$s.cpu_seconds;working_set_bytes=$s.working_set_bytes;heap_bytes=$s.runtime.heap_alloc_bytes;goroutines=$s.runtime.goroutines;num_gc=$s.runtime.num_gc;gc_pause_total_ns=$s.runtime.pause_total_ns;pool_open=$s.pool.open_connections;pool_in_use=$s.pool.in_use;pool_wait_count=$s.pool.wait_count;mysql_cpu_percent=$s.mysql.CPUPerc;mysql_memory=$s.mysql.MemUsage;mysql_connections=$s.sql.threads_connected;row_waits=$s.sql.row_waits;row_wait_ms=$s.sql.row_wait_ms;lock_timeouts=$s.sql.lock_timeouts;database_bytes=$s.sql.database_bytes}}
$txn=@($r.transactions.series|Where-Object {$_.stage -eq 'transaction_attempt' -and $_.outcome -eq 'success'})[0]
$retry=($r.transactions.series|Where-Object stage -eq 'transaction_retry_delay'|ForEach-Object {$_.durations.count}|Measure-Object -Sum).Sum
$summary=[ordered]@{
    source_commit=$r.source_commit;run_id=$r.run_id;formal_seconds=$r.formal_seconds;wall_seconds=$r.wall_seconds
    start_utc=$r.start_utc;finish_utc=$r.finish_utc;target_rate=$r.rate;max_vus=$r.max_vus
    requests=[long]$m.http_reqs.count;business_success=$success;http_rps=$m.http_reqs.rate;business_ops_s=$m.business_success.rate
    p50_ms=$m.http_req_duration.med;p95_ms=$m.http_req_duration.'p(95)';p99_ms=$m.http_req_duration.'p(99)'
    http_error_rate=$m.http_req_failed.value;business_error_rate=$m.business_error_rate.value;business_rejected=[long]$m.business_rejected.count;dropped=[long]$m.dropped_iterations.count
    orders_delta=[long]$r.after.Orders-[long]$r.before.Orders;items_delta=[long]$r.after.OrderItems-[long]$r.before.OrderItems;stock_delta=[long]$r.after.inventory_units-[long]$r.before.inventory_units
    final_state=$r.after;consistent=$r.consistent;per_product_stock_mismatches=$r.per_product_stock_mismatches
    runtime_before=$r.runtime_before;runtime_end=$r.runtime_end;runtime_cooldown=$r.runtime_cooldown
    cpu_cores=($samples[-1].cpu_seconds-$samples[0].cpu_seconds)/($samples[-1].elapsed_s-$samples[0].elapsed_s)
    max_working_set_bytes=($samples|Measure-Object working_set_bytes -Maximum).Maximum
    max_heap_bytes=($samples|Measure-Object heap_bytes -Maximum).Maximum;max_goroutines=($samples|Measure-Object goroutines -Maximum).Maximum
    row_lock_waits=$r.database_after.innodb_row_lock_waits-$r.database_before.innodb_row_lock_waits
    row_lock_wait_ms=$r.database_after.innodb_row_lock_time_ms-$r.database_before.innodb_row_lock_time_ms
    deadlocks=$r.database_after.innodb_deadlocks-$r.database_before.innodb_deadlocks
    lock_timeouts=$samples[-1].lock_timeouts-$samples[0].lock_timeouts;retries=$retry
    transaction_p95_ms=$txn.durations.p95_ms;transactions=$r.transactions;windows=$windows
}
$summary|ConvertTo-Json -Depth 15|Set-Content "$out/hotspot-10m-summary.json" -Encoding utf8NoBOM
$windows|Export-Csv "$out/hotspot-10m-windows.csv" -NoTypeInformation -Encoding utf8NoBOM
$samples|Export-Csv "$out/hotspot-10m-monitor.csv" -NoTypeInformation -Encoding utf8NoBOM
Write-Output 'Saved ten-minute extension summary, minute windows and aggregate resource samples'
