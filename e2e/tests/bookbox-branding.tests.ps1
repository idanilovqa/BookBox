$ErrorActionPreference = 'Stop'
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$runtimeRoots = @('android', 'server', 'deploy', 'e2e', 'README.md', 'RELEASE_NOTES.md')
Push-Location $repo
try {
    $allMatches = @(
        & rg -n -i 'netstorage' @runtimeRoots --glob '!bookbox-branding.tests.ps1'
    )
    $legacyIdentifierMatches = @($allMatches | Where-Object {
        $_ -notmatch '^README\.md:\d+:- \[Migrate from netStorage\]\(docs/migration-from-netstorage\.md\)$' -and
        $_ -notmatch '^RELEASE_NOTES\.md:\d+:.*\bnetStorage\b.*(?:migrat|rename|old app|old deployment)'
    })
    $legacyPaths = @(
        & rg --files |
            Where-Object {
                $_ -match '(?i)netstorage' -and
                $_ -cne 'docs\migration-from-netstorage.md'
            }
    )
} finally {
    Pop-Location
}
if ($legacyIdentifierMatches.Count -gt 0) {
    throw "Legacy product identifiers remain:`n$($legacyIdentifierMatches -join "`n")"
}
if ($legacyPaths.Count -gt 0) {
    throw "Legacy product identifiers remain in paths:`n$($legacyPaths -join "`n")"
}
$gradle = Get-Content -Raw (Join-Path $repo 'android\app\build.gradle.kts')
if ($gradle -notmatch 'applicationId = "com\.rendref\.bookbox"') {
    throw 'Android application ID is not com.rendref.bookbox.'
}
Write-Output 'PASS: BookBox product and technical identity is consistent.'
