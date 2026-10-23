param([string]$BaseUri = 'http://127.0.0.1:28080')

$ErrorActionPreference = 'Stop'
$uri = [uri]$BaseUri
if (-not $uri.IsLoopback -or $uri.Scheme -ne 'http') { throw 'Only local HTTP development URLs are supported' }
$clients = @()
$created = @()
$key = 'stage6-' + [guid]::NewGuid().ToString('N')

function Assert-Denied([string]$Method, [string]$Url, [hashtable]$Headers, [int]$Expected) {
    try {
        Invoke-WebRequest -Method $Method -Uri $Url -Headers $Headers -UseBasicParsing | Out-Null
        throw 'Unexpected success for a request expected to fail'
    }
    catch {
        if (-not $_.Exception.Response -or [int]$_.Exception.Response.StatusCode -ne $Expected) { throw }
        Write-Output "$Method $($Url.Replace($BaseUri, '')) -> $Expected"
    }
}

try {
    foreach ($label in @('a', 'b')) {
        $random = New-Object byte[] 32
        $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
        try { $generator.GetBytes($random) } finally { $generator.Dispose() }
        $username = 'smoke_' + $label + '_' + [guid]::NewGuid().ToString('N').Substring(0, 12)
        $password = [Convert]::ToBase64String($random)
        $body = @{ username = $username; password = $password } | ConvertTo-Json -Compress
        $registered = Invoke-RestMethod -Method Post -Uri "$BaseUri/auth/register" -ContentType 'application/json' -Body $body
        $login = Invoke-RestMethod -Method Post -Uri "$BaseUri/auth/login" -ContentType 'application/json' -Body $body
        $headers = @{ Authorization = "Bearer $($login.data.access_token)"; 'Idempotency-Key' = $key }
        $clients += @{ headers = $headers; id = $registered.data.id }
        # Never print the login body, raw token, password, or Authorization.
        Write-Output "Registered/logged in user $($registered.data.id); token_type=$($login.data.token_type); expires_at=$($login.data.expires_at)"
    }
    $products = (Invoke-RestMethod -Uri "$BaseUri/products?limit=1").data.items
    if ($products.Count -lt 1 -or $products[0].available_quantity -lt 2) { throw 'Need one local product with at least two available units' }
    $productID = $products[0].id
    $before = $products[0].available_quantity
    $orderBody = @{ items = @(@{ product_id = $productID; quantity = 1 }) } | ConvertTo-Json -Depth 4 -Compress
    foreach ($client in $clients) {
        $response = Invoke-WebRequest -Method Post -Uri "$BaseUri/orders" -Headers $client.headers -ContentType 'application/json' -Body $orderBody -UseBasicParsing
        if ($response.StatusCode -ne 201) { throw 'Expected a fresh order for each user using the same key' }
        $order = ($response.Content | ConvertFrom-Json).data
        $created += @{ id = $order.id; headers = $client.headers }
        Write-Output "POST /orders -> 201 user=$($client.id) order=$($order.id) total_cents=$($order.total_amount_cents)"
    }
    if ($created[0].id -eq $created[1].id) { throw 'Cross-user idempotency leak' }
    $replayed = Invoke-WebRequest -Method Post -Uri "$BaseUri/orders" -Headers $clients[0].headers -ContentType 'application/json' -Body $orderBody -UseBasicParsing
    if ($replayed.StatusCode -ne 200 -or $replayed.Headers['Idempotency-Replayed'] -ne 'true') { throw 'Replay failed' }
    Write-Output 'Same-user replay -> 200, Idempotency-Replayed=true'
    $own = Invoke-RestMethod -Uri "$BaseUri/orders/$($created[0].id)" -Headers $clients[0].headers
    Write-Output ('GET own order -> ' + ($own | ConvertTo-Json -Depth 8 -Compress))
    Assert-Denied Get "$BaseUri/orders/$($created[0].id)" $clients[1].headers 404
    Assert-Denied Post "$BaseUri/orders/$($created[0].id)/cancel" $clients[1].headers 404
    Assert-Denied Get "$BaseUri/orders/$($created[0].id)" @{} 401
    foreach ($entry in $created) {
        $cancelled = Invoke-RestMethod -Method Post -Uri "$BaseUri/orders/$($entry.id)/cancel" -Headers $entry.headers
        if ($cancelled.data.status -ne 'CANCELLED') { throw 'Cancellation failed' }
        Write-Output "POST /orders/$($entry.id)/cancel -> 200 CANCELLED"
    }
    $after = (Invoke-RestMethod -Uri "$BaseUri/products/$productID").data.available_quantity
    if ($before -ne $after) { throw 'Inventory did not return to its original value' }
    Write-Output "Stock restored: $before -> $after"
    foreach ($client in $clients) {
        Invoke-RestMethod -Method Post -Uri "$BaseUri/auth/logout" -Headers $client.headers | Out-Null
        Assert-Denied Get "$BaseUri/auth/me" $client.headers 401
    }
}
finally {
    # Only revoke this script's sessions. No user/order rows or inventories are
    # deleted or reset. Failed runs leave auditable simulated data for diagnosis.
    foreach ($client in $clients) {
        try { Invoke-RestMethod -Method Post -Uri "$BaseUri/auth/logout" -Headers $client.headers | Out-Null } catch { }
    }
    Remove-Variable password, body, login, headers, clients -ErrorAction SilentlyContinue
}
