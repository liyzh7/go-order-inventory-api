param([ValidatePattern('^[a-zA-Z0-9-]{1,16}$')][string]$RunPrefix='s10')
$ErrorActionPreference='Stop'
$root=Split-Path -Parent $PSScriptRoot
Set-Location $root
$output=Join-Path $root 'tests\stability\results'
New-Item -ItemType Directory -Force $output|Out-Null
function Median([double[]]$values){$sorted=@($values|Sort-Object);if(-not $sorted.Count){return 0};$half=[int][math]::Floor($sorted.Count/2);if($sorted.Count%2){return $sorted[$half]};return ($sorted[$half-1]+$sorted[$half])/2}
$runs=@();$windows=@();$samples=@()
foreach($name in @('read','distributed','skewed','hotspot')){
    $r=Get-Content -Raw ".tmp/stability/$RunPrefix-$name-result.json"|ConvertFrom-Json
    if(-not $r.complete -or -not $r.consistent){throw "Incomplete or inconsistent run: $name"}
    $metrics=$r.k6.metrics;$items=@($r.samples)
    $cpu=($items[-1].cpu_seconds-$items[0].cpu_seconds)/($items[-1].elapsed_s-$items[0].elapsed_s)
    $retry=@($r.transactions.series|Where-Object stage -eq 'transaction_retry_delay'|ForEach-Object {$_.durations.count}|Measure-Object -Sum).Sum
    $txn=@($r.transactions.series|Where-Object {$_.stage -eq 'transaction_attempt' -and $_.outcome -eq 'success'})[0]
    $commit=@($r.transactions.series|Where-Object {$_.stage -eq 'commit' -and $_.outcome -eq 'success'})[0]
    $row=[pscustomobject][ordered]@{
        scenario=$name;source_commit=$r.source_commit;start_utc=$r.start_utc;finish_utc=$r.finish_utc
        formal_seconds=$r.formal_seconds;wall_seconds=$r.wall_seconds;target_rate=$r.rate;max_vus=$r.max_vus
        requests=[long]$metrics.http_reqs.count;http_rps=$metrics.http_reqs.rate;business_success=[long]$metrics.business_success.count
        business_ops_s=$metrics.business_success.rate;business_rejected=[long]$metrics.business_rejected.count
        http_error_rate=[double]$metrics.http_req_failed.value;business_error_rate=[double]$metrics.business_error_rate.value
        p50_ms=$metrics.http_req_duration.med;p95_ms=$metrics.http_req_duration.'p(95)';p99_ms=$metrics.http_req_duration.'p(99)'
        dropped=[long]$metrics.dropped_iterations.count;average_cpu_cores=$cpu
        max_working_set_bytes=($items|Measure-Object working_set_bytes -Maximum).Maximum
        start_heap=$r.runtime_before.runtime.heap_alloc_bytes;end_heap=$r.runtime_end.runtime.heap_alloc_bytes;cooldown_heap=$r.runtime_cooldown.runtime.heap_alloc_bytes
        max_heap=($items|ForEach-Object {$_.runtime.heap_alloc_bytes}|Measure-Object -Maximum).Maximum
        start_goroutines=$r.runtime_before.runtime.goroutines;end_goroutines=$r.runtime_end.runtime.goroutines;cooldown_goroutines=$r.runtime_cooldown.runtime.goroutines
        max_goroutines=($items|ForEach-Object {$_.runtime.goroutines}|Measure-Object -Maximum).Maximum
        gc_count=$r.runtime_end.runtime.num_gc-$r.runtime_before.runtime.num_gc
        gc_pause_total_ms=($r.runtime_end.runtime.pause_total_ns-$r.runtime_before.runtime.pause_total_ns)/1e6
        pool_wait_count=$r.runtime_end.database.wait_count-$r.runtime_before.database.wait_count
        pool_wait_ms=($r.runtime_end.database.wait_duration_ns-$r.runtime_before.database.wait_duration_ns)/1e6
        end_pool_in_use=$r.runtime_end.database.in_use;cooldown_pool_in_use=$r.runtime_cooldown.database.in_use
        max_mysql_connections=($items|ForEach-Object {$_.sql.threads_connected}|Measure-Object -Maximum).Maximum
        mysql_cpu_max_percent=($items|ForEach-Object {[double]$_.mysql.CPUPerc.TrimEnd('%')}|Measure-Object -Maximum).Maximum
        row_lock_waits=$r.database_after.innodb_row_lock_waits-$r.database_before.innodb_row_lock_waits
        row_lock_wait_ms=$r.database_after.innodb_row_lock_time_ms-$r.database_before.innodb_row_lock_time_ms
        deadlocks=$r.database_after.innodb_deadlocks-$r.database_before.innodb_deadlocks
        lock_timeouts=$items[-1].sql.lock_timeouts-$items[0].sql.lock_timeouts
        retry_count=$retry;transaction_p95_ms=$txn.durations.p95_ms;commit_p95_ms=$commit.durations.p95_ms
        orders_delta=[long]$r.after.Orders-[long]$r.before.Orders;items_delta=[long]$r.after.OrderItems-[long]$r.before.OrderItems
        stock_delta=[long]$r.after.inventory_units-[long]$r.before.inventory_units
        database_bytes_start=$items[0].sql.database_bytes;database_bytes_end=$items[-1].sql.database_bytes
        consistent=$r.consistent;samples=$items.Count
    }
    $runs+=$row
    for($i=0;$i -lt [math]::Ceiling($r.formal_seconds/60);$i++){
        $duration=$metrics."http_req_duration{window:$i}";$count=[long]$metrics."http_reqs{window:$i}".count
        $windows+=[pscustomobject]@{scenario=$name;window=$i;nominal_seconds=[math]::Min(60,$r.formal_seconds-60*$i);requests=$count;rps=$count/[math]::Min(60,$r.formal_seconds-60*$i);success=[long]$metrics."business_success{window:$i}".count;p50_ms=$duration.med;p95_ms=$duration.'p(95)';p99_ms=$duration.'p(99)';http_error_rate=$metrics."http_req_failed{window:$i}".value}
    }
    foreach($s in $items){$samples+=[pscustomobject]@{scenario=$name;utc=$s.utc;elapsed_s=$s.elapsed_s;cpu_seconds=$s.cpu_seconds;working_set_bytes=$s.working_set_bytes;heap_bytes=$s.runtime.heap_alloc_bytes;goroutines=$s.runtime.goroutines;num_gc=$s.runtime.num_gc;gc_pause_total_ns=$s.runtime.pause_total_ns;pool_open=$s.pool.open_connections;pool_in_use=$s.pool.in_use;pool_wait_count=$s.pool.wait_count;mysql_cpu_percent=$s.mysql.CPUPerc;mysql_memory=$s.mysql.MemUsage;mysql_connections=$s.sql.threads_connected;row_waits=$s.sql.row_waits;row_wait_ms=$s.sql.row_wait_ms;lock_timeouts=$s.sql.lock_timeouts;database_bytes=$s.sql.database_bytes}}
}
if(@($runs|Where-Object {$_.http_error_rate -ne 0 -or $_.business_error_rate -ne 0 -or $_.dropped -ne 0}).Count){throw 'Steady-state test contains failures or dropped work'}
$doc=[ordered]@{generated_at_utc=[DateTime]::UtcNow.ToString('o');run_count=$runs.Count;resource_limits=@{mysql_cpu=2;mysql_memory_bytes=2GB;go_maxprocs=2;go_soft_memory_limit='256MiB';go_working_set_stop_bytes=512MB;min_free_disk_bytes=20GB;database_size_stop_bytes=1GB};runs=$runs;windows=$windows}
$doc|ConvertTo-Json -Depth 10|Set-Content "$output/stability-summary.json" -Encoding utf8NoBOM
$runs|Export-Csv "$output/stability-runs.csv" -NoTypeInformation -Encoding utf8NoBOM
$windows|Export-Csv "$output/stability-windows.csv" -NoTypeInformation -Encoding utf8NoBOM
$samples|Export-Csv "$output/stability-monitor.csv" -NoTypeInformation -Encoding utf8NoBOM
Write-Output "Summarized $($runs.Count) bounded runs, $($windows.Count) minute windows and $($samples.Count) resource samples"
