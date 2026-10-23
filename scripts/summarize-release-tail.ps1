param([ValidatePattern('^[a-zA-Z0-9-]{1,30}$')][string[]]$RunIDs=@('s11-hotspot-r1','s11-hotspot-r2'))
$ErrorActionPreference='Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$out='tests/release/results';New-Item -ItemType Directory -Force $out|Out-Null
$results=[Collections.Generic.List[object]]::new();$windows=[Collections.Generic.List[object]]::new();$monitors=[Collections.Generic.List[object]]::new()
foreach($id in $RunIDs){
    $r=Get-Content -Raw ".tmp/stability/$id-result.json"|ConvertFrom-Json
    if(-not $r.complete -or -not $r.consistent -or $r.formal_seconds -ne 600 -or $r.rate -ne 50 -or $r.max_vus -ne 25){throw 'Not a completed, consistent same-parameter tail repeat'}
    $m=$r.k6.metrics
    foreach($s in $r.samples){$monitors.Add([pscustomobject]@{run_id=$id;utc=([DateTime]$s.utc).ToUniversalTime().ToString('o');elapsed_s=$s.elapsed_s;cpu_seconds=$s.cpu_seconds;working_set_bytes=$s.working_set_bytes;heap_bytes=$s.runtime.heap_alloc_bytes;goroutines=$s.runtime.goroutines;num_gc=$s.runtime.num_gc;gc_pause_total_ns=$s.runtime.pause_total_ns;pool_open=$s.pool.open_connections;pool_in_use=$s.pool.in_use;pool_wait_count=$s.pool.wait_count;mysql_cpu_percent=$s.mysql.CPUPerc;mysql_memory=$s.mysql.MemUsage;mysql_connections=$s.sql.threads_connected;row_waits=$s.sql.row_waits;row_wait_ms=$s.sql.row_wait_ms;lock_timeouts=$s.sql.lock_timeouts;database_bytes=$s.sql.database_bytes})}
    for($i=0;$i -lt 10;$i++){
        $d=$m."http_req_duration{window:$i}";$samples=@($r.samples|Where-Object {$_.elapsed_s -ge ($i*60) -and $_.elapsed_s -lt (($i+1)*60)})
        $windows.Add([pscustomobject]@{run_id=$id;minute=$i+1;requests=[long]$m."http_reqs{window:$i}".count;success=[long]$m."business_success{window:$i}".count;p50_ms=$d.med;p95_ms=$d.'p(95)';p99_ms=$d.'p(99)';http_error_rate=$m."http_req_failed{window:$i}".value;max_heap_bytes=($samples|ForEach-Object {$_.runtime.heap_alloc_bytes}|Measure-Object -Maximum).Maximum;max_goroutines=($samples|ForEach-Object {$_.runtime.goroutines}|Measure-Object -Maximum).Maximum;pool_wait_count=$samples[-1].pool.wait_count;row_wait_count=$samples[-1].sql.row_waits;row_wait_ms=$samples[-1].sql.row_wait_ms;database_bytes=$samples[-1].sql.database_bytes})
    }
    $results.Add([ordered]@{run_id=$id;source_commit=$r.source_commit;start_utc=$r.start_utc;finish_utc=$r.finish_utc;wall_seconds=$r.wall_seconds;formal_seconds=600;warmup_seconds=5;target_rate=50;max_vus=25;requests=$m.http_reqs.count;success=$m.business_success.count;rps=$m.http_reqs.rate;p50_ms=$m.http_req_duration.med;p95_ms=$m.http_req_duration.'p(95)';p99_ms=$m.http_req_duration.'p(99)';http_error_rate=$m.http_req_failed.value;business_error_rate=$m.business_error_rate.value;business_rejected=$m.business_rejected.count;dropped=$m.dropped_iterations.count;orders_delta=[long]$r.after.Orders-[long]$r.before.Orders;items_delta=[long]$r.after.OrderItems-[long]$r.before.OrderItems;stock_delta=[long]$r.after.inventory_units-[long]$r.before.inventory_units;per_product_stock_mismatches=$r.per_product_stock_mismatches;consistent=$r.consistent;final=$r.after;runtime_before=$r.runtime_before;runtime_end=$r.runtime_end;runtime_cooldown=$r.runtime_cooldown;transactions=$r.transactions;row_waits=$r.database_after.innodb_row_lock_waits-$r.database_before.innodb_row_lock_waits;row_wait_ms=$r.database_after.innodb_row_lock_time_ms-$r.database_before.innodb_row_lock_time_ms;deadlocks=$r.database_after.innodb_deadlocks-$r.database_before.innodb_deadlocks})
}
$results.ToArray()|ConvertTo-Json -Depth 16|Set-Content "$out/tail-repeats.json" -Encoding utf8NoBOM
$windows|Export-Csv "$out/tail-windows.csv" -NoTypeInformation -Encoding utf8NoBOM
$monitors|Export-Csv "$out/tail-monitor.csv" -NoTypeInformation -Encoding utf8NoBOM
Write-Output 'Saved release-only evidence; stage 9/10 original reports unchanged.'
