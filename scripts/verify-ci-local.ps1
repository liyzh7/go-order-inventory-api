param(
    [string]$GoImage = 'golang@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61',
    [string]$MySQLImage = 'container-registry.oracle.com/mysql/community-server@sha256:7dcc4add9183664de3a214daf85a50c3ba6cccfd7534f700b6561bf5b41885be'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$suffix = [guid]::NewGuid().ToString('N').Substring(0, 12)
$network = "order-api-showcase-ci-$suffix"
$mysqlName = "order-api-showcase-ci-mysql-$suffix"
$runnerName = "order-api-showcase-ci-go-$suffix"
$snapshot = Join-Path $projectRoot ".tmp\ci-$suffix"
New-Item -ItemType Directory -Path $snapshot -Force | Out-Null
foreach ($file in @('go.mod', 'go.sum')) {
    Copy-Item -LiteralPath (Join-Path $projectRoot $file) -Destination $snapshot
}
foreach ($directory in @('cmd', 'internal', 'tests', 'migrations', 'scripts')) {
    foreach ($file in Get-ChildItem -LiteralPath (Join-Path $projectRoot $directory) -Recurse -File) {
        if ($file.Extension -notin @('.go', '.sql', '.sh')) { continue }
        $destination = Join-Path $snapshot $file.FullName.Substring($projectRoot.Length + 1)
        New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
        Copy-Item -LiteralPath $file.FullName -Destination $destination
    }
}

$networkCreated = $false
$mysqlCreated = $false
$runnerCreated = $false
try {
    # Unique task-owned network and an unpublished MySQL service. There is no
    # path, credential or port shared with the development database.
    docker network create $network | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not create isolated CI network' }
    $networkCreated = $true
    docker run --detach --rm --cpus 2 --memory 2g --pids-limit 256 --name $mysqlName --network $network --network-alias mysql-ci `
        -e MYSQL_DATABASE=order_api_ci -e MYSQL_USER=order_ci -e MYSQL_PASSWORD=ci-app-not-a-secret `
        -e MYSQL_ROOT_PASSWORD=ci-root-not-a-secret -e MYSQL_ROOT_HOST=% -e TZ=UTC `
        --health-cmd "MYSQL_PWD=ci-app-not-a-secret mysql --protocol=tcp -h 127.0.0.1 -u order_ci order_api_ci --batch --skip-column-names -e 'SELECT 1'" `
        --health-interval 5s --health-timeout 5s --health-retries 24 --health-start-period 30s `
        $MySQLImage | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated CI MySQL' }
    $mysqlCreated = $true
    $deadline = [DateTime]::UtcNow.AddMinutes(3)
    do {
        $health = docker inspect --format '{{.State.Health.Status}}' $mysqlName
        if ($health -eq 'healthy') { break }
        if ($health -eq 'unhealthy' -or [DateTime]::UtcNow -ge $deadline) { throw 'Isolated CI MySQL failed health check' }
        Start-Sleep -Seconds 2
    } while ($true)

    # No host env values are forwarded, and .env/Git/home are never mounted.
    # The sole write area in this non-root runner is ephemeral /tmp.
    docker run --detach --rm --read-only --name $runnerName --network $network `
        --user 65534:65534 --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --cpus 2 --memory 4g `
        --tmpfs '/tmp:rw,exec,nosuid,size=3g' --mount "type=bind,source=$snapshot,target=/src,readonly" `
        -w /src -e CGO_ENABLED=1 -e GOTOOLCHAIN=local -e GOCACHE=/tmp/build -e GOPATH=/tmp/gopath `
        -e DB_HOST=mysql-ci -e DB_PORT=3306 -e DB_NAME=order_api_ci -e DB_USER=order_ci `
        -e DB_PASSWORD=ci-app-not-a-secret -e MYSQL_ROOT_PASSWORD=ci-root-not-a-secret `
        -e TEST_MYSQL_HOST=mysql-ci -e TEST_MYSQL_PORT=3306 `
        $GoImage sleep 7200 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated Go CI runner' }
    $runnerCreated = $true
    docker exec $runnerName bash scripts/test-ci-linux.sh all
    if ($LASTEXITCODE -ne 0) { throw 'Local Linux CI checks failed' }
    # Exercise the manual CLI only on the new, empty CI base database, never on
    # development sessions. Positive deletion is covered in isolated tests.
    docker exec $runnerName go run ./cmd/session-cleanup -retention 168h -batch-size 2 -max-batches 2 -timeout 10s
    if ($LASTEXITCODE -ne 0) { throw 'Cleanup CLI smoke check failed' }
    docker exec -e MYSQL_PWD=ci-root-not-a-secret $mysqlName mysql -uroot --batch --skip-column-names -e `
        "SELECT VERSION(); SELECT version_id FROM order_api_ci.goose_db_version ORDER BY id DESC LIMIT 1; SELECT COUNT(*) FROM information_schema.schemata WHERE LEFT(schema_name,13) = 'order_api_it_';"
    if ($LASTEXITCODE -ne 0) { throw 'Final CI state query failed' }
    Write-Output 'Local Linux unit, unit race, MySQL integration and integration race checks passed; this is not a GitHub Actions run.'
}
finally {
    # Delete only resources created successfully above with exact generated
    # names. --rm also removes each container's own anonymous data volume.
    if ($runnerCreated) { docker stop --time 10 $runnerName | Out-Null }
    if ($mysqlCreated) { docker stop --time 10 $mysqlName | Out-Null }
    if ($networkCreated) { docker network rm $network | Out-Null }
}
