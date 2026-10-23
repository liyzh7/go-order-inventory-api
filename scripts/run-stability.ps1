param(
    [Parameter(Mandatory=$true)][ValidateSet('products-list-hot','order-create-distributed','order-create-skewed','order-create-hotspot')][string]$Scenario,
    [ValidateRange(1,200)][int]$Rate = 50,
    [ValidateRange(1,600)][int]$Seconds = 300,
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-zA-Z0-9-]{1,30}$')][string]$RunID,
    [ValidateRange(1,30)][int]$CooldownSeconds = 30
)
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'source-snapshot.ps1')
$sourceSnapshotSHA256 = Get-SourceSnapshotSHA256 -Root $projectRoot
Set-Location $projectRoot
. .\scripts\performance-env.ps1
$raw = Join-Path $projectRoot '.tmp\stability'
New-Item -ItemType Directory -Force -Path $raw | Out-Null
if(Get-ChildItem -LiteralPath $raw -Filter "$RunID-*" -File){throw 'RunID already has evidence; select a new RunID instead of overwriting or mixing interrupted output'}
$k6 = Join-Path $projectRoot '.tmp\tools\k6-v2.3.0-windows-amd64\k6.exe'
$server = Get-Process -Id ([int](Get-Content .tmp/performance/server.pid))
if ($server.Path -ne (Join-Path $projectRoot '.tmp\performance\order-api-showcase-perf.exe')) { throw 'Not the exact isolated performance process' }
if ((Get-PSDrive D).Free -lt 20GB -or (Get-PSDrive C).Free -lt 20GB) { throw 'Insufficient free disk budget' }
$summaryPath = Join-Path $raw "$RunID-k6.json"
$env:PERF_MANIFEST=Join-Path $projectRoot '.tmp\performance\manifest.json'
$env:PERF_SCENARIO=$Scenario; $env:BASE_URL='http://127.0.0.1:28081'; $env:RUN_ID=$RunID
$env:LOAD_MODEL='constant-arrival-rate'; $env:VUS='25'; $env:RATE=[string]$Rate
$env:DURATION='5s'; $env:REQUEST_TIMEOUT='3s'; $env:ITERATION_SLEEP_MS='0'; $env:ALLOW_DROPPED_ITERATIONS='false'
$env:RUN_ID="$RunID-w"
& $k6 run --quiet tests/stability/workload.js *> (Join-Path $raw "$RunID-warmup.log")
if ($LASTEXITCODE -ne 0) { throw 'Stability warm-up failed' }
$env:DURATION="${Seconds}s"; $env:RUN_ID=$RunID
Invoke-RestMethod -Method Post http://127.0.0.1:28081/debug/transactions/reset | Out-Null
$before=go run ./cmd/perf-verify -expected-database $env:DB_NAME | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Pre-run consistency check failed' }
$dbBefore=& ./scripts/get-performance-schema-snapshot.ps1 | ConvertFrom-Json
$runtimeBefore=Invoke-RestMethod http://127.0.0.1:28081/debug/runtime
$started=[DateTime]::UtcNow
$samples=[Collections.Generic.List[object]]::new()
$process=$null; $complete=$false
try {
    $process=Start-Process -FilePath $k6 -WindowStyle Hidden -PassThru -ArgumentList @('run','--quiet','--summary-export',$summaryPath,'tests/stability/workload.js') -RedirectStandardOutput (Join-Path $raw "$RunID-stdout.log") -RedirectStandardError (Join-Path $raw "$RunID-stderr.log")
    $next=$started
    while (-not $process.HasExited) {
        if (Test-Path (Join-Path $raw 'STOP')) { throw 'Manual STOP requested' }
        if (([DateTime]::UtcNow-$started).TotalSeconds -gt $Seconds+20) { throw 'Hard test deadline exceeded' }
        if ([DateTime]::UtcNow -ge $next) {
            $runtime=Invoke-RestMethod http://127.0.0.1:28081/debug/runtime -TimeoutSec 2
            $server.Refresh()
            $mysql=docker stats order-api-showcase-perf-mysql --no-stream --format '{{json .}}' | ConvertFrom-Json
            if ($LASTEXITCODE -ne 0) { throw 'Docker monitoring failed' }
            $sql=Get-Content -Raw tests/stability/monitor.sql | docker exec -i -e MYSQL_PWD=perf-root-not-a-secret order-api-showcase-perf-mysql mysql -uroot --batch --skip-column-names --raw
            if ($LASTEXITCODE -ne 0) { throw 'Database monitoring failed' }
            $sample=[pscustomobject]@{utc=[DateTime]::UtcNow.ToString('o'); elapsed_s=([DateTime]::UtcNow-$started).TotalSeconds; cpu_seconds=$server.TotalProcessorTime.TotalSeconds; working_set_bytes=$server.WorkingSet64; runtime=$runtime.runtime; pool=$runtime.database; mysql=$mysql; sql=($sql|ConvertFrom-Json)}
            $samples.Add($sample)
            # Append fixed-size aggregate samples so interrupted runs retain evidence.
            $sample|ConvertTo-Json -Depth 8 -Compress|Add-Content (Join-Path $raw "$RunID-monitor.jsonl")
            if($server.WorkingSet64 -gt 512MB -or $sample.sql.database_bytes -gt 1GB -or (Get-PSDrive D).Free -lt 20GB -or (Get-PSDrive C).Free -lt 20GB){throw 'Resource stop limit exceeded'}
            $next=$next.AddSeconds(5)
        }
        Start-Sleep -Milliseconds 250
        $process.Refresh()
    }
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { throw "k6 failed: $($process.ExitCode); see ignored stderr" }
    $complete=$true
} finally {
    if($process -and -not $process.HasExited){ Stop-Process -Id $process.Id; $process.WaitForExit(5000)|Out-Null }
}
$finished=[DateTime]::UtcNow
$runtimeEnd=Invoke-RestMethod http://127.0.0.1:28081/debug/runtime
$transactions=Invoke-RestMethod http://127.0.0.1:28081/debug/transactions
$dbAfter=& ./scripts/get-performance-schema-snapshot.ps1 | ConvertFrom-Json
$after=go run ./cmd/perf-verify -expected-database $env:DB_NAME | ConvertFrom-Json
if($LASTEXITCODE -ne 0){throw 'Post-run consistency check failed'}
$stockCheck=docker exec -e MYSQL_PWD=perf-root-not-a-secret order-api-showcase-perf-mysql mysql -uroot order_api_perf_baseline --batch --skip-column-names -e "SELECT COUNT(*) FROM inventories v WHERE v.available_qty<>1000000-COALESCE((SELECT SUM(i.quantity) FROM order_items i JOIN orders o ON o.id=i.order_id WHERE i.product_id=v.product_id AND o.status='CREATED'),0);"
if($LASTEXITCODE -ne 0 -or [long]$stockCheck -ne 0){throw 'Per-product inventory conservation failed (this suite requires one million initial stock per product)'}
Start-Sleep -Seconds $CooldownSeconds
$cooldown=Invoke-RestMethod http://127.0.0.1:28081/debug/runtime
$summary=Get-Content -Raw $summaryPath|ConvertFrom-Json
$success=[long]$summary.metrics.business_success.count
$write=$Scenario -ne 'products-list-hot'
$delta=[long]$after.Orders-[long]$before.Orders
$stock=[long]$after.inventory_units-[long]$before.inventory_units
$valid=$after.valid -and $(if($write){$delta -eq $success -and ([long]$after.OrderItems-[long]$before.OrderItems) -eq $success -and $stock -eq -$success}else{$delta -eq 0 -and $stock -eq 0})
$result=[ordered]@{run_id=$RunID; source_snapshot_sha256=$sourceSnapshotSHA256; scenario=$Scenario; rate=$Rate; max_vus=25; formal_seconds=$Seconds; warmup_seconds=5; start_utc=$started.ToString('o'); finish_utc=$finished.ToString('o'); wall_seconds=($finished-$started).TotalSeconds; cooldown_seconds=$CooldownSeconds; complete=$complete; consistent=$valid; per_product_stock_mismatches=[long]$stockCheck; before=$before; after=$after; runtime_before=$runtimeBefore; runtime_end=$runtimeEnd; runtime_cooldown=$cooldown; database_before=$dbBefore; database_after=$dbAfter; samples=$samples; transactions=$transactions; k6=$summary}
$result|ConvertTo-Json -Depth 30|Set-Content (Join-Path $raw "$RunID-result.json") -Encoding utf8NoBOM
if(-not $valid){throw 'Write delta or final invariant failed'}
Write-Output "Stability completed ${RunID}: requests=$($summary.metrics.http_reqs.count), consistent=$valid"
