param(
    [Parameter(Mandatory=$true)]
    [ValidateSet('products-list-hot','products-list-cursor','product-detail-hot','product-detail-random','order-create','inventory-competition','idempotency-concurrent','idempotency-cross-user','order-query','order-cancel','order-create-distributed','order-create-skewed','order-create-hotspot','order-create-hotspot-limited')]
    [string]$Scenario,
    [ValidateSet('constant-vus','constant-arrival-rate','shared-iterations')]
    [string]$Model = 'constant-vus',
    [ValidateRange(1,25)][int]$VUs = 1,
    [ValidateRange(1,200)][int]$Rate = 10,
    [ValidateRange(1,10000)][int]$Iterations = 1,
    [ValidateRange(10,10000)][int]$IterationSleepMS = 100,
    [ValidatePattern('^([1-9]|[1-5][0-9]|60)s$')][string]$Duration = '5s',
    [ValidateRange(0,10)][int]$WarmupSeconds = 0,
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-zA-Z0-9._-]{1,40}$')][string]$RunID,
    [switch]$AllowDroppedIterations,
    [switch]$CaptureCPUProfile
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'source-snapshot.ps1')
$sourceSnapshotSHA256 = Get-SourceSnapshotSHA256 -Root $projectRoot
Set-Location $projectRoot
. .\scripts\performance-env.ps1
$stateDirectory = Join-Path $projectRoot '.tmp\performance'
$manifest = Join-Path $stateDirectory 'manifest.json'
$k6 = Join-Path $projectRoot '.tmp\tools\k6-v2.3.0-windows-amd64\k6.exe'
if (-not (Test-Path -LiteralPath $k6)) { throw 'Run scripts/install-k6-local.ps1 first' }
if (-not (Test-Path -LiteralPath $manifest)) { throw 'Run scripts/performance-start.ps1 -ResetData first' }
$pidPath = Join-Path $stateDirectory 'server.pid'
if (-not (Test-Path -LiteralPath $pidPath)) { throw 'Performance server PID is missing' }
$serverPID = [int](Get-Content -LiteralPath $pidPath)
$server = Get-Process -Id $serverPID -ErrorAction Stop
$expectedBinary = Join-Path $stateDirectory 'order-api-showcase-perf.exe'
if ($server.Path -ne $expectedBinary) { throw 'Recorded PID is not the performance server' }

$rawDirectory = Join-Path $stateDirectory 'raw'
New-Item -ItemType Directory -Path $rawDirectory -Force | Out-Null
$summaryPath = Join-Path $rawDirectory "$RunID-k6.json"
$envelopePath = Join-Path $rawDirectory "$RunID-result.json"
$samplesPath = Join-Path $rawDirectory "$RunID-resources.json"
$stdoutPath = Join-Path $rawDirectory "$RunID-k6.stdout.log"
$stderrPath = Join-Path $rawDirectory "$RunID-k6.stderr.log"
$warmupSummaryPath = Join-Path $rawDirectory "$RunID-warmup-k6.json"
$warmupStdoutPath = Join-Path $rawDirectory "$RunID-warmup-k6.stdout.log"
$warmupStderrPath = Join-Path $rawDirectory "$RunID-warmup-k6.stderr.log"

