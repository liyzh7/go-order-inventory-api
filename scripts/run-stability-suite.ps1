param([ValidateRange(1,600)][int]$Seconds=300,[switch]$SkipSmoke,[ValidatePattern('^[a-zA-Z0-9-]{1,16}$')][string]$RunPrefix='s10')
$ErrorActionPreference='Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$env:GOPATH=Join-Path (Get-Location) '.tmp\gopath-stage10'
# Use the caller's Go module cache; no machine-specific override.
$env:GOMEMLIMIT='256MiB'; $env:GOMAXPROCS='2'
if(Test-Path .tmp/stability/STOP){throw 'Remove the manual STOP file before starting a new suite'}
if(Test-Path .tmp/stability){if(Get-ChildItem .tmp/stability -Filter "$RunPrefix-*" -File){throw 'RunPrefix already has evidence; choose a new -RunPrefix before resetting test data'}}
& ./scripts/performance-start.ps1 -ResetData -CompetitionStock 1000000 -CancellationOrders 0 -StabilityLimits
if(-not $SkipSmoke){& ./scripts/run-stability.ps1 -Scenario order-create-hotspot -Rate 5 -Seconds 5 -RunID "$RunPrefix-smoke" -CooldownSeconds 2}
foreach($case in @(@('read','products-list-hot',100),@('distributed','order-create-distributed',50),@('skewed','order-create-skewed',50),@('hotspot','order-create-hotspot',50))){
    & ./scripts/performance-reset.ps1 -CompetitionStock 1000000 -CancellationOrders 0
    & ./scripts/run-stability.ps1 -Scenario $case[1] -Rate $case[2] -Seconds $Seconds -RunID "$RunPrefix-$($case[0])" -CooldownSeconds 30
}
