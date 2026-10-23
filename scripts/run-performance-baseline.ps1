$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot

# Fixed-VU progression demonstrates closed-model behavior. A 100 ms sleep caps
# each VU at roughly 10 iterations/s before response time, so 20 VUs cannot
# exceed 200 requests/s.
foreach ($vus in 1,5,10,20) {
    & .\scripts\run-performance.ps1 -Scenario products-list-hot -Model constant-vus -VUs $vus -Duration 5s `
        -IterationSleepMS 100 -RunID "progress-list-vu$vus"
}

$readScenarios = @('products-list-hot','products-list-cursor','product-detail-hot','product-detail-random','order-query')
foreach ($scenario in $readScenarios) {
    foreach ($round in 1..3) {
        $profile = $scenario -eq 'products-list-hot' -and $round -eq 1
        & .\scripts\run-performance.ps1 -Scenario $scenario -Model constant-arrival-rate -VUs 10 -Rate 50 -Duration 5s `
            -RunID "baseline-$scenario-r$round" -CaptureCPUProfile:$profile
    }
}

foreach ($round in 1..3) {
    & .\scripts\performance-reset.ps1
    & .\scripts\run-performance.ps1 -Scenario order-create -Model constant-arrival-rate -VUs 10 -Rate 20 -Duration 5s `
        -RunID "baseline-order-create-r$round"
}
foreach ($round in 1..3) {
    & .\scripts\performance-reset.ps1
    & .\scripts\run-performance.ps1 -Scenario inventory-competition -Model shared-iterations -VUs 20 -Iterations 200 -Duration 5s `
        -RunID "baseline-inventory-competition-r$round"
}
foreach ($round in 1..3) {
    & .\scripts\performance-reset.ps1
    & .\scripts\run-performance.ps1 -Scenario idempotency-concurrent -Model shared-iterations -VUs 20 -Iterations 20 -Duration 5s `
        -RunID "baseline-idempotency-concurrent-r$round"
}
foreach ($round in 1..3) {
    & .\scripts\performance-reset.ps1
    & .\scripts\run-performance.ps1 -Scenario order-cancel -Model shared-iterations -VUs 20 -Iterations 200 -Duration 5s `
        -RunID "baseline-order-cancel-r$round"
}
