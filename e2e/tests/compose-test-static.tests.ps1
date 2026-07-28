[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$composePath = Join-Path $repoRoot 'deploy\compose.test.yml'
$compose = Get-Content -Raw -LiteralPath $composePath
$baseComposePath = Join-Path $repoRoot 'deploy\compose.yml'
$baseCompose = Get-Content -Raw -LiteralPath $baseComposePath
$smokePath = Join-Path $repoRoot 'deploy\scripts\smoke-server.ps1'
$smoke = Get-Content -Raw -LiteralPath $smokePath

$requirements = [ordered]@{
    'API publication reset' = 'ports:\s*!reset\s*\[\]'
    'fixture service' = '(?m)^  provider-fixtures:'
    'Toxiproxy service' = '(?m)^  toxiproxy:'
    'Toxiproxy HTTP port' = 'published:\s*"18787"'
    'Toxiproxy control port' = 'published:\s*"18474"'
    'fixture Open Library URL' = 'http://provider-fixtures:8080/openlibrary'
    'fixture Google Books URL' = 'http://provider-fixtures:8080/googlebooks'
}
foreach ($entry in $requirements.GetEnumerator()) {
    if ($compose -notmatch $entry.Value) {
        throw "Missing test Compose requirement: $($entry.Key)"
    }
}

if ($compose -match '(?m)^\s+name:\s+bookbox-') {
    throw 'Test Compose must not declare fixed resource names.'
}
if ($compose -match '(?i)tls|certificate|fingerprint|https|8443|18443|--insecure|--no-check-certificate') {
    throw 'Test Compose still contains TLS behavior.'
}
foreach ($entry in ([ordered]@{
    'BookBox production project' = '(?m)^name:\s+bookbox-prod\r?$'
    'named HTTP publication' = '(?m)^\s+- name:\s+http\r?$'
    'HTTP container port' = '(?m)^\s+target:\s+8787\r?$'
    'HTTP host-port default' = '\$\{BOOKBOX_HOST_PORT:-8787\}'
    'required LAN bind address' = '\$\{BOOKBOX_BIND_ADDRESS:\?'
    'HTTP readiness probe' = 'http://localhost:8787/v1/ready'
}).GetEnumerator()) {
    if ($baseCompose -notmatch $entry.Value) {
        throw "Missing production Compose requirement: $($entry.Key)"
    }
}
if ($baseCompose -match '(?i)tls|fingerprint|https|8443|18443|--insecure|--no-check-certificate') {
    throw 'Production Compose still contains transport-encryption behavior.'
}
foreach ($requirement in @(
    'Get-NetIPAddress -AddressFamily IPv4',
    "NetworkCategory -eq 'Private'",
    '"BOOKBOX_BIND_ADDRESS=\$bindAddress"',
    'http://\$\{bindAddress\}:\$port'
)) {
    if ($smoke -notmatch $requirement) {
        throw "Smoke server lacks private-LAN binding behavior: $requirement"
    }
}
if ($smoke -match 'BOOKBOX_BIND_ADDRESS=127\.0\.0\.1|default.*loopback') {
    throw 'Smoke server still assumes the removed loopback deployment default.'
}

Write-Output 'PASS: test Compose statically isolates fixtures and proxy publication.'
