param(
    [string]$Image = 'golang:1.27.2-bookworm'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$snapshot = Join-Path $projectRoot ('.tmp\race-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $snapshot -Force | Out-Null

# Copy only source/module/SQL files. Never mount the workspace containing .env,
# the user's home, Git configuration, Docker socket, or host credentials.
foreach ($file in @('go.mod', 'go.sum')) {
    Copy-Item -LiteralPath (Join-Path $projectRoot $file) -Destination $snapshot
}
foreach ($directory in @('cmd', 'internal', 'tests', 'migrations')) {
    $sourceRoot = Join-Path $projectRoot $directory
    foreach ($file in Get-ChildItem -LiteralPath $sourceRoot -Recurse -File) {
        if ($file.Extension -notin @('.go', '.sql')) { continue }
        $relative = $file.FullName.Substring($projectRoot.Length + 1)
        $destination = Join-Path $snapshot $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
        Copy-Item -LiteralPath $file.FullName -Destination $destination
    }
}

# Pin the official Go toolchain version to go.mod. The standard Go build/test
# commands are the only container entrypoint; there are no third-party scripts.
# A read-only root, non-root UID, no added capabilities and PID/memory bounds
# limit the test container. No host environment variables are forwarded.
docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges `
    --pids-limit 256 --memory 4g --user 65534:65534 `
    --tmpfs '/tmp:rw,exec,nosuid,size=3g' `
    --mount "type=bind,source=$snapshot,target=/src,readonly" `
    -w /src -e CGO_ENABLED=1 -e GOTOOLCHAIN=local -e GOCACHE=/tmp/build -e GOPATH=/tmp/gopath `
    $Image sh -c 'go version && gcc --version && go mod download && go test -race -count=1 ./...'
if ($LASTEXITCODE -ne 0) { throw "Linux race tests failed with exit code $LASTEXITCODE" }

Write-Output 'Linux race detector passed. The ignored source-only snapshot is retained under .tmp for inspection.'
