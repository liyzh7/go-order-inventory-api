$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'source-snapshot.ps1')
$sourceSnapshotSHA256 = Get-SourceSnapshotSHA256 -Root $projectRoot
$rawDirectory = Join-Path $projectRoot '.tmp\performance\raw'
$outputDirectory = Join-Path $projectRoot 'tests\performance\results'
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null

function MetricValue($metrics, [string]$name, [string]$property, $fallback = 0) {
    $metric = $metrics.PSObject.Properties[$name]
    if (-not $metric) { return $fallback }
    $value = $metric.Value.PSObject.Properties[$property]
    if (-not $value -or $null -eq $value.Value) { return $fallback }
    return $value.Value
}
function NumberFromPercent([string]$value) {
    if (-not $value) { return 0 }
    return [double]($value.TrimEnd('%'))
}
function Median([double[]]$values) {
    if (-not $values -or $values.Count -eq 0) { return 0 }
    $ordered = @($values | Sort-Object)
    $middle = [math]::Floor($ordered.Count / 2)
    if ($ordered.Count % 2) { return $ordered[$middle] }
    return ($ordered[$middle - 1] + $ordered[$middle]) / 2
}

$trials = foreach ($file in Get-ChildItem -LiteralPath $rawDirectory -Filter '*-result.json' | Where-Object { $_.BaseName -match '^(baseline|progress)-' } | Sort-Object Name) {
    $result = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json
    $metrics = $result.k6.metrics
    $requests = [int64](MetricValue $metrics 'http_reqs' 'count')
    $samples = @($result.resource_samples)
    $mysqlCPU = @($samples | ForEach-Object { NumberFromPercent $_.mysql_cpu_percent })
    $first = $samples | Select-Object -First 1
    $last = $samples | Select-Object -Last 1
    $poolWaitDelta = if ($first -and $last) { [int64]$last.database.wait_count - [int64]$first.database.wait_count } else { 0 }
    $gcDelta = if ($first -and $last) { [int64]$last.runtime.num_gc - [int64]$first.runtime.num_gc } else { 0 }
    $gcPauseDelta = if ($first -and $last) { [int64]$last.runtime.pause_total_ns - [int64]$first.runtime.pause_total_ns } else { 0 }
    $requestRate = [double](MetricValue $metrics 'http_reqs' 'rate')
    $activeSeconds = if ($requestRate -gt 0) { $requests / $requestRate } else { 0 }
    $distinctOperations = switch ($result.scenario) {
        'order-create' { [int64]$result.after.Orders-[int64]$result.before.Orders }
        'inventory-competition' { [int64]$result.after.Orders-[int64]$result.before.Orders }
        'idempotency-concurrent' { [int64]$result.after.Orders-[int64]$result.before.Orders }
        'idempotency-cross-user' { [int64]$result.after.Orders-[int64]$result.before.Orders }
        'order-cancel' { [int64]$result.after.CancelledOrders-[int64]$result.before.CancelledOrders }
        default { [int64](MetricValue $metrics 'business_success' 'count') }
    }
    [pscustomobject][ordered]@{
        run_id=$result.run_id; scenario=$result.scenario; model=$result.model; vus=$result.vus; configured_rate=$result.rate
        configured_duration=$result.configured_duration; requests=$requests; request_rate_per_second=$requestRate
        business_success=[int64](MetricValue $metrics 'business_success' 'count'); business_success_per_second=[double](MetricValue $metrics 'business_success' 'rate')
        distinct_business_operations=$distinctOperations; distinct_business_operations_per_second=$(if($activeSeconds -gt 0){$distinctOperations/$activeSeconds}else{0})
        business_rejected=[int64](MetricValue $metrics 'business_rejected' 'count'); idempotency_replayed=[int64](MetricValue $metrics 'idempotency_replayed' 'count')
        cancellation_replayed=[int64](MetricValue $metrics 'cancellation_replayed' 'count'); unexpected_responses=[int64](MetricValue $metrics 'unexpected_responses' 'count')
        auth_failures=[int64](MetricValue $metrics 'auth_failures' 'count'); database_failures=[int64](MetricValue $metrics 'database_failures' 'count')
        http_error_rate=[double](MetricValue $metrics 'http_req_failed' 'value'); business_error_rate=[double](MetricValue $metrics 'business_error_rate' 'value')
        avg_ms=[double](MetricValue $metrics 'http_req_duration' 'avg'); p50_ms=[double](MetricValue $metrics 'http_req_duration' 'med')
        p95_ms=[double](MetricValue $metrics 'http_req_duration' 'p(95)'); p99_ms=[double](MetricValue $metrics 'http_req_duration' 'p(99)')
        p99_confidence=$(if($requests -lt 1000){'limited_sample'}else{'baseline_sample'})
        normalized_server_cpu_percent=[double]$result.normalized_host_cpu_percent
        max_server_working_set_mib=$(if($samples.Count){[math]::Round((($samples | Measure-Object server_working_set_bytes -Maximum).Maximum/1MB),3)}else{0})
        max_heap_alloc_mib=$(if($samples.Count){[math]::Round((($samples.runtime | Measure-Object heap_alloc_bytes -Maximum).Maximum/1MB),3)}else{0})
        max_goroutines=$(if($samples.Count){($samples.runtime | Measure-Object goroutines -Maximum).Maximum}else{0})
        max_db_open=$(if($samples.Count){($samples.database | Measure-Object open_connections -Maximum).Maximum}else{0})
        max_db_in_use=$(if($samples.Count){($samples.database | Measure-Object in_use -Maximum).Maximum}else{0})
        pool_wait_count_delta=$poolWaitDelta; gc_count_delta=$gcDelta; gc_pause_delta_ms=[math]::Round($gcPauseDelta/1e6,4)
        avg_mysql_cpu_percent=$(if($mysqlCPU.Count){($mysqlCPU | Measure-Object -Average).Average}else{0})
        max_mysql_cpu_percent=$(if($mysqlCPU.Count){($mysqlCPU | Measure-Object -Maximum).Maximum}else{0})
        orders_delta=([int64]$result.after.Orders-[int64]$result.before.Orders)
        order_items_delta=([int64]$result.after.OrderItems-[int64]$result.before.OrderItems)
        inventory_units_delta=([int64]$result.after.inventory_units-[int64]$result.before.inventory_units)
        cancelled_orders_delta=([int64]$result.after.CancelledOrders-[int64]$result.before.CancelledOrders)
        consistency_valid=[bool]$result.after.valid
    }
}

