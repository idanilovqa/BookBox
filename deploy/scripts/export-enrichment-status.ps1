param([string]$OutputPath = (Join-Path $PSScriptRoot '../../artifacts/enrichment-status/current-books.csv'))
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$composePath = Join-Path $PSScriptRoot '../compose.yml'
$sqlPath = Join-Path $PSScriptRoot '../sql/enrichment-status.sql'
$csv = @(Get-Content -Raw -LiteralPath $sqlPath | & docker compose -f $composePath exec -T postgres psql -X -v ON_ERROR_STOP=1 -U bookbox -d bookbox -f -)
if ($LASTEXITCODE -ne 0) { throw 'Current enrichment status query failed.' }
$rows = @($csv | ConvertFrom-Csv)
if (@($rows | Group-Object book_id | Where-Object Count -gt 1).Count -gt 0) { throw 'Duplicate book IDs in current status output.' }
$targetPath = [IO.Path]::GetFullPath($OutputPath)
[IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($targetPath)) | Out-Null
$csv | Set-Content -LiteralPath $targetPath -Encoding utf8
$pending = @($rows | Where-Object current_status -ne 'complete')
$pending | Export-Csv -LiteralPath ([IO.Path]::ChangeExtension($targetPath, 'pending.csv')) -NoTypeInformation -Encoding utf8
$lines = [Collections.Generic.List[string]]::new()
$lines.Add('# Current enrichment status')
$lines.Add('')
$lines.Add(('Generated at {0:u}. One row per active book with audio. Historical failures and retired records are excluded; separate physical copies are preserved.' -f [DateTime]::UtcNow))
$lines.Add('')
$lines.Add('Latest job errors are historical context, not the current book status. Cover columns distinguish indexed files from recorded publication events.')
$lines.Add('')
foreach ($group in @($rows | Group-Object current_status | Sort-Object Name)) { $lines.Add(('- {0}: {1}' -f $group.Name, $group.Count)) }
$lines.Add('')
$lines.Add('| Book | Author | Current status | Folder | Book ID |')
$lines.Add('|---|---|---|---|---|')
foreach ($row in $pending) {
 $cells = @($row.title,$row.authors,$row.current_status,$row.source_folder,$row.book_id) | ForEach-Object { ([string]$_).Replace('|','\|').Replace("`r",' ').Replace("`n",' ') }
 $lines.Add('| ' + ($cells -join ' | ') + ' |')
}
$lines | Set-Content -LiteralPath ([IO.Path]::ChangeExtension($targetPath,'md')) -Encoding utf8
$rows | Group-Object current_status | Sort-Object Name | Select-Object Name,Count
Write-Output "Report: $targetPath"
