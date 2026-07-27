[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $ProjectName,
    [Parameter(Mandatory)]
    [string[]] $ComposeFile,
    [Parameter(Mandatory)]
    [string] $EnvironmentFile,
    [Parameter(Mandatory)]
    [string] $CurrentPasswordFile,
    [Parameter(Mandatory)]
    [string] $NewPasswordFile,
    [string] $BaseUrl = 'https://127.0.0.1:18443',
    [string] $Username = 'owner',
    [ValidateRange(30, 600)]
    [int] $TimeoutSeconds = 180
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Invoke-Docker {
    param([string[]] $Arguments, [string] $InputText)
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        if ($PSBoundParameters.ContainsKey('InputText')) {
            $output = @($InputText | & docker @Arguments 2>&1)
        } else {
            $output = @(& docker @Arguments 2>&1)
        }
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "Docker command failed:`n$($output -join [Environment]::NewLine)"
    }
    return $output
}

function Read-OneLine {
    param([string] $Path)
    $bytes = [IO.File]::ReadAllBytes([IO.Path]::GetFullPath($Path))
    try {
        $value = [Text.Encoding]::UTF8.GetString($bytes).TrimEnd("`r", "`n")
    } finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
    }
    if (-not $value -or $value.Contains("`r") -or $value.Contains("`n")) {
        throw 'Password input must contain exactly one non-empty line.'
    }
    return $value
}

function Invoke-SessionRequest {
    param([string] $Path, [hashtable] $Body)
    $request = Join-Path $script:recoveryRoot "request-$([Guid]::NewGuid().ToString('N')).json"
    $response = Join-Path $script:recoveryRoot "response-$([Guid]::NewGuid().ToString('N')).json"
    try {
        [IO.File]::WriteAllText(
            $request,
            ($Body | ConvertTo-Json -Compress),
            [Text.UTF8Encoding]::new($false)
        )
        $code = @(
            & curl.exe --silent --show-error --insecure --output $response `
                --write-out '%{http_code}' --header 'Content-Type: application/json' `
                --data-binary "@$request" ($BaseUrl.TrimEnd('/') + $Path)
        ) -join ''
        return [pscustomobject]@{
            Code = [int] $code
            Body = if (Test-Path -LiteralPath $response) {
                Get-Content -LiteralPath $response -Raw
            } else {
                ''
            }
        }
    } finally {
        Remove-Item -LiteralPath $request, $response -Force -ErrorAction SilentlyContinue
    }
}

if ($ProjectName -notlike 'netstorage-recovery-*') {
    throw 'This build permits rotation only for a netstorage-recovery-* synthetic project.'
}
$script:recoveryRoot = [IO.Path]::GetFullPath(
    (Join-Path ([IO.Path]::GetTempPath()) 'netstorage-recovery')
).TrimEnd('\') + '\'
[IO.Directory]::CreateDirectory($script:recoveryRoot) | Out-Null
$environment = [IO.Path]::GetFullPath($EnvironmentFile)
$composeFiles = @($ComposeFile | ForEach-Object { [IO.Path]::GetFullPath($_) })
foreach ($path in @($composeFiles) + @($environment)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required recovery deployment file not found: $path"
    }
}
$composeArgs = @('compose', '--env-file', $environment, '-p', $ProjectName)
foreach ($path in $composeFiles) {
    $composeArgs += @('-f', $path)
}
$resolved = ((Invoke-Docker (@($composeArgs) + @(
    'config', '--format', 'json'
))) -join "`n") | ConvertFrom-Json
if ([string] $resolved.name -cne $ProjectName) {
    throw 'Resolved recovery project identity does not match.'
}
$hashPath = [IO.Path]::GetFullPath([string] $resolved.secrets.app_password_hash.file)
$sessionPath = [IO.Path]::GetFullPath([string] $resolved.secrets.session_key.file)
foreach ($path in @($hashPath, $sessionPath)) {
    if (-not $path.StartsWith(
        $script:recoveryRoot,
        [StringComparison]::OrdinalIgnoreCase
    )) {
        throw "Synthetic secret is outside the recovery root: $path"
    }
}

