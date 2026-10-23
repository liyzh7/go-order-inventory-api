param([ValidateRange(1,600)][int]$Seconds=600,[ValidatePattern('^[a-zA-Z0-9-]{1,30}$')][string]$RunID='s11-hotspot-r2')
$ErrorActionPreference='Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$out=".tmp/stability/$RunID-mysql-extra.jsonl"
if(Test-Path -LiteralPath $out){throw 'Preserve existing observation; choose a new ID'}
$name=docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}} {{.Name}}' order-api-showcase-perf-mysql
if($LASTEXITCODE -ne 0 -or $name -ne 'order-api-showcase-perf /order-api-showcase-perf-mysql'){throw 'Exact dedicated performance container required'}
$query=@'
SELECT JSON_OBJECT(
 'utc',UTC_TIMESTAMP(6),
 'status',(SELECT JSON_OBJECTAGG(VARIABLE_NAME,VARIABLE_VALUE) FROM performance_schema.global_status
 WHERE VARIABLE_NAME IN('Innodb_log_waits','Innodb_os_log_written','Innodb_redo_log_current_lsn','Innodb_redo_log_checkpoint_lsn','Innodb_redo_log_physical_size','Innodb_data_fsyncs','Innodb_data_written','Innodb_buffer_pool_wait_free','Innodb_buffer_pool_pages_dirty','Threads_connected','Threads_running')),
 'commit_count',(SELECT COUNT_STAR FROM performance_schema.events_statements_summary_global_by_event_name WHERE EVENT_NAME='statement/sql/commit'),
 'commit_total_ms',(SELECT SUM_TIMER_WAIT/1000000000 FROM performance_schema.events_statements_summary_global_by_event_name WHERE EVENT_NAME='statement/sql/commit'),
 'innodb_file_waits',(SELECT JSON_ARRAYAGG(JSON_OBJECT('event',EVENT_NAME,'count',COUNT_STAR,'total_ms',SUM_TIMER_WAIT/1000000000)) FROM performance_schema.events_waits_summary_global_by_event_name WHERE EVENT_NAME LIKE 'wait/io/file/innodb/%')
);
'@
$deadline=[DateTime]::UtcNow.AddSeconds($Seconds)
do{
    $timer=[Diagnostics.Stopwatch]::StartNew()
    $json=$query|docker exec -i -e MYSQL_PWD=perf-root-not-a-secret order-api-showcase-perf-mysql mysql -uroot --batch --skip-column-names --raw
    $timer.Stop()
    if($LASTEXITCODE -ne 0){throw 'Read-only observation failed'}
    [pscustomobject]@{host_utc=[DateTime]::UtcNow.ToString('o');observer_wall_ms=$timer.Elapsed.TotalMilliseconds;mysql=($json|ConvertFrom-Json)}|ConvertTo-Json -Depth 8 -Compress|Add-Content -LiteralPath $out
    if(Test-Path ".tmp/stability/$RunID-result.json"){break}
    Start-Sleep -Seconds 15
}while([DateTime]::UtcNow -lt $deadline)
Write-Output 'Bounded read-only MySQL observation completed; missing counters remain absent, not zero.'
