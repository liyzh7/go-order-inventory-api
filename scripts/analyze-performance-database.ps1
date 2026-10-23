$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot
$container = docker inspect --format '{{.Name}} {{.State.Health.Status}}' order-api-showcase-perf-mysql
if ($LASTEXITCODE -ne 0 -or $container -ne '/order-api-showcase-perf-mysql healthy') { throw 'Exact isolated performance MySQL is not healthy' }
$outputDirectory = Join-Path $projectRoot '.tmp\performance'
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
Get-Content -Raw -LiteralPath .\tests\performance\sql\analyze.sql | `
    docker exec -i -e MYSQL_PWD=perf-root-not-a-secret order-api-showcase-perf-mysql mysql -uroot --table `
    | Set-Content -LiteralPath (Join-Path $outputDirectory 'database-analysis.txt') -Encoding utf8NoBOM
if ($LASTEXITCODE -ne 0) { throw 'Read-only performance SQL analysis failed' }
Write-Output (Join-Path $outputDirectory 'database-analysis.txt')
