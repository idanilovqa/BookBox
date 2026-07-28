$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Test-SyntheticRecoveryResolvedScope {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        $Resolved,

        [Parameter(Mandatory)]
        [string] $RunRoot,

        [Parameter(Mandatory)]
        [string] $ProjectName
    )

    function Assert-PathEqual {
        param(
            [Parameter(Mandatory)][string] $Actual,
            [Parameter(Mandatory)][string] $Expected,
            [Parameter(Mandatory)][string] $Label
        )
        $actualPath = [IO.Path]::GetFullPath($Actual).TrimEnd('\')
        $expectedPath = [IO.Path]::GetFullPath($Expected).TrimEnd('\')
        if (-not [string]::Equals(
            $actualPath,
            $expectedPath,
            [StringComparison]::OrdinalIgnoreCase
        )) {
            throw "Synthetic recovery $Label is outside the exact task root."
        }
    }

    function Get-Service {
        param([Parameter(Mandatory)][string] $Name)
        $property = $Resolved.services.PSObject.Properties[$Name]
        if (-not $property) {
            throw "Synthetic recovery service is missing: $Name"
        }
        $property.Value
    }

    function Assert-Mount {
        param(
            [Parameter(Mandatory)][string] $ServiceName,
            [Parameter(Mandatory)][string] $Target,
            [Parameter(Mandatory)][string] $Type,
            [Parameter(Mandatory)][string] $Source,
            [Nullable[bool]] $ReadOnly,
            [Parameter(Mandatory)][string] $Label
        )
        $service = Get-Service $ServiceName
        $mounts = @($service.volumes | Where-Object {
            [string] $_.target -ceq $Target
        })
        if ($mounts.Count -ne 1) {
            throw "Synthetic recovery $Label must have exactly one mount."
        }
        $mount = $mounts[0]
        if ([string] $mount.type -cne $Type) {
            throw "Synthetic recovery $Label has the wrong mount type."
        }
        if ($Type -eq 'bind') {
            Assert-PathEqual ([string] $mount.source) $Source $Label
        } elseif ([string] $mount.source -cne $Source) {
            throw "Synthetic recovery $Label has the wrong recovery volume."
        }
        if ($null -ne $ReadOnly) {
            $actualReadOnly = $false
            $property = $mount.PSObject.Properties['read_only']
            if ($property) {
                $actualReadOnly = [bool] $property.Value
            }
            if ($actualReadOnly -ne [bool] $ReadOnly) {
                throw "Synthetic recovery $Label has the wrong read-only setting."
            }
        }
    }

    $root = [IO.Path]::GetFullPath($RunRoot).TrimEnd('\')
    $parent = [IO.Path]::GetFullPath(
        (Join-Path ([IO.Path]::GetTempPath()) 'bookbox-recovery')
    ).TrimEnd('\')
    $runID = [IO.Path]::GetFileName($root)
    if (
        $runID -notmatch '^[0-9a-f]{32}$' -or
        -not [string]::Equals(
            [IO.Path]::GetDirectoryName($root),
            $parent,
            [StringComparison]::OrdinalIgnoreCase
        ) -or
        $ProjectName -cne "bookbox-recovery-$runID" -or
        [string] $Resolved.name -cne $ProjectName
    ) {
        throw 'Synthetic recovery resolved project is outside the exact task scope.'
    }

    $library = Join-Path $root 'library'
    $config = Join-Path $root 'config.yml'
    Assert-Mount 'bookbox-api' '/library/audiobooks' 'bind' $library $true `
        'API library bind'
    Assert-Mount 'bookbox-api' '/etc/bookbox/config.yml' 'bind' $config $true `
        'API config bind'
    Assert-Mount 'bookbox-catalog-worker' '/library/audiobooks' 'bind' $library $false `
        'worker library bind'
    Assert-Mount 'bookbox-catalog-worker' '/etc/bookbox/config.yml' 'bind' $config $true `
        'worker config bind'

    $volumeMounts = @(
        @('postgres', '/var/lib/postgresql', 'recovery_postgres_data', 'PostgreSQL data'),
        @('bookbox-api', '/var/lib/bookbox', 'recovery_tls_data', 'TLS data'),
        @(
            'bookbox-catalog-worker',
            '/var/lib/bookbox/covers',
            'recovery_cover_cache',
            'cover cache'
        ),
        @(
            'bookbox-catalog-worker',
            '/var/lib/bookbox/backups',
            'recovery_cover_backups',
            'cover backups'
        )
    )
    foreach ($definition in $volumeMounts) {
        Assert-Mount $definition[0] $definition[1] 'volume' $definition[2] $null `
            $definition[3]
        $volume = $Resolved.volumes.PSObject.Properties[$definition[2]]
        $expectedName = "${ProjectName}_$($definition[2])"
        if (-not $volume -or [string] $volume.Value.name -cne $expectedName) {
            throw "Synthetic recovery $($definition[3]) volume name is not task-scoped."
        }
    }

    $api = Get-Service 'bookbox-api'
    $ports = @($api.ports)
    if (
        $ports.Count -ne 1 -or
        [string] $ports[0].target -cne '8443' -or
        [string] $ports[0].published -cne '18443' -or
        [string] $ports[0].host_ip -cne '127.0.0.1' -or
        [string] $ports[0].protocol -cne 'tcp'
    ) {
        throw 'Synthetic recovery must expose only 127.0.0.1:18443 to API port 8443.'
    }

    $secretRoot = Join-Path $root 'secrets'
    $expectedSecrets = [ordered]@{
        postgres_password = 'postgres_password.txt'
        app_password_hash = 'app_password_hash.txt'
        session_key = 'session_key.bin'
        google_books_key = 'google_books_key.txt'
    }
    foreach ($entry in $expectedSecrets.GetEnumerator()) {
        $secret = $Resolved.secrets.PSObject.Properties[$entry.Key]
        if (-not $secret) {
            throw "Synthetic recovery secret is missing: $($entry.Key)"
        }
        Assert-PathEqual `
            ([string] $secret.Value.file) `
            (Join-Path $secretRoot $entry.Value) `
            "$($entry.Key) secret"
    }
}
