$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot
$rawDirectory = Join-Path $projectRoot '.tmp\performance\raw'
$outputDirectory = Join-Path $projectRoot 'tests\performance\results'
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null

function Median([double[]]$values) {
    $sorted = @($values | Sort-Object)
    if ($sorted.Count -eq 0) { return 0.0 }
    if ($sorted.Count % 2 -eq 1) { return [double]$sorted[[math]::Floor($sorted.Count / 2)] }
    return ([double]$sorted[$sorted.Count / 2 - 1] + [double]$sorted[$sorted.Count / 2]) / 2
}
function Values($items, [string]$property) { return [double[]]@($items | ForEach-Object { [double]($_.$property) }) }
function Stage($result, [string]$name, [string]$outcome = 'success') {
    return @($result.transaction_metrics.series | Where-Object { $_.stage -eq $name -and $_.outcome -eq $outcome })[0]
}
function Delta($after, $before, [string]$property) { return [double]$after.$property - [double]$before.$property }

function Read-Trial([System.IO.FileInfo]$file, [string]$variant) {
    $result = Get-Content -Raw -LiteralPath $file.FullName | ConvertFrom-Json
    $metrics = $result.k6.metrics
    $transaction = Stage $result 'transaction_attempt'
    $idempotency = Stage $result 'idempotency_lookup'
    $product = Stage $result 'product_read'
    $header = Stage $result 'order_header_insert'
    $item = Stage $result 'order_item_insert'
    $inventory = Stage $result 'inventory_update'
    $commit = Stage $result 'commit'
    $requests = [int64]$metrics.http_reqs.count
    $success = [int64]$metrics.business_success.count
    $ordersDelta = [int64]$result.after.Orders - [int64]$result.before.Orders
    $itemsDelta = [int64]$result.after.OrderItems - [int64]$result.before.OrderItems
    $stockDelta = [int64]$result.after.inventory_units - [int64]$result.before.inventory_units
    $commitCount = Delta $result.performance_schema_after $result.performance_schema_before 'commit_count'
    $commitTimeMS = (Delta $result.performance_schema_after $result.performance_schema_before 'commit_time_ps') / 1e9
    $retryCount = @($result.transaction_metrics.series | Where-Object { $_.stage -eq 'transaction_retry_delay' } | ForEach-Object { $_.durations.count } | Measure-Object -Sum).Sum
    [pscustomobject][ordered]@{
        variant=$variant; run_id=$result.run_id; source_commit=$result.source_commit; scenario=$result.scenario
        model=$result.model; target_rate=$result.configured_rate; vus=$result.vus
        duration=$result.configured_duration; warmup_seconds=$result.warmup_seconds
        requests=$requests; actual_iterations_per_second=[double]$metrics.iterations.rate
        http_requests_per_second=[double]$metrics.http_reqs.rate
        business_success=$success; business_success_per_second=[double]$metrics.business_success.rate
        business_rejected=[int64]$metrics.business_rejected.count
        dropped_iterations=[int64]$metrics.dropped_iterations.count
        http_error_rate=[double]$metrics.http_req_failed.value
        business_error_rate=[double]$metrics.business_error_rate.value
        avg_ms=[double]$metrics.http_req_duration.avg; p50_ms=[double]$metrics.http_req_duration.med
        p95_ms=[double]$metrics.http_req_duration.'p(95)'; p99_ms=[double]$metrics.http_req_duration.'p(99)'
        p99_confidence=$(if ($requests -ge 1000) { 'adequate_for_baseline' } else { 'limited_sample' })
        transaction_p95_ms=$(if ($transaction) { [double]$transaction.durations.p95_ms } else { 0 })
        idempotency_p95_ms=$(if ($idempotency) { [double]$idempotency.durations.p95_ms } else { 0 })
        product_p95_ms=$(if ($product) { [double]$product.durations.p95_ms } else { 0 })
        order_header_p95_ms=$(if ($header) { [double]$header.durations.p95_ms } else { 0 })
        order_item_p95_ms=$(if ($item) { [double]$item.durations.p95_ms } else { 0 })
        inventory_p95_ms=$(if ($inventory) { [double]$inventory.durations.p95_ms } else { 0 })
        commit_p95_ms=$(if ($commit) { [double]$commit.durations.p95_ms } else { 0 })
        recorder_overhead_average_us=1000 * [double]$result.transaction_metrics.recorder_overhead.average_ms
        pool_wait_count_delta=(Delta $result.runtime_after.database $result.runtime_before.database 'wait_count')
        pool_wait_duration_ms=(Delta $result.runtime_after.database $result.runtime_before.database 'wait_duration_ns') / 1e6
        innodb_row_lock_waits_delta=(Delta $result.performance_schema_after $result.performance_schema_before 'innodb_row_lock_waits')
        innodb_row_lock_time_ms_delta=(Delta $result.performance_schema_after $result.performance_schema_before 'innodb_row_lock_time_ms')
        innodb_deadlocks_delta=(Delta $result.performance_schema_after $result.performance_schema_before 'innodb_deadlocks')
        commit_count_delta=$commitCount; commit_time_ms_delta=$commitTimeMS
        commit_average_ms=$(if ($commitCount -gt 0) { $commitTimeMS / $commitCount } else { 0 })
        retry_count=$retryCount; orders_delta=$ordersDelta; order_items_delta=$itemsDelta; inventory_units_delta=$stockDelta
        consistency_valid=([bool]$result.after.valid -and $ordersDelta -eq $success -and $itemsDelta -eq $success -and $stockDelta -eq -$success)
    }
}