$currentPlain = $null
$newPlain = $null
$hashInput = $null
$oldHash = [IO.File]::ReadAllBytes($hashPath)
$oldSession = [IO.File]::ReadAllBytes($sessionPath)
$rotated = $false
try {
    $currentPlain = Read-OneLine $CurrentPasswordFile
    $newPlain = Read-OneLine $NewPasswordFile
    $login = Invoke-SessionRequest '/v1/session' @{
        username = $Username
        password = $currentPlain
    }
    if ($login.Code -ne 200) {
        throw 'Current application credentials were rejected.'
    }
    $oldRefresh = [string] (($login.Body | ConvertFrom-Json).refreshToken)
    if (-not $oldRefresh) {
        throw 'Login response did not include a refresh token.'
    }

    $hashInput = $newPlain + [Environment]::NewLine +
        $newPlain + [Environment]::NewLine
    $hashOutput = @(
        Invoke-Docker (@($composeArgs) + @(
            'run', '--rm', '--no-deps', '--entrypoint',
            '/usr/local/bin/netstorage-admin', 'netstorage-api',
            'hash-password', '--stdin'
        )) $hashInput
    )
    $hash = @($hashOutput | Where-Object {
        $_ -match '^\$argon2id\$v=19\$m=65536,t=3,p=2\$'
    })
    if ($hash.Count -ne 1) {
        throw 'Password hashing returned an unexpected result.'
    }

    $session = [byte[]]::new(32)
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $generator.GetBytes($session)
    } finally {
        $generator.Dispose()
    }
    $temporaryHash = "$hashPath.new-$([Guid]::NewGuid().ToString('N'))"
    $temporarySession = "$sessionPath.new-$([Guid]::NewGuid().ToString('N'))"
    try {
        [IO.File]::WriteAllText(
            $temporaryHash,
            $hash[0],
            [Text.UTF8Encoding]::new($false)
        )
        [IO.File]::WriteAllBytes($temporarySession, $session)
        Move-Item -LiteralPath $temporaryHash -Destination $hashPath -Force
        Move-Item -LiteralPath $temporarySession -Destination $sessionPath -Force
        $rotated = $true
    } finally {
        [Array]::Clear($session, 0, $session.Length)
        Remove-Item -LiteralPath $temporaryHash, $temporarySession `
            -Force `
            -ErrorAction SilentlyContinue
    }

    Invoke-Docker (@($composeArgs) + @(
        'up', '-d', '--no-deps', '--force-recreate', 'netstorage-api'
    )) | Out-Null
    & (Join-Path $PSScriptRoot 'wait-healthy.ps1') `
        -BaseUrl $BaseUrl `
        -TimeoutSeconds $TimeoutSeconds
    if ($LASTEXITCODE -ne 0) {
        throw 'API did not become ready after rotation.'
    }
    $oldResult = Invoke-SessionRequest '/v1/session/refresh' @{
        refreshToken = $oldRefresh
    }
    if ($oldResult.Code -ne 401) {
        throw "Old refresh token returned HTTP $($oldResult.Code), not 401."
    }
    $newLogin = Invoke-SessionRequest '/v1/session' @{
        username = $Username
        password = $newPlain
    }
    if ($newLogin.Code -ne 200) {
        throw 'New application credentials were rejected.'
    }
} catch {
    if ($rotated) {
        [IO.File]::WriteAllBytes($hashPath, $oldHash)
        [IO.File]::WriteAllBytes($sessionPath, $oldSession)
        Invoke-Docker (@($composeArgs) + @(
            'up', '-d', '--no-deps', '--force-recreate', 'netstorage-api'
        )) | Out-Null
    }
    throw
} finally {
    [Array]::Clear($oldHash, 0, $oldHash.Length)
    [Array]::Clear($oldSession, 0, $oldSession.Length)
    $currentPlain = $null
    $newPlain = $null
    $hashInput = $null
}
Write-Host 'PASS: synthetic password/session rotation invalidated the old refresh token.'
