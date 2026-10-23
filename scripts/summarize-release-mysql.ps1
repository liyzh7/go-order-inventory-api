param([ValidatePattern('^[a-zA-Z0-9-]{1,30}$')][string]$RunID='s11-hotspot-r2')
$ErrorActionPreference='Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$r=Get-Content -Raw ".tmp/stability/$RunID-result.json"|ConvertFrom-Json
$start=([DateTime]$r.start_utc).ToUniversalTime()
$samples=@(Get-Content ".tmp/stability/$RunID-mysql-extra.jsonl"|ForEach-Object {$_|ConvertFrom-Json}|Where-Object {(([DateTime]$_.host_utc).ToUniversalTime()-$start).TotalSeconds -le 600})
if($samples.Count -lt 2){throw 'Insufficient auxiliary counter samples'}
$out='tests/release/results'
$rawSamples=foreach($s in $samples){
    $status=$s.mysql.status
    [pscustomobject]@{run_id=$RunID;utc=([DateTime]$s.host_utc).ToUniversalTime().ToString('o');elapsed_s=(([DateTime]$s.host_utc).ToUniversalTime()-$start).TotalSeconds;observer_wall_ms=$s.observer_wall_ms;commit_count=$s.mysql.commit_count;commit_total_ms=$s.mysql.commit_total_ms;redo_current_lsn=$status.Innodb_redo_log_current_lsn;redo_checkpoint_lsn=$status.Innodb_redo_log_checkpoint_lsn;redo_physical_size=$status.Innodb_redo_log_physical_size;log_waits=$status.Innodb_log_waits;buffer_wait_free=$status.Innodb_buffer_pool_wait_free;dirty_pages=$status.Innodb_buffer_pool_pages_dirty;fsyncs=$status.Innodb_data_fsyncs;data_written=$status.Innodb_data_written;log_written=$status.Innodb_os_log_written}
}
$intervals=for($i=1;$i -lt $rawSamples.Count;$i++){
    $a=$rawSamples[$i-1];$b=$rawSamples[$i];$count=[long]$b.commit_count-[long]$a.commit_count
    if($count -le 0){throw 'Commit instrument missing or counter reset; cannot derive mean'}
    [pscustomobject]@{run_id=$RunID;from_s=$a.elapsed_s;to_s=$b.elapsed_s;commit_count=$count;commit_mean_ms=([double]$b.commit_total_ms-[double]$a.commit_total_ms)/$count;redo_age_bytes=[long]$b.redo_current_lsn-[long]$b.redo_checkpoint_lsn;log_waits_delta=[long]$b.log_waits-[long]$a.log_waits;buffer_wait_free_delta=[long]$b.buffer_wait_free-[long]$a.buffer_wait_free;fsyncs_delta=[long]$b.fsyncs-[long]$a.fsyncs;data_written_delta=[long]$b.data_written-[long]$a.data_written;log_written_delta=[long]$b.log_written-[long]$a.log_written}
}
$rawSamples|Export-Csv "$out/tail-mysql-samples.csv" -NoTypeInformation -Encoding utf8NoBOM
$intervals|Export-Csv "$out/tail-mysql-intervals.csv" -NoTypeInformation -Encoding utf8NoBOM
Write-Output 'Saved in-run counter deltas only; no percentile is derived from cumulative totals.'
