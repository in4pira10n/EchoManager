function Get-ManagerRawProcess($record) {
  if ($null -eq $record) { return $null }
  try { return (Get-Process -Id ([int]$record.pid) -ErrorAction SilentlyContinue) } catch { return $null }
}
function Get-PortListenerOwnerIds {
  $owners = @()
  try {
    $owners = @(Get-NetTCPConnection -State Listen -LocalPort ([int]$port) -ErrorAction Stop | ForEach-Object { [int]$_.OwningProcess } | Sort-Object -Unique)
  } catch {}
  if ($owners.Count -gt 0) { return $owners }

  try {
    $netstatPath = Join-Path $env:SystemRoot 'System32\netstat.exe'
    if (-not (Test-Path -LiteralPath $netstatPath)) { $netstatPath = 'netstat.exe' }
    $portSuffix = ':' + [string]$port
    $owners = @(
      & $netstatPath -ano -p tcp 2>$null | ForEach-Object {
        $columns = $_.Trim() -split '\s+'
        if ($columns.Count -lt 5) { return }
        $localAddress = $columns[$columns.Count - 4]
        $state = $columns[$columns.Count - 2]
        if (-not $localAddress.EndsWith($portSuffix, [StringComparison]::OrdinalIgnoreCase)) { return }
        if ($state -notin @('LISTENING', 'LISTEN', '侦听', '监听')) { return }
        $ownerId = 0
        if ([int]::TryParse($columns[$columns.Count - 1], [ref]$ownerId) -and $ownerId -gt 0) { $ownerId }
      } | Sort-Object -Unique
    )
  } catch {}
  return $owners
}
function Get-PortListenerProcess {
  $owners = @(Get-PortListenerOwnerIds)
  if ($owners.Count -ne 1) { return $null }
  try { return (Get-Process -Id ([int]$owners[0]) -ErrorAction Stop) } catch { return $null }
}
function Set-ManagerRecordProperty($record, [string]$name, $value) {
  $record | Add-Member -MemberType NoteProperty -Name $name -Value $value -Force
}
function Set-ManagerRecord($record, $process) {
  if ($null -eq $record) { $record = [pscustomobject]@{} }
  $sameStart = $false
  try { $sameStart = ([int]$record.pid -eq $process.Id -and [long]$record.startedTicks -eq [long]$process.StartTime.ToUniversalTime().Ticks) } catch {}
  try { $executable = [IO.Path]::GetFullPath($process.Path) } catch { $executable = '__ECHOMANAGER_ADOPTED__' }
  $startedTicks = [long]$process.StartTime.ToUniversalTime().Ticks
  $startedAt = 0
  try { $startedAt = [long]$record.startedAt } catch {}
  if (-not $sameStart -or $startedAt -le 0) {
    $utcEpoch = [datetime]::SpecifyKind([datetime]'1970-01-01', [DateTimeKind]::Utc)
    $startedAt = [long]($process.StartTime.ToUniversalTime().Subtract($utcEpoch).TotalSeconds)
  }
  Set-ManagerRecordProperty $record 'executable' $executable
  Set-ManagerRecordProperty $record 'pid' ([int]$process.Id)
  Set-ManagerRecordProperty $record 'startedTicks' $startedTicks
  Set-ManagerRecordProperty $record 'startedAt' $startedAt
  [void][IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($metaPath))
  $temporaryPath = $metaPath + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
  $record | ConvertTo-Json -Compress | Set-Content -LiteralPath $temporaryPath -Encoding UTF8
  Move-Item -LiteralPath $temporaryPath -Destination $metaPath -Force
  return $true
}
function Adopt-ManagerProcess($record) {
  $recorded = Get-ManagerRawProcess $record
  if ($null -ne $recorded) {
    try {
      if ([long]$recorded.StartTime.ToUniversalTime().Ticks -eq [long]$record.startedTicks) {
        if (Set-ManagerRecord $record $recorded) { return $recorded }
      }
    } catch {}
  }
  $listener = Get-PortListenerProcess
  if ($null -ne $listener -and (Set-ManagerRecord $record $listener)) { return $listener }
  return $null
}