$trials = @()
foreach ($variant in @('a3','b')) {
    foreach ($file in Get-ChildItem -LiteralPath $rawDirectory -Filter "stage9-ab-$variant-*-result.json") {
        $trials += Read-Trial $file $variant
    }
}
if (@($trials | Where-Object variant -eq 'a3').Count -ne 27 -or @($trials | Where-Object variant -eq 'b').Count -ne 27) {
    throw 'Expected exactly 27 A3 and 27 B trials'
}
if (@($trials | Where-Object { -not $_.consistency_valid -or $_.http_error_rate -ne 0 -or $_.business_error_rate -ne 0 }).Count -ne 0) {
    throw 'A/B trial correctness or error-rate verification failed'
}

$aggregates = foreach ($grouping in $trials | Group-Object variant,scenario,target_rate) {
    $group = @($grouping.Group)
    [pscustomobject][ordered]@{
        variant=$group[0].variant; source_commit=($group.source_commit | Sort-Object -Unique) -join ','
        scenario=$group[0].scenario; target_rate=[int]$group[0].target_rate; rounds=$group.Count
        median_actual_iterations_per_second=Median (Values $group 'actual_iterations_per_second')
        median_business_success_per_second=Median (Values $group 'business_success_per_second')
        median_dropped_iterations=Median (Values $group 'dropped_iterations')
        median_avg_ms=Median (Values $group 'avg_ms'); median_p50_ms=Median (Values $group 'p50_ms')
        median_p95_ms=Median (Values $group 'p95_ms'); median_p99_ms=Median (Values $group 'p99_ms')
        median_transaction_p95_ms=Median (Values $group 'transaction_p95_ms')
        median_idempotency_p95_ms=Median (Values $group 'idempotency_p95_ms')
        median_product_p95_ms=Median (Values $group 'product_p95_ms')
        median_order_header_p95_ms=Median (Values $group 'order_header_p95_ms')
        median_order_item_p95_ms=Median (Values $group 'order_item_p95_ms')
        median_inventory_p95_ms=Median (Values $group 'inventory_p95_ms')
        median_commit_p95_ms=Median (Values $group 'commit_p95_ms')
        median_row_lock_waits_delta=Median (Values $group 'innodb_row_lock_waits_delta')
        median_row_lock_time_ms_delta=Median (Values $group 'innodb_row_lock_time_ms_delta')
        median_pool_wait_count_delta=Median (Values $group 'pool_wait_count_delta')
        deadlocks_total=($group | Measure-Object innodb_deadlocks_delta -Sum).Sum
        retries_total=($group | Measure-Object retry_count -Sum).Sum
        max_http_error_rate=($group | Measure-Object http_error_rate -Maximum).Maximum
        all_consistent=(@($group | Where-Object { -not $_.consistency_valid }).Count -eq 0)
    }
}

