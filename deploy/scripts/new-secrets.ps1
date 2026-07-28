param(
    [string] $SecretsDirectory = (Join-Path $PSScriptRoot '..\secrets')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$secrets = [IO.Path]::GetFullPath($SecretsDirectory)
[IO.Directory]::CreateDirectory($secrets) | Out-Null
$paths = @{
    postgres = Join-Path $secrets 'postgres_password.txt'
    password = Join-Path $secrets 'app_password_hash.txt'
    session  = Join-Path $secrets 'session_key.bin'
    google   = Join-Path $secrets 'google_books_key.txt'
}
foreach ($path in @($paths.postgres, $paths.password, $paths.session)) {
    if ((Test-Path -LiteralPath $path -PathType Leaf) -and
        (Get-Item -LiteralPath $path).Length -gt 0) {
        throw "Refusing to overwrite existing non-empty secret: $([IO.Path]::GetFileName($path))"
    }
}

function New-RandomBytes {
    param([Parameter(Mandatory)][int] $Count)
    $bytes = [byte[]]::new($Count)
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $generator.GetBytes($bytes)
    } finally {
        $generator.Dispose()
    }
    return $bytes
}

function ConvertFrom-SecureValue {
    param([Parameter(Mandatory)][Security.SecureString] $Value)
    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Value)
    try {
        return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
    } finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer)
    }
}

$first = Read-Host 'Application password' -AsSecureString
$second = Read-Host 'Confirm application password' -AsSecureString
$firstPlain = ConvertFrom-SecureValue $first
$secondPlain = ConvertFrom-SecureValue $second
$inputBytes = $null
$inputText = $null
try {
    if ([string]::IsNullOrEmpty($firstPlain) -or $firstPlain -cne $secondPlain) {
        throw 'Application passwords are empty or do not match.'
    }
    $repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
    $server = Join-Path $repo 'server'
    $inputBytes = [Text.Encoding]::UTF8.GetBytes(
        $firstPlain + [Environment]::NewLine +
        $secondPlain + [Environment]::NewLine
    )
    $inputText = [Text.Encoding]::UTF8.GetString($inputBytes)
    $hashOutput = @(
        $inputText | & docker run --rm -i `
            -v "$($server.Replace('\', '/')):/src" `
            -w /src golang:1.26.5 `
            go run ./cmd/bookbox-admin hash-password --stdin
    )
    if ($LASTEXITCODE -ne 0) {
        throw 'bookbox-admin password hashing failed.'
    }
    $hash = @($hashOutput | Where-Object {
        $_ -match '^\$argon2id\$v=19\$m=65536,t=3,p=2\$'
    })
    if ($hash.Count -ne 1) {
        throw 'bookbox-admin returned an unexpected password hash.'
    }
} finally {
    if ($null -ne $inputBytes) {
        [Array]::Clear($inputBytes, 0, $inputBytes.Length)
    }
    $firstPlain = $null
    $secondPlain = $null
    $inputText = $null
}

$created = [Collections.Generic.List[string]]::new()
[IO.File]::WriteAllText(
    $paths.postgres,
    [Convert]::ToBase64String((New-RandomBytes 32)),
    [Text.UTF8Encoding]::new($false)
)
$created.Add($paths.postgres)
[IO.File]::WriteAllText(
    $paths.password,
    $hash[0],
    [Text.UTF8Encoding]::new($false)
)
$created.Add($paths.password)
[IO.File]::WriteAllBytes($paths.session, (New-RandomBytes 32))
$created.Add($paths.session)
if (-not (Test-Path -LiteralPath $paths.google)) {
    [IO.File]::WriteAllBytes($paths.google, [byte[]]::new(0))
    $created.Add($paths.google)
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
foreach ($path in $paths.Values) {
    & icacls $path /inheritance:r /grant:r "${identity}:(F)" 'SYSTEM:(F)' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Could not restrict ACL for $([IO.Path]::GetFileName($path))."
    }
}

foreach ($path in $created) {
    Write-Host "Created $([IO.Path]::GetFileName($path))"
}
Write-Host 'Next: docker compose --env-file deploy/.env -f deploy/compose.yml up -d --build'
