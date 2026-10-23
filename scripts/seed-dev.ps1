$ErrorActionPreference = 'Stop'

$projectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
. (Join-Path $PSScriptRoot 'load-env.ps1')

$composeFile = Join-Path $projectRoot 'deploy\compose.yaml'
$envFile = Join-Path $projectRoot '.env'
$seedFile = Join-Path $projectRoot 'deploy\mysql\seed-dev.sql'

$hadMySQLPassword = Test-Path Env:MYSQL_PWD
$previousMySQLPassword = $env:MYSQL_PWD
try {
    # Pass only the variable name to Docker so the password is not expanded
    # into the host process command line.
    $env:MYSQL_PWD = $env:DB_PASSWORD
    Get-Content -LiteralPath $seedFile -Raw |
        docker compose --env-file $envFile -f $composeFile exec -T `
            -e MYSQL_PWD mysql `
            mysql --protocol=tcp -h 127.0.0.1 -u $env:DB_USER $env:DB_NAME

    if ($LASTEXITCODE -ne 0) {
        throw "development seed failed with exit code $LASTEXITCODE"
    }
}
finally {
    if ($hadMySQLPassword) {
        $env:MYSQL_PWD = $previousMySQLPassword
    }
    else {
        Remove-Item Env:MYSQL_PWD -ErrorAction SilentlyContinue
    }
}
