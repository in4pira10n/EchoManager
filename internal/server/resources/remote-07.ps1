$ErrorActionPreference = 'Stop'
$launchToken = '__EM_LAUNCH_TOKEN__'
$id = %s
$exe = %s
$workingDirectory = [IO.Path]::GetDirectoryName($exe)
$arguments = %s
$environment = ConvertFrom-Json -InputObject %s
$root = Join-Path $env:USERPROFILE '.echomanager'
$metaPath = Join-Path (Join-Path $root 'instances') ($id + '.json')
$stdoutPath = Join-Path (Join-Path $root 'logs') ($id + '.out.log')
$stderrPath = Join-Path (Join-Path $root 'logs') ($id + '.err.log')
try {
  foreach ($entry in $environment.PSObject.Properties) {
    [Environment]::SetEnvironmentVariable($entry.Name, [string]$entry.Value, 'Process')
  }
  $startOptions = @{
    FilePath = $exe
    WorkingDirectory = $workingDirectory
    PassThru = $true
    WindowStyle = 'Hidden'
    RedirectStandardOutput = $stdoutPath
    RedirectStandardError = $stderrPath
  }
  if ($arguments.Length -gt 0) { $startOptions.ArgumentList = $arguments }
  $process = Start-Process @startOptions
  Start-Sleep -Milliseconds 150
  $process.Refresh()
  if ($process.HasExited) { throw '进程启动后立即退出；请查看远程日志' }
  $started = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
  $record = [ordered]@{
    launchToken = $launchToken
    pid = $process.Id
    executable = $exe
    startedTicks = $process.StartTime.ToUniversalTime().Ticks
    startedAt = $started
  }
  $temporaryPath = $metaPath + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
  $record | ConvertTo-Json -Compress | Set-Content -LiteralPath $temporaryPath -Encoding UTF8
  Move-Item -LiteralPath $temporaryPath -Destination $metaPath -Force
} catch {
  try { Add-Content -LiteralPath $stderrPath -Value $_.ToString() -Encoding UTF8 } catch {}
  $record = [ordered]@{ launchToken = $launchToken; error = $_.Exception.Message }
  $temporaryPath = $metaPath + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
  $record | ConvertTo-Json -Compress | Set-Content -LiteralPath $temporaryPath -Encoding UTF8
  Move-Item -LiteralPath $temporaryPath -Destination $metaPath -Force
}