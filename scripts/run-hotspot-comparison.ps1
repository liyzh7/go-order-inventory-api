param(
    [Parameter(Mandatory=$true)]
    [ValidatePattern('^[a-z0-9-]{1,20}$')]
    [string]$Variant,
    [ValidateRange(1,3)][int]$Rounds = 3,
    [ValidatePattern('^([5-9]|[1-5][0-9]|60)s$')][string]$Duration = '8s',
    [ValidateRange(0,10)][int]$WarmupSeconds = 2
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot
$scenarios = @(
    @{ Name='order-create-distributed'; Code='dist' },
    @{ Name='order-create-skewed'; Code='skew' },
    @{ Name='order-create-hotspot'; Code='hot' }
)
$rates = @(20,100,200)

foreach ($scenario in $scenarios) {
    foreach ($rate in $rates) {
        foreach ($round in 1..$Rounds) {
            & .\scripts\performance-reset.ps1 -CompetitionStock 1000000 -Seed 20261016
            & .\scripts\run-performance.ps1 -Scenario $scenario.Name -Model constant-arrival-rate -VUs 25 `
                -Rate $rate -Duration $Duration -WarmupSeconds $WarmupSeconds `
                -RunID "stage9-ab-$Variant-$($scenario.Code)-q$rate-r$round" -AllowDroppedIterations
        }
    }
}
