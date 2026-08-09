$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$compose = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\compose.yml'))
$dockerfile = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\Dockerfile.worker'))
$serverSmoke = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'smoke-server.ps1'))

foreach ($required in @($compose, $dockerfile, $serverSmoke)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required catalog smoke input is missing: $required"
    }
}

$composeText = Get-Content -LiteralPath $compose -Raw
$workerMatch = [regex]::Match(
    $composeText,
    '(?ms)^  bookbox-catalog-worker:\r?\n(?<body>.*?)(?=^secrets:)'
)
if (-not $workerMatch.Success) {
    throw 'The catalog worker Compose service is missing.'
}
$worker = $workerMatch.Groups['body'].Value
foreach ($requiredText in @(
    'dockerfile: deploy/Dockerfile.worker',
    'user: "10001:10001"',
    'read_only: true',
    'source: cover_cache',
    'source: cover_backups',
    'no-new-privileges:true'
)) {
    if (-not $worker.Contains($requiredText)) {
        throw "The worker service is missing required isolation: $requiredText"
    }
}
if ($worker -match '(?m)^    (ports|expose):') {
    throw 'The worker must not publish or expose a port.'
}
if ($worker -notmatch '(?ms)cap_drop:\s*\r?\n\s+- ALL') {
    throw 'The worker must drop all capabilities.'
}

$dockerfileText = Get-Content -LiteralPath $dockerfile -Raw
if ($dockerfileText -notmatch '(?m)^RUN apk add --no-cache ca-certificates ffmpeg') {
    throw 'The worker image does not install ffprobe through FFmpeg.'
}
if ($dockerfileText -notmatch '(?m)^USER 10001:10001\r?$') {
    throw 'The worker image does not use the dedicated non-root user.'
}
if ($dockerfileText -match '(?m)^EXPOSE\s') {
    throw 'The worker image must not expose a port.'
}

# smoke-server starts the complete Compose project, including PostgreSQL, API,
# and the catalog worker. Its task-scoped library snapshot proves that startup
# indexing does not alter audio/source files. The server smoke also exercises
# the audiobook and raw indexed-search response shapes, authentication, and
# source-path confidentiality. Its finally block removes the exact Compose
# project, volumes, image, and temporary directory.
$smokeOutput = @(& $serverSmoke)
if (-not $?) {
    throw 'The catalog startup smoke failed.'
}
$catalogStatus = @($smokeOutput | Where-Object {
    $_.PSObject.Properties.Name -contains 'LastInventoryStatus'
}) | Select-Object -Last 1
if ($null -eq $catalogStatus) {
    throw 'The catalog startup smoke returned no sanitized status summary.'
}
Write-Host (
    ('Catalog status: activeFiles={0} activeBooks={1} latestGeneration={2} ' +
    'projectionGeneration={3} queuedJobs={4} deadJobs={5} lastInventory={6}') -f
    $catalogStatus.ActiveFiles,
    $catalogStatus.ActiveBooks,
    $catalogStatus.LatestGeneration,
    $catalogStatus.ProjectionGeneration,
    $catalogStatus.QueuedJobs,
    $catalogStatus.DeadJobs,
    $catalogStatus.LastInventoryStatus
)

Write-Host 'PASS: catalog worker isolation and indexed-search smoke checks passed; startup scan preserved the source tree.'
