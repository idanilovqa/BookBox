[CmdletBinding()]
param(
    [string] $Address,
    [Nullable[int]] $Port,
    [string] $DiscoveryExecutable,
    [string] $StateDirectory,
    [string] $ComposeFile
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$deployRoot = Split-Path -Parent $PSScriptRoot
$repoRoot = Split-Path -Parent $deployRoot
. (Join-Path $PSScriptRoot 'startup-environment.ps1')
$needsStartupEnvironment = (
    (-not $PSBoundParameters.ContainsKey('Address') -and -not $env:BOOKBOX_BIND_ADDRESS) -or
    (-not $PSBoundParameters.ContainsKey('Port') -and -not $env:BOOKBOX_HOST_PORT)
)
$startupEnvironment = $null
if ($needsStartupEnvironment) {
    $startupEnvironment = Read-BookBoxStartupEnvironment -Path (Join-Path $deployRoot '.env')
}
if (-not $PSBoundParameters.ContainsKey('Address')) {
    $Address = if ($env:BOOKBOX_BIND_ADDRESS) {
        $env:BOOKBOX_BIND_ADDRESS
    } else {
        $startupEnvironment.Address
    }
}
if (-not $PSBoundParameters.ContainsKey('Port')) {
    $Port = if ($env:BOOKBOX_HOST_PORT) {
        [int]$env:BOOKBOX_HOST_PORT
    } else {
        $startupEnvironment.Port
    }
}
if (-not $ComposeFile) {
    $ComposeFile = Join-Path $deployRoot 'compose.yml'
}
$composeFile = [System.IO.Path]::GetFullPath($ComposeFile)
if (-not $DiscoveryExecutable) {
    $DiscoveryExecutable = Join-Path $repoRoot 'artifacts\bookbox-discovery.exe'
}
if (-not $StateDirectory) {
    $StateDirectory = Join-Path $deployRoot '.state'
}
. (Join-Path $PSScriptRoot 'discovery-process.ps1')

function Test-PrivateIPv4 {
    param([Parameter(Mandatory)][Net.IPAddress] $IPAddress)

    $bytes = $IPAddress.GetAddressBytes()
    if ($bytes.Count -ne 4) { return $false }
    return (
        $bytes[0] -eq 10 -or
        ($bytes[0] -eq 172 -and $bytes[1] -ge 16 -and $bytes[1] -le 31) -or
        ($bytes[0] -eq 192 -and $bytes[1] -eq 168)
    )
}

function Assert-BookBoxFirewallRule {
    param(
        [Parameter(Mandatory)][string] $DisplayName,
        [Parameter(Mandatory)][string] $Protocol,
        [Parameter(Mandatory)][int] $LocalPort
    )

    $rules = @(Get-NetFirewallRule -DisplayName $DisplayName -ErrorAction SilentlyContinue)
    if ($rules.Count -ne 1) {
        throw "Required firewall rule is missing or ambiguous: $DisplayName. Run install-bookbox-discovery.ps1 elevated."
    }
    $rule = $rules[0]
    $portFilter = $rule | Get-NetFirewallPortFilter
    $addressFilter = $rule | Get-NetFirewallAddressFilter
    if (
        $rule.Enabled -ne 'True' -or
        $rule.Direction -ne 'Inbound' -or
        $rule.Action -ne 'Allow' -or
        $rule.Profile -ne 'Private' -or
        $rule.Group -ne 'BookBox' -or
        [string]$portFilter.Protocol -cne $Protocol -or
        [string]$portFilter.LocalPort -cne [string]$LocalPort -or
        [string]$addressFilter.LocalAddress -cne $Address -or
        [string]$addressFilter.RemoteAddress -cne 'LocalSubnet'
    ) {
        throw "Firewall rule does not match the required private-LAN scope: $DisplayName"
    }
}

$parsedAddress = $null
if (
    -not [Net.IPAddress]::TryParse($Address, [ref]$parsedAddress) -or
    -not (Test-PrivateIPv4 -IPAddress $parsedAddress)
) {
    throw 'BOOKBOX_BIND_ADDRESS must be an RFC1918 private IPv4 address.'
}
$assigned = Get-NetIPAddress -AddressFamily IPv4 -IPAddress $Address -ErrorAction SilentlyContinue |
    Where-Object { $_.AddressState -eq 'Preferred' } |
    Select-Object -First 1
if (-not $assigned) {
    throw "BOOKBOX_BIND_ADDRESS is not assigned to this Windows machine: $Address"
}
$profile = Get-NetConnectionProfile -InterfaceIndex $assigned.InterfaceIndex -ErrorAction Stop
if ($profile.NetworkCategory -ne 'Private') {
    throw "The selected address must belong to a Windows Private network profile: $Address"
}
Assert-BookBoxFirewallRule -DisplayName "BookBox HTTP $Port" -Protocol 'TCP' -LocalPort $Port
Assert-BookBoxFirewallRule -DisplayName 'BookBox mDNS' -Protocol 'UDP' -LocalPort 5353

$env:BOOKBOX_BIND_ADDRESS = $Address
$env:BOOKBOX_HOST_PORT = [string]$Port
$composeStarted = $false
$discoveryStarted = $false
try {
    & docker compose -f $composeFile up -d
    if ($LASTEXITCODE -ne 0) {
        throw "Docker Compose start failed with exit code $LASTEXITCODE."
    }
    $composeStarted = $true

    $readyUri = "http://${Address}:$Port/v1/ready"
    $deadline = [DateTime]::UtcNow.AddMinutes(2)
    $response = $null
    do {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri $readyUri -TimeoutSec 3
            if ($response.StatusCode -eq 200) { break }
        } catch {
            Start-Sleep -Seconds 2
        }
    } while ([DateTime]::UtcNow -lt $deadline)
    if (-not $response -or $response.StatusCode -ne 200) {
        throw "BookBox did not become ready at $readyUri."
    }

    $record = Start-BookBoxDiscovery `
        -Executable $DiscoveryExecutable `
        -Address $Address `
        -Port $Port `
        -StateDirectory $StateDirectory
    $discoveryStarted = $true
    if (-not (Test-BookBoxDiscovery -StateDirectory $StateDirectory)) {
        throw 'BookBox discovery process failed its ownership check.'
    }

    $resolved = Resolve-DnsName -Name 'bookbox.local' -Type A -QuickTimeout -ErrorAction Stop |
        Where-Object { $_.IPAddress -eq $Address } |
        Select-Object -First 1
    if (-not $resolved) {
        throw "bookbox.local did not resolve to $Address."
    }
    $serviceRecord = Resolve-DnsName `
        -Name '_bookbox._tcp.local' `
        -Type SRV `
        -QuickTimeout `
        -ErrorAction Stop |
        Where-Object {
            [int]$_.Port -eq $Port -and
            [string]$_.NameTarget -match '^(?i:bookbox\.local)\.?$'
        } |
        Select-Object -First 1
    if (-not $serviceRecord) {
        throw "_bookbox._tcp.local did not advertise bookbox.local on port $Port."
    }

    Write-Output "BookBox is ready at http://bookbox.local:$Port"
} catch {
    if ($discoveryStarted) {
        Stop-BookBoxDiscovery -StateDirectory $StateDirectory
    }
    if ($composeStarted) {
        & docker compose -f $composeFile down --remove-orphans
    }
    throw
}
