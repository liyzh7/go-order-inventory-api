param(
    [string]$GoImage='golang@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61',
    [string]$ResultDirectory='tests/stability/results'
)
$ErrorActionPreference='Stop'
$root=Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'source-snapshot.ps1')
$sourceSnapshotSHA256 = Get-SourceSnapshotSHA256 -Root $root
Set-Location $root
$id=[guid]::NewGuid().ToString('N').Substring(0,12)
$state=Join-Path $root ".tmp\recovery-$id"
$source=Join-Path $state 'src';$control=Join-Path $state 'control'
New-Item -ItemType Directory -Force -Path $source,$control|Out-Null
foreach($f in @('go.mod','go.sum')){Copy-Item -LiteralPath (Join-Path $root $f) -Destination $source}
foreach($dir in @('cmd','internal','tests','migrations')){foreach($file in Get-ChildItem (Join-Path $root $dir) -Recurse -File){if($file.Extension -notin @('.go','.sql')){continue};$dest=Join-Path $source $file.FullName.Substring($root.Length+1);New-Item -ItemType Directory -Force -Path (Split-Path $dest -Parent)|Out-Null;Copy-Item -LiteralPath $file.FullName -Destination $dest}}
$runner="order-api-showcase-recovery-go-$id"
$mysql='order-api-showcase-recovery-mysql';$paused=$false;$created=$false
$actions=[Collections.Generic.List[object]]::new()
function Assert-RecoveryContainer {
    $name=docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}} {{.Name}}' $mysql
    if($LASTEXITCODE -ne 0 -or $name -ne 'order-api-showcase-recovery /order-api-showcase-recovery-mysql'){throw 'Exact dedicated recovery container identity mismatch'}
}
function Wait-RecoveryMySQL {
    $deadline=[DateTime]::UtcNow.AddSeconds(60)
    do{$health=docker inspect --format '{{.State.Health.Status}}' $mysql;if($health -eq 'healthy'){return};if([DateTime]::UtcNow -ge $deadline){throw 'Recovery MySQL readiness deadline'};Start-Sleep -Seconds 1}while($true)
}
try{
    docker compose -f tests/stability/recovery.compose.yaml up -d
    if($LASTEXITCODE -ne 0){throw 'Recovery MySQL failed to start'}
    Assert-RecoveryContainer;Wait-RecoveryMySQL
    docker run --detach --name $runner --read-only --user 65534:65534 --network order-api-showcase-recovery_default --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --cpus 2 --memory 3g --tmpfs '/tmp:rw,exec,nosuid,size=2g' --mount "type=bind,source=$source,target=/src,readonly" --mount "type=bind,source=$control,target=/control" -w /src/tests/stability -e GOTOOLCHAIN=local -e CGO_ENABLED=1 -e GOCACHE=/tmp/build -e GOPATH=/tmp/gopath -e GOMAXPROCS=2 -e GOMEMLIMIT=256MiB -e DB_HOST=mysql -e DB_PORT=3306 -e TEST_MYSQL_HOST=mysql -e TEST_MYSQL_PORT=3306 -e "DB_NAME=order_api_recovery_$id" -e DB_USER=recovery_app -e DB_PASSWORD=recovery-app-not-a-secret -e MYSQL_ROOT_PASSWORD=recovery-root-not-a-secret -e RECOVERY_TEST_ENABLED=1 $GoImage sh -c 'go test -tags=recovery -timeout 6m -count=1 -v . && cd /src && go test -tags=integration -timeout 2m -count=1 -v -run ^TestCreateCommitRecoveryWithMySQL$ ./tests/integration'|Out-Null
    if($LASTEXITCODE -ne 0){throw 'Recovery runner failed to start'};$created=$true
    $deadline=[DateTime]::UtcNow.AddMinutes(8)
    while((docker inspect --format '{{.State.Running}}' $runner) -eq 'true'){
        if([DateTime]::UtcNow -ge $deadline){throw 'Recovery suite hard deadline exceeded'}
        foreach($action in @('pause','resume','restart')){
            $request=Join-Path $control "$action.request";$done=Join-Path $control "$action.done"
            if((Test-Path $request) -and -not(Test-Path $done)){
                Assert-RecoveryContainer
                $actionStart=[DateTime]::UtcNow
                switch($action){
                    pause {docker pause $mysql|Out-Null;if($LASTEXITCODE -ne 0){throw 'Pause failed'};$paused=$true;$pauseDeadline=[DateTime]::UtcNow.AddSeconds(12)}
                    resume {docker unpause $mysql|Out-Null;if($LASTEXITCODE -ne 0){throw 'Resume failed'};$paused=$false}
                    restart {docker restart --time 2 $mysql|Out-Null;if($LASTEXITCODE -ne 0){throw 'Restart failed'};Wait-RecoveryMySQL}
                }
                $actions.Add([pscustomobject]@{action=$action;start_utc=$actionStart.ToString('o');finish_utc=[DateTime]::UtcNow.ToString('o')})
                Set-Content -NoNewline -Encoding ascii -LiteralPath $done -Value 'ok'
            }
        }
        if($paused -and [DateTime]::UtcNow -gt $pauseDeadline){docker unpause $mysql|Out-Null;$paused=$false;throw 'Pause fail-safe timeout'}
        Start-Sleep -Milliseconds 200
    }
    docker logs $runner 2>&1|Tee-Object -FilePath (Join-Path $state 'tests.log')
    $code=[int](docker inspect --format '{{.State.ExitCode}}' $runner)
    if($code -ne 0){throw "Recovery tests failed: exit $code; evidence retained at $state"}
    $report=[ordered]@{source_snapshot_sha256=$sourceSnapshotSHA256;database="order_api_recovery_$id";actions=$actions;events=(Get-Content -Raw (Join-Path $control 'recovery-report.json')|ConvertFrom-Json);commit_injection_cases=@('committed','not_committed','verification_unavailable');passed=$true}
    New-Item -ItemType Directory -Force $ResultDirectory|Out-Null
    # A fresh name preserves earlier stages' successful and failed evidence.
    $report|ConvertTo-Json -Depth 10|Set-Content (Join-Path $ResultDirectory "recovery-$id.json") -Encoding utf8NoBOM
}finally{
    if($paused){docker unpause $mysql|Out-Null}
    if($created){docker stop --time 5 $runner|Out-Null}
    # Stop only this exact fault instance. Its named Volume and test schemas remain.
    docker compose -f tests/stability/recovery.compose.yaml stop
}
