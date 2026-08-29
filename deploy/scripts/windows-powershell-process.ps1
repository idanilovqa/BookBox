function Set-BookBoxWindowsPowerShellModulePath {
    param(
        [Parameter(Mandatory)]
        [Diagnostics.ProcessStartInfo] $StartInfo
    )

    if ([IO.Path]::GetFileName($StartInfo.FileName) -ine 'powershell.exe') {
        return
    }

    $paths = [Collections.Generic.List[string]]::new()
    $seen = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::OrdinalIgnoreCase
    )
    $documents = [Environment]::GetFolderPath(
        [Environment+SpecialFolder]::MyDocuments
    )
    $candidates = @()
    if (-not [string]::IsNullOrWhiteSpace($documents)) {
        $candidates += Join-Path $documents 'WindowsPowerShell\Modules'
    }
    if (-not [string]::IsNullOrWhiteSpace($env:ProgramFiles)) {
        $candidates += Join-Path $env:ProgramFiles 'WindowsPowerShell\Modules'
    }
    if (-not [string]::IsNullOrWhiteSpace($env:SystemRoot)) {
        $candidates += Join-Path $env:SystemRoot 'system32\WindowsPowerShell\v1.0\Modules'
    }
    $candidates += @($env:PSModulePath -split ';' | Where-Object {
        $_ -match '(?i)[\\/]WindowsPowerShell[\\/]Modules(?:$|[\\/])'
    })
    foreach ($candidate in $candidates) {
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and $seen.Add($candidate)) {
            $paths.Add($candidate)
        }
    }
    if ($paths.Count -eq 0) {
        throw 'Could not construct a Windows PowerShell module path.'
    }
    $StartInfo.EnvironmentVariables['PSModulePath'] = $paths -join ';'
}
