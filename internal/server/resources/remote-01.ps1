$payload = [Console]::In.ReadToEnd().Trim()
if ([string]::IsNullOrWhiteSpace($payload)) { throw '远程启动脚本为空' }
$scriptBytes = [Convert]::FromBase64String($payload)
$scriptText = [Text.Encoding]::UTF8.GetString($scriptBytes)
Invoke-Expression $scriptText