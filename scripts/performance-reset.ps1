param(
    [int]$Products = 1000,
    [int]$Users = 25,
    [int]$CancellationOrders = 500,
    [int]$CompetitionStock = 100,
    [long]$Seed = 20261016
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot
. .\scripts\performance-env.ps1
$stateDirectory = Join-Path $projectRoot '.tmp\performance'
$pidPath = Join-Path $stateDirectory 'server.pid'
if (-not (Test-Path -LiteralPath $pidPath)) { throw 'Performance server is not running' }
$server = Get-Process -Id ([int](Get-Content -LiteralPath $pidPath)) -ErrorAction Stop
if ($server.Path -ne (Join-Path $stateDirectory 'order-api-showcase-perf.exe')) { throw 'Recorded PID is not the exact performance server' }
Invoke-RestMethod -Uri 'http://127.0.0.1:28081/health' -TimeoutSec 2 | Out-Null
$manifest = go run .\cmd\perf-seed -allow-reset -expected-database $env:DB_NAME -products $Products -users $Users `
    -cancellation-orders $CancellationOrders -competition-stock $CompetitionStock -seed $Seed
if ($LASTEXITCODE -ne 0) { throw 'Performance data reset failed' }
Set-Content -LiteralPath (Join-Path $stateDirectory 'manifest.json') -Value $manifest -Encoding utf8NoBOM
Write-Output "Reset exact isolated database $env:DB_NAME with seed $Seed"
