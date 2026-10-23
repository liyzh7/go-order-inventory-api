param([string]$Version = '2.3.0')

$ErrorActionPreference = 'Stop'
if ($Version -ne '2.3.0') { throw 'Only the reviewed k6 2.3.0 release is allowed by this script' }
$projectRoot = Split-Path -Parent $PSScriptRoot
$toolDirectory = Join-Path $projectRoot ".tmp\tools\k6-v$Version-windows-amd64"
$binary = Join-Path $toolDirectory 'k6.exe'
if (Test-Path -LiteralPath $binary) {
    & $binary version
    if ($LASTEXITCODE -ne 0) { throw 'Existing project-local k6 binary is invalid' }
    return
}
$archiveName = "k6-v$Version-windows-amd64.zip"
$expectedSHA256 = '112276d495e5741c968e2bc09ea6196099c1275bd6db9ee0875d173c7148ce43'
$temporary = Join-Path $projectRoot ".tmp\tools\$archiveName"
New-Item -ItemType Directory -Path (Split-Path -Parent $temporary) -Force | Out-Null
Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/grafana/k6/releases/download/v$Version/$archiveName" -OutFile $temporary
$actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $temporary).Hash.ToLowerInvariant()
if ($actual -ne $expectedSHA256) { throw 'Downloaded k6 archive SHA-256 did not match the reviewed release checksum' }
Expand-Archive -LiteralPath $temporary -DestinationPath (Split-Path -Parent $toolDirectory) -Force
if (-not (Test-Path -LiteralPath $binary)) { throw 'Expected k6 binary was not present after extraction' }
& $binary version
if ($LASTEXITCODE -ne 0) { throw 'Downloaded project-local k6 binary could not run' }
