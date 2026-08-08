Set-StrictMode -Version Latest

function Read-BookBoxStartupEnvironment {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $Path
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "BookBox environment file does not exist: $Path"
    }

    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path) {
        $trimmed = $line.Trim()
        if (-not $trimmed -or $trimmed.StartsWith('#')) {
            continue
        }

        $separator = $trimmed.IndexOf('=')
        if ($separator -lt 1) {
            continue
        }
        $name = $trimmed.Substring(0, $separator).Trim()
        if ($name -notin @('BOOKBOX_BIND_ADDRESS', 'BOOKBOX_HOST_PORT')) {
            continue
        }
        if ($values.ContainsKey($name)) {
            throw "BookBox environment file contains duplicate $name entries."
        }
        $values[$name] = $trimmed.Substring($separator + 1).Trim()
    }

    $address = [string]$values['BOOKBOX_BIND_ADDRESS']
    if ([string]::IsNullOrWhiteSpace($address)) {
        throw 'BOOKBOX_BIND_ADDRESS is missing from the BookBox environment file.'
    }

    $port = 0
    if (
        -not [int]::TryParse([string]$values['BOOKBOX_HOST_PORT'], [ref]$port) -or
        $port -lt 1 -or
        $port -gt 65535
    ) {
        throw 'BOOKBOX_HOST_PORT in the BookBox environment file must be an integer from 1 through 65535.'
    }

    [pscustomobject]@{
        Address = $address
        Port = $port
    }
}
