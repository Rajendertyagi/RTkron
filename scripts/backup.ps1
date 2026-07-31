param(
  [string]$src = "D:\RTkron",
  [string]$dstRoot = "D:\RTkron_backup"
)
$ts = Get-Date -Format yyyyMMdd_HHmmss
$dst = Join-Path $dstRoot $ts
Copy-Item -Path $src -Destination $dst -Recurse -Force
Write-Host "Backup completed to $dst"
