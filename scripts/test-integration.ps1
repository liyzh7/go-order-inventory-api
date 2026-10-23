$ErrorActionPreference = 'Stop'

. (Join-Path $PSScriptRoot 'load-env.ps1')

Push-Location (Join-Path $PSScriptRoot '..')
try {
    go test -tags=integration -count=1 -v ./tests/integration
    if ($LASTEXITCODE -ne 0) {
        throw "integration tests failed with exit code $LASTEXITCODE"
    }
}
finally {
    Pop-Location
}
