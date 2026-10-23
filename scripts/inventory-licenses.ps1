# Copy upstream notices byte-for-byte; this does not choose a project LICENSE.
$ErrorActionPreference='Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$cache=(go env GOMODCACHE).Trim()
if($LASTEXITCODE -ne 0){throw 'Go module cache unavailable'}
$items=[Collections.Generic.List[object]]::new()
foreach($line in Get-Content go.mod){
    if($line -notmatch '^\s*([a-zA-Z0-9][^\s]+)\s+(v[^\s]+)'){continue}
    $module=$Matches[1];$version=$Matches[2]
    $escaped=[regex]::Replace($module,'[A-Z]',{param($m) '!'+$m.Value.ToLowerInvariant()})
    $directory=Join-Path $cache "$escaped@$version"
    if(-not(Test-Path -LiteralPath $directory)){throw "Module not cached: $module $version; fetch deliberately before inventorying"}
    $files=@(Get-ChildItem -LiteralPath $directory -File|Where-Object {$_.Name -match '^(LICENSE|COPYING|NOTICE|PATENTS)(\..*)?$'})
    if($files.Count -eq 0){throw "No root license found for $module"}
    $dest=Join-Path 'third_party/licenses' "$module@$version"
    New-Item -ItemType Directory -Force $dest|Out-Null
    $records=foreach($file in $files){
        $target=Join-Path $dest $file.Name
        if(Test-Path -LiteralPath $target){
            # Module-cache notices may retain the read-only attribute. Reuse
            # identical copies, and refuse to overwrite a changed declaration.
            if((Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash -ne (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash){throw "Existing upstream notice differs: $module $($file.Name)"}
        }else{Copy-Item -LiteralPath $file.FullName -Destination $dest}
        [pscustomobject]@{file=$file.Name;sha256=(Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()}
    }
    $items.Add([pscustomobject]@{module=$module;version=$version;notices=@($records)})
}
$items.ToArray()|ConvertTo-Json -Depth 5|Set-Content third_party/modules.json -Encoding utf8NoBOM
Write-Output "Inventoried $($items.Count) declared modules and preserved upstream root notices; project LICENSE is managed separately."
