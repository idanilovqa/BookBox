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
    '(?ms)^  netstorage-catalog-worker:\r?\n(?<body>.*?)(?=^secrets:)'
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
if ($dockerfileText -notmatch '(?m)^USER 10001:10001$') {
    throw 'The worker image does not use the dedicated non-root user.'
}
if ($dockerfileText -match '(?m)^EXPOSE\s') {
    throw 'The worker image must not expose a port.'
}

# smoke-server starts the complete Compose project, including PostgreSQL, API,
# and the catalog worker. Its task-scoped library snapshot proves that startup
# indexing does not alter audio/source files, and its finally block removes the
# exact Compose project, volumes, image, and temporary directory.
& $serverSmoke
if (-not $?) {
    throw 'The catalog startup smoke failed.'
}

Write-Host 'PASS: catalog worker image and Compose isolation passed; startup scan preserved the source tree.'
