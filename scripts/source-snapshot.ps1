# Content identity for the source snapshot; never consult a parent Git repository.
# SHA-256 over ordinally sorted UTF-8 "relative/path lowercase-file-sha256\n" records.
# Includes README, docs, code, configuration and third-party notices. Excludes
# .git, .tmp, caches, local .env, and generated tests/*/results experiment output.
function Get-SourceSnapshotSHA256 {
    param([Parameter(Mandatory=$true)][string]$Root)
    $base = (Get-Item -LiteralPath $Root -Force -ErrorAction Stop).FullName.TrimEnd('\','/')
    $pending = [Collections.Generic.Stack[string]]::new()
    $pending.Push($base)
    $files = [Collections.Generic.Dictionary[string,string]]::new([StringComparer]::Ordinal)
    while ($pending.Count -gt 0) {
        $directory = $pending.Pop()
        foreach ($entry in Get-ChildItem -LiteralPath $directory -Force -ErrorAction Stop) {
            $relative = $entry.FullName.Substring($base.Length + 1).Replace('\','/')
            if ($relative -match '(^|/)(\.git|\.tmp|\.cache|node_modules|__pycache__)(/|$)' -or
                $relative -match '^tests/[^/]+/results(/|$)' -or
                ($entry.Name -like '.env*' -and $entry.Name -ne '.env.example')) { continue }
            if (($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw 'Source snapshot refuses symbolic links or junctions'
            }
            if ($entry.PSIsContainer) { $pending.Push($entry.FullName); continue }
            $files.Add($relative, (Get-FileHash -LiteralPath $entry.FullName -Algorithm SHA256).Hash.ToLowerInvariant())
        }
    }
    if ($files.Count -eq 0) { throw 'Source snapshot contains no files' }
    [string[]]$paths = @($files.Keys)
    [Array]::Sort($paths, [StringComparer]::Ordinal)
    $records = foreach ($path in $paths) { $path + ' ' + $files[$path] + "`n" }
    $hasher = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($hasher.ComputeHash([Text.Encoding]::UTF8.GetBytes(($records -join ''))))).Replace('-','').ToLowerInvariant()
    } finally { $hasher.Dispose() }
}
