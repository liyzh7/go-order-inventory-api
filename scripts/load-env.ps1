param(
    [string]$Path = (Join-Path $PSScriptRoot '..\.env')
)

$resolvedPath = Resolve-Path -LiteralPath $Path -ErrorAction Stop
foreach ($line in Get-Content -LiteralPath $resolvedPath) {
    $trimmed = $line.Trim()
    if ($trimmed.Length -eq 0 -or $trimmed.StartsWith('#')) {
        continue
    }

    $parts = $trimmed.Split('=', 2)
    if ($parts.Count -ne 2 -or $parts[0].Trim() -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') {
        throw "Invalid environment entry in $resolvedPath"
    }
    if ($parts[1] -match '[\s#''"$`]') {
        throw "Unsupported quoted, interpolated, commented, or whitespace-containing value for $($parts[0].Trim()) in $resolvedPath"
    }
    Set-Item -LiteralPath "Env:$($parts[0].Trim())" -Value $parts[1]
}