$aggregates = foreach ($group in $trials | Where-Object { $_.run_id -like 'baseline-*' } | Group-Object scenario) {
    $items = @($group.Group)
    [pscustomobject][ordered]@{
        scenario=$group.Name; rounds=$items.Count; requests_total=($items | Measure-Object requests -Sum).Sum
        median_request_rate_per_second=Median @($items.request_rate_per_second)
        median_business_success_per_second=Median @($items.business_success_per_second)
        median_distinct_business_operations_per_second=Median @($items.distinct_business_operations_per_second)
        median_avg_ms=Median @($items.avg_ms); median_p50_ms=Median @($items.p50_ms)
        median_p95_ms=Median @($items.p95_ms); median_p99_ms=Median @($items.p99_ms)
        business_rejected_total=($items | Measure-Object business_rejected -Sum).Sum
        idempotency_replayed_total=($items | Measure-Object idempotency_replayed -Sum).Sum
        cancellation_replayed_total=($items | Measure-Object cancellation_replayed -Sum).Sum
        unexpected_responses_total=($items | Measure-Object unexpected_responses -Sum).Sum
        http_error_rate_max=($items | Measure-Object http_error_rate -Maximum).Maximum
        business_error_rate_max=($items | Measure-Object business_error_rate -Maximum).Maximum
        max_normalized_server_cpu_percent=($items | Measure-Object normalized_server_cpu_percent -Maximum).Maximum
        max_server_working_set_mib=($items | Measure-Object max_server_working_set_mib -Maximum).Maximum
        max_heap_alloc_mib=($items | Measure-Object max_heap_alloc_mib -Maximum).Maximum
        max_goroutines=($items | Measure-Object max_goroutines -Maximum).Maximum
        max_db_open=($items | Measure-Object max_db_open -Maximum).Maximum
        max_db_in_use=($items | Measure-Object max_db_in_use -Maximum).Maximum
        pool_wait_count_delta_total=($items | Measure-Object pool_wait_count_delta -Sum).Sum
        max_mysql_cpu_percent=($items | Measure-Object max_mysql_cpu_percent -Maximum).Maximum
        consistency_all_valid=(@($items | Where-Object { -not $_.consistency_valid }).Count -eq 0)
    }
}

$document = [ordered]@{
    generated_at_utc=[DateTime]::UtcNow.ToString('o')
    source_snapshot_sha256=$sourceSnapshotSHA256
    environment=[ordered]@{ cpu='AMD Ryzen 7 8845H'; physical_cores=8; logical_processors=16; memory_gib=27.81; go='1.27.2 windows/amd64'; k6='2.3.0 windows/amd64'; mysql='8.4.12'; products=1000; users=25; cancellation_orders=500; seed=20261016; db_max_open=25; db_max_idle=10 }
    aggregates=$aggregates
    trials=$trials
}
$document | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath (Join-Path $outputDirectory 'baseline-summary.json') -Encoding utf8NoBOM
$trials | Export-Csv -LiteralPath (Join-Path $outputDirectory 'baseline-trials.csv') -NoTypeInformation -Encoding utf8NoBOM
Write-Output "Wrote $($trials.Count) trials and $($aggregates.Count) scenario aggregates"