$comparisons = foreach ($before in $aggregates | Where-Object variant -eq 'a3') {
    $after = $aggregates | Where-Object { $_.variant -eq 'b' -and $_.scenario -eq $before.scenario -and $_.target_rate -eq $before.target_rate }
    $percent = { param($a,$b) if ([double]$a -eq 0) { return $null }; return 100 * ([double]$b - [double]$a) / [double]$a }
    [pscustomobject][ordered]@{
        scenario=$before.scenario; target_rate=$before.target_rate
        a_actual_iterations_per_second=$before.median_actual_iterations_per_second
        b_actual_iterations_per_second=$after.median_actual_iterations_per_second
        throughput_delta_percent=& $percent $before.median_actual_iterations_per_second $after.median_actual_iterations_per_second
        a_p95_ms=$before.median_p95_ms; b_p95_ms=$after.median_p95_ms
        p95_delta_percent=& $percent $before.median_p95_ms $after.median_p95_ms
        a_p99_ms=$before.median_p99_ms; b_p99_ms=$after.median_p99_ms
        p99_delta_percent=& $percent $before.median_p99_ms $after.median_p99_ms
        a_dropped_iterations=$before.median_dropped_iterations; b_dropped_iterations=$after.median_dropped_iterations
        dropped_delta_percent=& $percent $before.median_dropped_iterations $after.median_dropped_iterations
        a_inventory_p95_ms=$before.median_inventory_p95_ms; b_inventory_p95_ms=$after.median_inventory_p95_ms
        inventory_p95_delta_percent=& $percent $before.median_inventory_p95_ms $after.median_inventory_p95_ms
        a_row_lock_time_ms=$before.median_row_lock_time_ms_delta; b_row_lock_time_ms=$after.median_row_lock_time_ms_delta
        a_pool_wait_count=$before.median_pool_wait_count_delta; b_pool_wait_count=$after.median_pool_wait_count_delta
    }
}

$limited = @(Get-ChildItem -LiteralPath $rawDirectory -Filter 'stage9-limited-hot-q200-*-result.json' | ForEach-Object { Read-Trial $_ 'limited-stock' })
$fixedVU = Read-Trial (Get-Item -LiteralPath (Join-Path $rawDirectory 'stage9-fixedvu-hot-vu20-result.json')) 'fixed-vu'
$diagnosticsOff = @(Get-ChildItem -LiteralPath $rawDirectory -Filter 'stage9-diagoff-dist-q100-*-result.json' | ForEach-Object { Read-Trial $_ 'diagnostics-off' })
$diagnosticsOn = @($trials | Where-Object { $_.variant -eq 'b' -and $_.scenario -eq 'order-create-distributed' -and $_.target_rate -eq 100 })
$diagnosticOverhead = [ordered]@{
    on_median_rps=Median (Values $diagnosticsOn 'actual_iterations_per_second')
    off_median_rps=Median (Values $diagnosticsOff 'actual_iterations_per_second')
    on_median_p50_ms=Median (Values $diagnosticsOn 'p50_ms'); off_median_p50_ms=Median (Values $diagnosticsOff 'p50_ms')
    on_median_p95_ms=Median (Values $diagnosticsOn 'p95_ms'); off_median_p95_ms=Median (Values $diagnosticsOff 'p95_ms')
    on_median_p99_ms=Median (Values $diagnosticsOn 'p99_ms'); off_median_p99_ms=Median (Values $diagnosticsOff 'p99_ms')
    collector_average_us_median=Median (Values $diagnosticsOn 'recorder_overhead_average_us')
}

$stage8 = Get-Content -Raw -LiteralPath .\tests\performance\results\baseline-summary.json | ConvertFrom-Json
$document = [ordered]@{
    generated_at_utc=[DateTime]::UtcNow.ToString('o')
    stage8_source_commit=$stage8.source_commit
    a_source_commit='09f884f23fde5219ff6af0e67c4024def80a0267'
    b_source_commit='96ff2506a5728fc0b90fe8c46b9e954c1d0dfa58'
    formula='percent_delta = (B_median - A_median) / A_median * 100'
    calibration=[ordered]@{
        arrival_rate_limit='RATE is capped at 200 requests/s only for constant-arrival-rate; shared-iterations is a finite burst with no rate cap'
        sample_window='2s warm-up excluded from deltas, 8s formal sample, up to 3s graceful stop'
        limits='25 max VUs, 200 target requests/s, 3s request timeout'
        p99='under 1000 requests per round is marked limited_sample; tail differences are not treated as stable improvements'
    }
    aggregates=$aggregates
    comparisons=$comparisons
    trials=$trials
    limited_stock_trials=$limited
    fixed_vu_trial=$fixedVU
    diagnostic_overhead=$diagnosticOverhead
    diagnostics_off_trials=$diagnosticsOff
}
$document | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $outputDirectory 'hotspot-comparison.json') -Encoding utf8NoBOM
$trials | Export-Csv -LiteralPath (Join-Path $outputDirectory 'hotspot-trials.csv') -NoTypeInformation -Encoding utf8NoBOM
$comparisons | Export-Csv -LiteralPath (Join-Path $outputDirectory 'hotspot-comparison.csv') -NoTypeInformation -Encoding utf8NoBOM

