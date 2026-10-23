$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $projectRoot
$container = docker inspect --format '{{.Name}} {{.State.Health.Status}}' order-api-showcase-perf-mysql
if ($LASTEXITCODE -ne 0 -or $container -ne '/order-api-showcase-perf-mysql healthy') {
    throw 'Exact isolated performance MySQL is not healthy'
}
$json = Get-Content -Raw -LiteralPath .\tests\performance\sql\snapshot.sql | `
    docker exec -i -e MYSQL_PWD=perf-root-not-a-secret order-api-showcase-perf-mysql mysql -uroot --batch --skip-column-names --raw
if ($LASTEXITCODE -ne 0) { throw 'Performance Schema snapshot failed' }
$json | ConvertFrom-Json | ConvertTo-Json -Compress -Depth 4
