param(
    [string]$GoImage='golang@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61',
    [string]$MySQLImage='container-registry.oracle.com/mysql/community-server@sha256:7dcc4add9183664de3a214daf85a50c3ba6cccfd7534f700b6561bf5b41885be'
)
$ErrorActionPreference='Stop'
$root=Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'source-snapshot.ps1')
$sourceSnapshotSHA256 = Get-SourceSnapshotSHA256 -Root $root
Set-Location $root
if((Get-PSDrive D).Free -lt 20GB -or (Get-PSDrive C).Free -lt 20GB){throw 'Backup exercise requires 20 GiB free disk headroom'}
$id=[guid]::NewGuid().ToString('N').Substring(0,12)
$state=Join-Path $root ".tmp\backups\$id"
$snapshot=Join-Path $state 'src';$control=Join-Path $state 'control';$backup=Join-Path $state 'dump'
New-Item -ItemType Directory -Path $snapshot,$control,$backup|Out-Null
foreach($file in @('go.mod','go.sum')){Copy-Item -LiteralPath (Join-Path $root $file) -Destination $snapshot}
foreach($dir in @('cmd','internal','tests','migrations')){
    foreach($file in Get-ChildItem (Join-Path $root $dir) -Recurse -File){
        if($file.Extension -notin @('.go','.sql')){continue}
        $dest=Join-Path $snapshot $file.FullName.Substring($root.Length+1)
        New-Item -ItemType Directory -Force -Path (Split-Path $dest -Parent)|Out-Null
        Copy-Item -LiteralPath $file.FullName -Destination $dest
    }
}
$sourceHashes=Get-ChildItem -LiteralPath $snapshot -Recurse -File|Sort-Object FullName|ForEach-Object { $_.FullName.Substring($snapshot.Length+1).Replace('\','/')+' '+(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
$hasher=[Security.Cryptography.SHA256]::Create()
try{$snapshotHash=([BitConverter]::ToString($hasher.ComputeHash([Text.Encoding]::UTF8.GetBytes(($sourceHashes -join "`n"))))).Replace('-','').ToLowerInvariant()}finally{$hasher.Dispose()}
$network="order-api-showcase-backup-$id";$source="order-api-showcase-backup-source-$id";$target="order-api-showcase-backup-target-$id";$runner="order-api-showcase-backup-go-$id"
$database="order_api_backup_$id";$created=[Collections.Generic.List[string]]::new()
$dumpPath=Join-Path $backup 'backup.sql';$transferred=$false
function Assert-ExactContainer([string]$name){
    $label=docker inspect --format '{{index .Config.Labels "order-api-showcase.backup-run"}}' $name
    if($LASTEXITCODE -ne 0 -or $label -ne $id -or $name -notin @($source,$target,$runner)){throw 'Exact backup container identity mismatch'}
}
try{
    docker network create --label "order-api-showcase.backup-run=$id" $network|Out-Null
    if($LASTEXITCODE -ne 0){throw 'Dedicated network creation failed'}
    foreach($pair in @(@($source,'mysql-source'),@($target,'mysql-target'))){
        $mount="type=bind,source=$backup,target=/backup";if($pair[0] -eq $target){$mount+=',readonly'}
        docker run --detach --name $pair[0] --label "order-api-showcase.backup-run=$id" --network $network --network-alias $pair[1] `
            --cpus 2 --memory 2g --pids-limit 256 --log-opt max-size=10m --log-opt max-file=2 `
            --mount "type=volume,source=$($pair[0])-data,target=/var/lib/mysql" --mount $mount `
            -e MYSQL_ROOT_PASSWORD=backup-root-not-a-secret -e 'MYSQL_ROOT_HOST=%' -e TZ=UTC `
            --health-cmd 'MYSQL_PWD=backup-root-not-a-secret mysql -uroot --batch --skip-column-names -e "SELECT 1"' `
            --health-interval 2s --health-timeout 3s --health-retries 60 --health-start-period 20s `
            $MySQLImage --innodb-buffer-pool-size=134217728 --innodb-redo-log-capacity=134217728 --max-connections=30 --skip-log-bin|Out-Null
        if($LASTEXITCODE -ne 0){throw 'Dedicated MySQL container start failed'};$created.Add($pair[0])
    }
    $deadline=[DateTime]::UtcNow.AddMinutes(3)
    foreach($name in @($source,$target)){
        do{Assert-ExactContainer $name;$health=docker inspect --format '{{.State.Health.Status}}' $name;if($health -eq 'healthy'){break};if([DateTime]::UtcNow -gt $deadline -or $health -eq 'unhealthy'){throw 'Backup database health deadline'};Start-Sleep -Seconds 1}while($true)
    }
    $toolVersion=docker exec $source mysqldump --version
    if($LASTEXITCODE -ne 0){throw 'Official mysqldump unavailable'}
    docker run --detach --name $runner --label "order-api-showcase.backup-run=$id" --read-only --user 65534:65534 --network $network `
        --cap-drop ALL --security-opt no-new-privileges --pids-limit 256 --cpus 2 --memory 3g --tmpfs '/tmp:rw,exec,nosuid,size=2g' `
        --mount "type=bind,source=$snapshot,target=/src,readonly" --mount "type=bind,source=$control,target=/control" `
        -w /src/tests/release -e GOTOOLCHAIN=local -e CGO_ENABLED=1 -e GOCACHE=/tmp/build -e GOPATH=/tmp/gopath `
        -e GOMAXPROCS=2 -e GOMEMLIMIT=256MiB -e BACKUP_TEST_ENABLED=1 -e BACKUP_CONTROL_DIR=/control -e "BACKUP_DATABASE=$database" `
        $GoImage go test -tags=backup -timeout 5m -count=1 -v .|Out-Null
    if($LASTEXITCODE -ne 0){throw 'Backup test runner failed to start'};$created.Add($runner)
    $deadline=[DateTime]::UtcNow.AddMinutes(6)
    while((docker inspect --format '{{.State.Running}}' $runner) -eq 'true'){
        if([DateTime]::UtcNow -gt $deadline){throw 'Backup exercise hard deadline exceeded'}
        if(-not $transferred -and (Test-Path (Join-Path $control 'backup.request'))){
            if((Get-Content -Raw (Join-Path $control 'backup.request')) -cne $database){throw 'Requested schema mismatch'}
            Assert-ExactContainer $source;Assert-ExactContainer $target
            # A byte-preserving container result file avoids Windows shell encoding.
            # No DDL or concurrent writers are running during this small fixture dump.
            $backupStart=[DateTime]::UtcNow;$timer=[Diagnostics.Stopwatch]::StartNew()
            docker exec -e MYSQL_PWD=backup-root-not-a-secret $source mysqldump -uroot --single-transaction --quick --routines --events --triggers --hex-blob --no-tablespaces --set-gtid-purged=OFF --default-character-set=utf8mb4 --databases $database --result-file=/backup/backup.sql
            $timer.Stop();$backupMS=$timer.Elapsed.TotalMilliseconds
            if($LASTEXITCODE -ne 0 -or -not(Test-Path -LiteralPath $dumpPath)){throw 'Logical dump failed'}
            $bytes=(Get-Item -LiteralPath $dumpPath).Length;if($bytes -lt 1 -or $bytes -gt 1MB){throw 'Unexpected dump size; do not import'}
            $count=docker exec -e MYSQL_PWD=backup-root-not-a-secret $target mysql -uroot --batch --skip-column-names -e "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name='$database'"
            if($LASTEXITCODE -ne 0 -or [int]$count -ne 0){throw 'Restore target must be an empty fresh schema'}
            $restoreStart=[DateTime]::UtcNow;$timer.Restart()
            docker exec -e MYSQL_PWD=backup-root-not-a-secret $target sh -c 'exec mysql -uroot --default-character-set=utf8mb4 < /backup/backup.sql'
            $timer.Stop();$restoreMS=$timer.Elapsed.TotalMilliseconds
            if($LASTEXITCODE -ne 0){throw 'Restore failed; retain evidence, do not retry over existing objects'}
            Set-Content -LiteralPath (Join-Path $control 'backup.done') -Value 'ok' -NoNewline -Encoding ascii
            $transferred=$true
        }
        Start-Sleep -Milliseconds 200
    }
    docker logs $runner 2>&1|Tee-Object -FilePath (Join-Path $state 'tests.log')
    $code=[int](docker inspect --format '{{.State.ExitCode}}' $runner)
    if($code -ne 0 -or -not $transferred){throw "Backup/restore verification failed: exit $code; evidence retained in $state"}
    $report=[ordered]@{source_snapshot_sha256=$sourceSnapshotSHA256;executed_source_sha256=$snapshotHash;run_id=$id;mysql_image=$MySQLImage;go_image=$GoImage;tool_version=$toolVersion;backup_start_utc=$backupStart.ToString('o');restore_start_utc=$restoreStart.ToString('o');backup_ms=$backupMS;restore_ms=$restoreMS;dump_bytes=$bytes;dump_sha256=(Get-FileHash -Algorithm SHA256 -LiteralPath $dumpPath).Hash;validation=(Get-Content -Raw (Join-Path $control 'backup-report.json')|ConvertFrom-Json)}
    New-Item -ItemType Directory -Force tests/release/results|Out-Null
    $report|ConvertTo-Json -Depth 15|Set-Content "tests/release/results/backup-$id.json" -Encoding utf8NoBOM
    Write-Output "Backup/restore passed: $id; sensitive dump retained only in ignored .tmp/backups; stopped volumes preserved."
}finally{
    foreach($name in $created){Assert-ExactContainer $name;docker stop --time 10 $name|Out-Null}
    # Deliberately preserve named volumes and ignored dump. Never prune globally.
}
