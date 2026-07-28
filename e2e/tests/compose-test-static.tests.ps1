[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$composePath = Join-Path $repoRoot 'deploy\compose.test.yml'
$compose = Get-Content -Raw -LiteralPath $composePath

$requirements = [ordered]@{
    'API publication reset' = 'ports:\s*!reset\s*\[\]'
    'fixture service' = '(?m)^  provider-fixtures:'
    'Toxiproxy service' = '(?m)^  toxiproxy:'
    'Toxiproxy HTTPS port' = 'published:\s*"18443"'
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

Write-Output 'PASS: test Compose statically isolates fixtures and proxy publication.'