$previous = @{}
foreach ($name in 'PERF_MANIFEST','PERF_SCENARIO','BASE_URL','RUN_ID','LOAD_MODEL','VUS','RATE','ITERATIONS','DURATION','REQUEST_TIMEOUT','ITERATION_SLEEP_MS','ALLOW_DROPPED_ITERATIONS') {
    $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$env:PERF_MANIFEST = $manifest
$env:PERF_SCENARIO = $Scenario
$env:BASE_URL = 'http://127.0.0.1:28081'
$env:RUN_ID = $RunID
$env:LOAD_MODEL = $Model
$env:VUS = [string]$VUs
$env:RATE = [string]$Rate
$env:ITERATIONS = [string]$Iterations
$env:DURATION = $Duration
$env:REQUEST_TIMEOUT = '3s'
$env:ITERATION_SLEEP_MS = if ($Model -eq 'constant-vus') { [string]$IterationSleepMS } else { '0' }
$env:ALLOW_DROPPED_ITERATIONS = $AllowDroppedIterations.IsPresent.ToString().ToLowerInvariant()
$formalDuration = $env:DURATION
$formalRunID = $env:RUN_ID
if ($WarmupSeconds -gt 0) {
    $env:DURATION = "${WarmupSeconds}s"
    $env:RUN_ID = "$RunID-warmup"
    $warmup = Start-Process -FilePath $k6 -ArgumentList @('run','--quiet','--summary-export',$warmupSummaryPath,'.\tests\performance\workload.js') `
        -PassThru -WindowStyle Hidden -Wait -RedirectStandardOutput $warmupStdoutPath -RedirectStandardError $warmupStderrPath
    if ($warmup.ExitCode -ne 0) { throw "k6 warm-up failed with exit code $($warmup.ExitCode); inspect $warmupStderrPath" }
    $env:DURATION = $formalDuration
    $env:RUN_ID = $formalRunID
}

Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:28081/debug/transactions/reset' -TimeoutSec 2 | Out-Null
$before = go run .\cmd\perf-verify -expected-database $env:DB_NAME | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Pre-run consistency verification failed' }
$databaseBefore = & .\scripts\get-performance-schema-snapshot.ps1 | ConvertFrom-Json
$runtimeBefore = Invoke-RestMethod -Uri 'http://127.0.0.1:28081/debug/runtime' -TimeoutSec 2
$startedAt = [DateTime]::UtcNow
$cpuStart = $server.TotalProcessorTime.TotalSeconds
$samples = [System.Collections.Generic.List[object]]::new()
$profileJob = $null
try {
    if ($CaptureCPUProfile) {
        $profilePath = Join-Path $stateDirectory "$RunID-cpu.pprof"
        $profileJob = Start-Job -ScriptBlock { param($Path) Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:28081/debug/pprof/profile?seconds=5' -OutFile $Path } -ArgumentList $profilePath
    }
    $k6Process = Start-Process -FilePath $k6 -ArgumentList @('run','--quiet','--summary-export',$summaryPath,'.\tests\performance\workload.js') `
        -PassThru -WindowStyle Hidden -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
    while (-not $k6Process.HasExited) {
        try {
            $runtime = Invoke-RestMethod -Uri 'http://127.0.0.1:28081/debug/runtime' -TimeoutSec 2
            $server.Refresh()
            $mysql = docker stats order-api-showcase-perf-mysql --no-stream --format '{{json .}}' | ConvertFrom-Json
            $samples.Add([pscustomobject]@{ captured_at_utc=[DateTime]::UtcNow.ToString('o'); server_cpu_seconds=$server.TotalProcessorTime.TotalSeconds; server_working_set_bytes=$server.WorkingSet64; runtime=$runtime.runtime; database=$runtime.database; mysql_cpu_percent=$mysql.CPUPerc; mysql_memory=$mysql.MemUsage })
        } catch { }
        Start-Sleep -Milliseconds 500
        $k6Process.Refresh()
    }
    if ($profileJob) {
        Wait-Job -Job $profileJob -Timeout 15 | Out-Null
        if ($profileJob.State -ne 'Completed') { throw 'CPU profile collection did not complete' }
        Receive-Job -Job $profileJob | Out-Null
    }
    if ($k6Process.ExitCode -ne 0) { throw "k6 failed with exit code $($k6Process.ExitCode); inspect $stderrPath" }
} finally {
    if ($profileJob) { Remove-Job -Job $profileJob -Force -ErrorAction SilentlyContinue }
    foreach ($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
}
$finishedAt = [DateTime]::UtcNow
$transactions = Invoke-RestMethod -Uri 'http://127.0.0.1:28081/debug/transactions' -TimeoutSec 2
$databaseAfter = & .\scripts\get-performance-schema-snapshot.ps1 | ConvertFrom-Json
$runtimeAfter = Invoke-RestMethod -Uri 'http://127.0.0.1:28081/debug/runtime' -TimeoutSec 2
$after = go run .\cmd\perf-verify -expected-database $env:DB_NAME | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw 'Post-run consistency verification failed' }
$summary = Get-Content -Raw -LiteralPath $summaryPath | ConvertFrom-Json
$samples | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $samplesPath -Encoding utf8NoBOM
$server.Refresh()
$elapsed = ($finishedAt - $startedAt).TotalSeconds
$hostCPUPercent = if ($elapsed -gt 0) { 100 * (($server.TotalProcessorTime.TotalSeconds - $cpuStart) / $elapsed) / [Environment]::ProcessorCount } else { 0 }
$envelope = [ordered]@{
    run_id=$RunID; source_snapshot_sha256=$sourceSnapshotSHA256; scenario=$Scenario; model=$Model; vus=$VUs
    configured_rate=$(if ($Model -eq 'constant-arrival-rate') { $Rate } else { $null })
    configured_iterations=$(if ($Model -eq 'shared-iterations') { $Iterations } else { $null })
    configured_duration=$Duration; warmup_seconds=$WarmupSeconds; allow_dropped_iterations=$AllowDroppedIterations.IsPresent
    executor_limit_note=$(if ($Model -eq 'constant-arrival-rate') { 'RATE is capped at 200 requests/s and maxVUs at 25' } elseif ($Model -eq 'constant-vus') { 'closed model; RATE is not applied' } else { 'finite burst; RATE and configured duration are not throughput limits' })
    started_at_utc=$startedAt.ToString('o'); finished_at_utc=$finishedAt.ToString('o'); wall_duration_seconds=$elapsed
    normalized_host_cpu_percent=$hostCPUPercent; before=$before; after=$after; resource_samples=$samples
    runtime_before=$runtimeBefore; runtime_after=$runtimeAfter
    performance_schema_before=$databaseBefore; performance_schema_after=$databaseAfter
    transaction_metrics=$transactions; k6=$summary
}
$envelope | ConvertTo-Json -Depth 30 | Set-Content -LiteralPath $envelopePath -Encoding utf8NoBOM
Write-Output $envelopePath
