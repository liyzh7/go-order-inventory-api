param([switch]$StopDatabase)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$stateDirectory = Join-Path $projectRoot '.tmp\performance'
$binary = Join-Path $stateDirectory 'order-api-showcase-perf.exe'
$pidPath = Join-Path $stateDirectory 'server.pid'
if (Test-Path -LiteralPath $pidPath) {
    $processID = [int](Get-Content -LiteralPath $pidPath)
    $process = Get-Process -Id $processID -ErrorAction SilentlyContinue
    if ($process) {
        if ($process.Path -ne $binary) { throw 'Recorded PID no longer belongs to the exact performance server binary' }
        Stop-Process -Id $processID
        $process.WaitForExit(10000) | Out-Null
    }
    Remove-Item -LiteralPath $pidPath -Force
}
if ($StopDatabase) {
    Set-Location $projectRoot
    docker compose -f .\deploy\performance.compose.yaml stop
    if ($LASTEXITCODE -ne 0) { throw 'Could not stop performance MySQL' }
}
Write-Output 'Performance server stopped. The isolated MySQL volume was preserved.'
