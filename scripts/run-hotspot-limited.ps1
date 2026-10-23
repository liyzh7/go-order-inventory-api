param(
    [ValidateRange(1,3)][int]$Rounds = 3,
    [ValidatePattern('^([5-9]|[1-5][0-9]|60)s$')][string]$Duration = '8s'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot
foreach ($round in 1..$Rounds) {
    & .\scripts\performance-reset.ps1 -CompetitionStock 500 -Seed 20261016
    & .\scripts\run-performance.ps1 -Scenario order-create-hotspot-limited -Model constant-arrival-rate -VUs 25 `
        -Rate 200 -Duration $Duration -WarmupSeconds 0 `
        -RunID "stage9-limited-hot-q200-r$round" -AllowDroppedIterations
}
