[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$helper = Join-Path $PSScriptRoot 'synthetic-recovery-scope.ps1'
if (-not (Test-Path -LiteralPath $helper -PathType Leaf)) {
    throw 'Synthetic recovery scope validation helper is missing.'
}
. $helper

$runID = '0123456789abcdef0123456789abcdef'
$project = "bookbox-recovery-$runID"
$runRoot = [IO.Path]::GetFullPath(
    (Join-Path (Join-Path ([IO.Path]::GetTempPath()) 'bookbox-recovery') $runID)
)
$library = Join-Path $runRoot 'library'
$config = Join-Path $runRoot 'config.yml'
$secrets = Join-Path $runRoot 'secrets'

function New-ResolvedScope {
    param([string] $LibrarySource = $library)

    $volumeNames = [ordered]@{
        recovery_postgres_data = "${project}_recovery_postgres_data"
        recovery_tls_data = "${project}_recovery_tls_data"
        recovery_cover_cache = "${project}_recovery_cover_cache"
        recovery_cover_backups = "${project}_recovery_cover_backups"
    }
    [pscustomobject]@{
        name = $project
        services = [pscustomobject]@{
            postgres = [pscustomobject]@{
                volumes = @(
                    [pscustomobject]@{
                        type = 'volume'
                        source = 'recovery_postgres_data'
                        target = '/var/lib/postgresql'
                    }
                )
            }
            'bookbox-api' = [pscustomobject]@{
                ports = @(
                    [pscustomobject]@{
                        target = 8443
                        published = '18443'
                        host_ip = '127.0.0.1'
                        protocol = 'tcp'
                    }
                )
                volumes = @(
                    [pscustomobject]@{
                        type = 'bind'
                        source = $LibrarySource
                        target = '/library/audiobooks'
                        read_only = $true
                    },
                    [pscustomobject]@{
                        type = 'bind'
                        source = $config
                        target = '/etc/bookbox/config.yml'
                        read_only = $true
                    },
                    [pscustomobject]@{
                        type = 'volume'
                        source = 'recovery_tls_data'
                        target = '/var/lib/bookbox'
                    }
                )
            }
            'bookbox-catalog-worker' = [pscustomobject]@{
                volumes = @(
                    [pscustomobject]@{
                        type = 'bind'
                        source = $LibrarySource
                        target = '/library/audiobooks'
                        read_only = $false
                    },
                    [pscustomobject]@{
                        type = 'bind'
                        source = $config
                        target = '/etc/bookbox/config.yml'
                        read_only = $true
                    },
                    [pscustomobject]@{
                        type = 'volume'
                        source = 'recovery_cover_cache'
                        target = '/var/lib/bookbox/covers'
                    },
                    [pscustomobject]@{
                        type = 'volume'
                        source = 'recovery_cover_backups'
                        target = '/var/lib/bookbox/backups'
                    }
                )
            }
        }
        secrets = [pscustomobject]@{
            postgres_password = [pscustomobject]@{
                file = (Join-Path $secrets 'postgres_password.txt')
            }
            app_password_hash = [pscustomobject]@{
                file = (Join-Path $secrets 'app_password_hash.txt')
            }
            session_key = [pscustomobject]@{
                file = (Join-Path $secrets 'session_key.bin')
            }
            google_books_key = [pscustomobject]@{
                file = (Join-Path $secrets 'google_books_key.txt')
            }
        }
        volumes = [pscustomobject]@{
            recovery_postgres_data = [pscustomobject]@{
                name = $volumeNames.recovery_postgres_data
            }
            recovery_tls_data = [pscustomobject]@{
                name = $volumeNames.recovery_tls_data
            }
            recovery_cover_cache = [pscustomobject]@{
                name = $volumeNames.recovery_cover_cache
            }
            recovery_cover_backups = [pscustomobject]@{
                name = $volumeNames.recovery_cover_backups
            }
        }
    }
}

Test-SyntheticRecoveryResolvedScope `
    -Resolved (New-ResolvedScope) `
    -RunRoot $runRoot `
    -ProjectName $project

$outsidePaths = @(
    'H:\Audiobooks',
    (Join-Path ([IO.Path]::GetTempPath()) 'outside-recovery-library')
)
foreach ($outside in $outsidePaths) {
    $rejected = $false
    try {
        Test-SyntheticRecoveryResolvedScope `
            -Resolved (New-ResolvedScope -LibrarySource $outside) `
            -RunRoot $runRoot `
            -ProjectName $project
    } catch {
        $rejected = $_.Exception.Message -match 'library bind'
    }
    if (-not $rejected) {
        throw "Synthetic recovery accepted an external library bind: $outside"
    }
}

$secretEscape = New-ResolvedScope
$secretEscape.secrets.session_key.file = Join-Path (
    [IO.Path]::GetTempPath()
) 'outside-session-key.bin'
$rejected = $false
try {
    Test-SyntheticRecoveryResolvedScope `
        -Resolved $secretEscape `
        -RunRoot $runRoot `
        -ProjectName $project
} catch {
    $rejected = $_.Exception.Message -match 'secret'
}
if (-not $rejected) {
    throw 'Synthetic recovery accepted an external secret source.'
}

Write-Output 'PASS: synthetic recovery resolved scope rejects external mounts and secrets.'
