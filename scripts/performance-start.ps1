param(
    [switch]$ResetData,
    [int]$Products = 1000,
    [int]$Users = 25,
    [int]$CancellationOrders = 500,
    [int]$CompetitionStock = 100,
    [long]$Seed = 20261016,
    [bool]$TransactionDiagnostics = $true,
    [switch]$StabilityLimits
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot
. .\scripts\performance-env.ps1
$env:TRANSACTION_DIAGNOSTICS_ENABLED = $TransactionDiagnostics.ToString().ToLowerInvariant()
$stateDirectory = Join-Path $projectRoot '.tmp\performance'
New-Item -ItemType Directory -Path $stateDirectory -Force | Out-Null
$binary = Join-Path $stateDirectory 'order-api-showcase-perf.exe'
$pidPath = Join-Path $stateDirectory 'server.pid'
if (Test-Path -LiteralPath $pidPath) {
    $oldPID = [int](Get-Content -LiteralPath $pidPath)
    $old = Get-Process -Id $oldPID -ErrorAction SilentlyContinue
    if ($old -and $old.Path -eq $binary) { throw 'Performance server is already running; stop it before reseeding or restarting' }
}

$composeArgs=@('-f','.\deploy\performance.compose.yaml')
if($StabilityLimits){$composeArgs+=@('-f','.\tests\stability\resources.compose.yaml')}
docker compose @composeArgs up -d
if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated performance MySQL' }
$deadline = [DateTime]::UtcNow.AddMinutes(3)
do {
    $health = docker inspect --format '{{.State.Health.Status}}' order-api-showcase-perf-mysql
    if ($health -eq 'healthy') { break }
    if ($health -eq 'unhealthy' -or [DateTime]::UtcNow -ge $deadline) { throw 'Performance MySQL failed its health check' }
    Start-Sleep -Seconds 2
} while ($true)

go run .\cmd\migrate up
if ($LASTEXITCODE -ne 0) { throw 'Performance database migration failed' }
if ($ResetData) {
    $manifest = go run .\cmd\perf-seed -allow-reset -expected-database $env:DB_NAME -products $Products -users $Users `
        -cancellation-orders $CancellationOrders -competition-stock $CompetitionStock -seed $Seed
    if ($LASTEXITCODE -ne 0) { throw 'Performance seed failed' }
    Set-Content -LiteralPath (Join-Path $stateDirectory 'manifest.json') -Value $manifest -Encoding utf8NoBOM
} elseif (-not (Test-Path -LiteralPath (Join-Path $stateDirectory 'manifest.json'))) {
    throw 'No local manifest exists; rerun with -ResetData to create explicit synthetic data'
}

go build -o $binary .\cmd\server
if ($LASTEXITCODE -ne 0) { throw 'Performance server build failed' }
$process = Start-Process -FilePath $binary -PassThru -WindowStyle Hidden `
    -RedirectStandardOutput (Join-Path $stateDirectory 'server.stdout.log') `
    -RedirectStandardError (Join-Path $stateDirectory 'server.stderr.log')
Set-Content -LiteralPath $pidPath -Value $process.Id -Encoding ascii
$deadline = [DateTime]::UtcNow.AddSeconds(30)
do {
    try {
        $health = Invoke-RestMethod -Uri 'http://127.0.0.1:28081/health' -TimeoutSec 2
        $runtime = Invoke-RestMethod -Uri 'http://127.0.0.1:28081/debug/runtime' -TimeoutSec 2
        if ($health.status -eq 'ok' -and $runtime.database.max_open_connections -eq 25) { break }
    } catch {
        if ($process.HasExited) { throw 'Performance server exited during startup; inspect .tmp/performance/server.stderr.log' }
    }
    if ([DateTime]::UtcNow -ge $deadline) { throw 'Performance server did not become healthy' }
    Start-Sleep -Milliseconds 500
} while ($true)
Write-Output "Performance environment ready: MySQL=$env:DB_NAME, API=http://127.0.0.1:28081, PID=$($process.Id)"