$hot = $comparisons | Where-Object { $_.scenario -eq 'order-create-hotspot' -and $_.target_rate -eq 200 }
$culture = [Globalization.CultureInfo]::InvariantCulture
function F([double]$value) { return $value.ToString('0.##', $culture) }
$svg = @"
<svg xmlns="http://www.w3.org/2000/svg" width="920" height="390" viewBox="0 0 920 390" role="img" aria-labelledby="title desc">
  <title id="title">Single-hotspot SKU A/B comparison at target 200 requests per second</title>
  <desc id="desc">Median of three runs. Higher throughput is better; lower latency and dropped iterations are better.</desc>
  <rect width="920" height="390" fill="#ffffff"/>
  <text x="40" y="42" font-family="Segoe UI,Arial" font-size="24" fill="#172033">Single-hotspot SKU: A/B median of 3 runs</text>
  <text x="40" y="68" font-family="Segoe UI,Arial" font-size="14" fill="#556176">25 max VUs · target 200 req/s · 2s warm-up + 8s sample</text>
  <g font-family="Segoe UI,Arial" font-size="14" fill="#172033">
    <text x="40" y="120">Actual iterations/s</text><text x="40" y="210">HTTP P95 (ms)</text><text x="40" y="300">Dropped iterations</text>
  </g>
  <g fill="#8091a7"><rect x="220" y="96" width="$(F (2*$hot.a_actual_iterations_per_second))" height="24"/><rect x="220" y="186" width="$(F (2*$hot.a_p95_ms))" height="24"/><rect x="220" y="276" width="$(F (0.7*$hot.a_dropped_iterations))" height="24"/></g>
  <g fill="#1677ff"><rect x="220" y="126" width="$(F (2*$hot.b_actual_iterations_per_second))" height="24"/><rect x="220" y="216" width="$(F (2*$hot.b_p95_ms))" height="24"/><rect x="220" y="306" width="$(F (0.7*$hot.b_dropped_iterations))" height="24"/></g>
  <g font-family="Segoe UI,Arial" font-size="13" fill="#172033">
    <text x="190" y="113" text-anchor="end">A</text><text x="190" y="143" text-anchor="end">B</text><text x="$(F (230+2*$hot.a_actual_iterations_per_second))" y="113">$(F $hot.a_actual_iterations_per_second)</text><text x="$(F (230+2*$hot.b_actual_iterations_per_second))" y="143">$(F $hot.b_actual_iterations_per_second)</text>
    <text x="190" y="203" text-anchor="end">A</text><text x="190" y="233" text-anchor="end">B</text><text x="$(F (230+2*$hot.a_p95_ms))" y="203">$(F $hot.a_p95_ms)</text><text x="$(F (230+2*$hot.b_p95_ms))" y="233">$(F $hot.b_p95_ms)</text>
    <text x="190" y="293" text-anchor="end">A</text><text x="190" y="323" text-anchor="end">B</text><text x="$(F (230+0.7*$hot.a_dropped_iterations))" y="293">$(F $hot.a_dropped_iterations)</text><text x="$(F (230+0.7*$hot.b_dropped_iterations))" y="323">$(F $hot.b_dropped_iterations)</text>
  </g>
  <text x="40" y="365" font-family="Segoe UI,Arial" font-size="12" fill="#68758a">A: original transaction order · B: item snapshots inserted before inventory locks</text>
</svg>
"@
$svg | Set-Content -LiteralPath (Join-Path $outputDirectory 'hotspot-comparison.svg') -Encoding utf8NoBOM
Write-Output "Wrote $($trials.Count) A/B trials, $($comparisons.Count) comparisons, and supporting validations"
