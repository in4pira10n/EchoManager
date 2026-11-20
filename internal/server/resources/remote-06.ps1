$ErrorActionPreference = 'Stop'
$id = %s
$exe = %s
$port = %s
$workingDirectory = [IO.Path]::GetDirectoryName($exe)
$root = Join-Path $env:USERPROFILE '.echomanager'
$instanceDir = Join-Path $root 'instances'
$logDir = Join-Path $root 'logs'
$metaPath = Join-Path $instanceDir ($id + '.json')
$launchScriptPath = Join-Path $instanceDir ($id + '.launch.ps1')
[void][IO.Directory]::CreateDirectory($instanceDir)
[void][IO.Directory]::CreateDirectory($logDir)
function Get-ManagerRecord { if (Test-Path -LiteralPath $metaPath) { return (Get-Content -LiteralPath $metaPath -Raw | ConvertFrom-Json) }; return $null }
function Get-ManagerProcess($record) {
  if ($null -eq $record) { return $null }
  $process = Get-Process -Id ([int]$record.pid) -ErrorAction SilentlyContinue
  if ($null -eq $process) { return $null }
  try {
    if ([string]$record.executable -ne '__ECHOMANAGER_ADOPTED__' -and [IO.Path]::GetFullPath($process.Path) -ine [IO.Path]::GetFullPath([string]$record.executable)) { return $null }
    if ([long]$process.StartTime.ToUniversalTime().Ticks -ne [long]$record.startedTicks) { return $null }
  } catch { return $null }
  return $process
}
%s
$existing = Get-ManagerRecord
if ($null -ne (Get-ManagerProcess $existing)) { throw ('此实例已在目标设备运行（PID ' + $existing.pid + '）') }
if ($null -ne $existing) {
  $adopted = Adopt-ManagerProcess $existing
  if ($null -ne $adopted) { Write-Output ('__EM_STARTED__|' + $adopted.Id + '|' + $existing.startedAt); exit 0 }
  if ($null -ne (Get-ManagerRawProcess $existing)) { throw ('远端记录的 PID ' + $existing.pid + ' 仍在运行，但身份校验失败；无法从端口 ' + $port + ' 唯一确认监听进程（已尝试 Get-NetTCPConnection 和 netstat），为避免接管错误进程或启动重复实例，未执行接管') }
} else {
  $adopted = Adopt-ManagerProcess ([pscustomobject]@{})
  if ($null -ne $adopted) { $record = Get-ManagerRecord; Write-Output ('__EM_STARTED__|' + $record.pid + '|' + $record.startedAt); exit 0 }
  if (@(Get-PortListenerOwnerIds).Count -gt 0) { throw ('配置端口 ' + $port + ' 已有监听进程，但无法唯一确认其身份；为避免启动重复实例，未执行启动') }
}
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw '程序路径不存在或不是文件' }
$launchToken = [guid]::NewGuid().ToString('N')
$launcherScript = %s
$launcherScript = $launcherScript.Replace('__EM_LAUNCH_TOKEN__', $launchToken)
Set-Content -LiteralPath $launchScriptPath -Value $launcherScript -Encoding UTF8
$powerShellPath = Join-Path $PSHOME 'powershell.exe'
$commandLine = '"' + $powerShellPath + '" -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "' + $launchScriptPath + '"'
$processClass = [WMIClass]'\\.\root\cimv2:Win32_Process'
$creation = $processClass.Create($commandLine, $workingDirectory, $null)
if ($creation.ReturnValue -ne 0) { throw ('无法通过 Windows Process Provider 启动独立进程（错误码 ' + $creation.ReturnValue + '）') }
$deadline = [DateTime]::UtcNow.AddSeconds(15)
while ([DateTime]::UtcNow -lt $deadline) {
  if (Test-Path -LiteralPath $metaPath) {
    $record = Get-ManagerRecord
    if ($null -ne $record -and $record.launchToken -eq $launchToken) {
      Remove-Item -LiteralPath $launchScriptPath -Force -ErrorAction SilentlyContinue
      if ($record.error) { throw [string]$record.error }
      Write-Output ('__EM_STARTED__|' + $record.pid + '|' + $record.startedAt)
      exit 0
    }
  }
  Start-Sleep -Milliseconds 100
}
throw '独立进程启动器未能及时回报结果；请检查目标设备远程日志'