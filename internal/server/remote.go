package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/masterzen/winrm"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const remoteOutputLimit = 300 * 1024
const remoteStartTimeout = 60 * time.Second

type remoteInstanceState struct {
	Status     string
	PID        int
	ActualPort int
	StartedAt  string
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.Len() >= b.limit {
		return len(data), nil
	}
	if len(data) > b.limit-b.Len() {
		data = data[:b.limit-b.Len()]
	}
	return b.Buffer.Write(data)
}

func runRemoteCommand(ctx context.Context, device Device, script string) (string, error) {
	switch device.Transport {
	case "ssh":
		return runSSHCommand(ctx, device, script)
	case "winrm":
		return runWinRMCommand(ctx, device, script)
	default:
		return "", fmt.Errorf("不支持的远程连接方式：%s", device.Transport)
	}
}

func runSSHCommand(ctx context.Context, device Device, script string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位 Manager 用户目录：%w", err)
	}
	knownHostsPath := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(knownHostsPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("SSH 主机密钥未配置；请先将已核验的设备主机密钥加入 %s", knownHostsPath)
		}
		return "", fmt.Errorf("无法读取 SSH known_hosts：%w", err)
	}
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return "", fmt.Errorf("读取 SSH known_hosts 失败：%w", err)
	}
	var authMethod ssh.AuthMethod
	switch device.AuthType {
	case "key":
		key, err := os.ReadFile(device.KeyPath)
		if err != nil {
			return "", fmt.Errorf("读取 SSH 私钥失败：%w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return "", fmt.Errorf("解析 SSH 私钥失败：%w", err)
		}
		authMethod = ssh.PublicKeys(signer)
	case "password":
		authMethod = ssh.Password(device.Password)
	default:
		return "", errors.New("SSH 认证方式无效")
	}
	address := net.JoinHostPort(device.Address, strconv.Itoa(device.Port))
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", fmt.Errorf("连接 SSH 设备失败：%w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	clientConfig := &ssh.ClientConfig{
		User: device.Username, Auth: []ssh.AuthMethod{authMethod}, HostKeyCallback: hostKeyCallback,
		Timeout: 10 * time.Second,
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, clientConfig)
	if err != nil {
		_ = connection.Close()
		return "", fmt.Errorf("SSH 握手或认证失败：%w", err)
	}
	client := ssh.NewClient(clientConnection, channels, requests)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("创建 SSH 会话失败：%w", err)
	}
	defer session.Close()
	stdout := &limitedBuffer{limit: remoteOutputLimit}
	stderr := &limitedBuffer{limit: 32 * 1024}
	session.Stdout = stdout
	session.Stderr = stderr
	session.Stdin = strings.NewReader(script)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	err = session.Run("bash -s")
	close(done)
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = strings.TrimSpace(stdout.String())
		}
		if ctx.Err() != nil {
			return stdout.String(), ctx.Err()
		}
		if message != "" {
			return stdout.String(), fmt.Errorf("SSH 远程命令失败：%s", message)
		}
		return stdout.String(), fmt.Errorf("SSH 远程命令失败：%w", err)
	}
	return stdout.String(), nil
}

func runWinRMCommand(ctx context.Context, device Device, script string) (string, error) {
	return runWinRMScriptWithStdin(ctx, device, script)
}

func winRMScriptStdin(script string) (string, string) {
	bootstrap := `$payload = [Console]::In.ReadToEnd().Trim()
if ([string]::IsNullOrWhiteSpace($payload)) { throw '远程启动脚本为空' }
$scriptBytes = [Convert]::FromBase64String($payload)
$scriptText = [Text.Encoding]::UTF8.GetString($scriptBytes)
Invoke-Expression $scriptText`
	return bootstrap, base64.StdEncoding.EncodeToString([]byte(script))
}

func runWinRMScriptWithStdin(ctx context.Context, device Device, script string) (string, error) {
	bootstrap, encodedScript := winRMScriptStdin(script)
	return runWinRM(ctx, device, bootstrap, encodedScript)
}

func runWinRM(ctx context.Context, device Device, script string, stdin string) (string, error) {
	endpoint := winrm.NewEndpoint(device.Address, device.Port, true, device.InsecureTLS, nil, nil, nil, 20*time.Second)
	parameters := winrm.NewParameters("PT20S", "en-US", 153600)
	client, err := winrm.NewClientWithParameters(endpoint, device.Username, device.Password, parameters)
	if err != nil {
		return "", fmt.Errorf("创建 WinRM 客户端失败：%w", err)
	}
	stdout, stderr, exitCode, err := client.RunPSWithContextWithString(ctx, script, stdin)
	if err != nil {
		return stdout, fmt.Errorf("WinRM 远程命令失败：%w", err)
	}
	if exitCode != 0 {
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = strings.TrimSpace(stdout)
		}
		message = decodePowerShellError(message)
		return stdout, fmt.Errorf("WinRM 远程命令退出码 %d：%s", exitCode, message)
	}
	return stdout, nil
}

func remoteProbeScript(device Device) string {
	if device.Transport == "winrm" {
		return `Write-Output "__EM_PROBE__|$env:COMPUTERNAME|$($PSVersionTable.PSVersion.ToString())"`
	}
	return `printf '__EM_PROBE__|%s|%s\n' "$(hostname)" "$(uname -s)"`
}

func remoteStartScript(device Device, instance Instance) string {
	if device.Transport == "winrm" {
		return windowsStartScript(instance)
	}
	return unixStartScript(instance)
}

func remoteStatusScript(device Device, instance Instance) string {
	if device.Transport == "winrm" {
		return windowsStatusScript(instance)
	}
	return unixStatusScript(instance)
}

func remoteStopScript(device Device, instance Instance) string {
	if device.Transport == "winrm" {
		return windowsStopScript(instance)
	}
	return unixStopScript(instance)
}

func remoteLogsScript(device Device, instance Instance) string {
	if device.Transport == "winrm" {
		return windowsLogsScript(instance)
	}
	id := shellQuote(instance.ID)
	return fmt.Sprintf(`set -eu
log="$HOME/.echomanager/logs/%s.log"
if [ ! -f "$log" ]; then printf '尚无远程运行日志。\n'; exit 0; fi
tail -c 262144 "$log"`, id)
}

const unixProcessIdentityHelpers = `process_start_token() {
  pid="$1"
  if [ -r "/proc/$pid/stat" ]; then
    sed 's/^.*) //' "/proc/$pid/stat" | awk '{print $20}'
  else
    ps -p "$pid" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
  fi
}
process_matches() {
  pid="$1"; expected="$2"; saved_token="$3"
  kill -0 "$pid" 2>/dev/null || return 1
  if [ "$expected" != "__ECHOMANAGER_ADOPTED__" ]; then
    if [ -e "/proc/$pid/exe" ]; then
      actual=$(readlink "/proc/$pid/exe" 2>/dev/null) || return 1
      expected_real=$(readlink -f "$expected" 2>/dev/null || printf '%s' "$expected")
      [ "$actual" = "$expected_real" ] || return 1
    else
      command=$(ps -p "$pid" -o command= 2>/dev/null) || return 1
      case "$command" in "$expected"|"$expected "*) ;; *) return 1 ;; esac
    fi
  fi
  current_token=$(process_start_token "$pid")
  [ -n "$current_token" ] && [ "$current_token" = "$saved_token" ]
}
port_listener_pids() {
  pids=""
  if command -v lsof >/dev/null 2>&1; then
    pids=$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | sort -u)
  fi
  if [ -z "$pids" ] && command -v ss >/dev/null 2>&1; then
    pids=$(ss -H -ltnp "sport = :$port" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | sort -u)
  fi
  printf '%s' "$pids"
}
write_adopted_record() {
  adopted_pid="$1"; adopted_token="$2"; adopted_started="$3"
  [ -n "$adopted_started" ] || adopted_started=$(date +%s)
  tmp="$meta.tmp.$$"
  printf '%s\n%s\n%s\n%s\n' "$adopted_pid" "$adopted_token" "__ECHOMANAGER_ADOPTED__" "$adopted_started" >"$tmp" || return 1
  mv "$tmp" "$meta" || return 1
  printf '__EM_STARTED__|%s|%s\n' "$adopted_pid" "$adopted_started"
}
adopt_port_listener() {
  pids=$(port_listener_pids)
  [ -n "$pids" ] || return 1
  set -- $pids
  [ "$#" -eq 1 ] || return 1
  adopted_pid="$1"
  kill -0 "$adopted_pid" 2>/dev/null || return 1
  adopted_token=$(process_start_token "$adopted_pid")
  [ -n "$adopted_token" ] || return 1
  if [ "$adopted_pid" = "${old_pid:-}" ] && [ "$adopted_token" = "${old_token:-}" ]; then
    adopted_started="${old_started:-}"
  else
    adopted_started=$(date +%s)
  fi
  write_adopted_record "$adopted_pid" "$adopted_token" "$adopted_started"
}`

func unixStartScript(instance Instance) string {
	id := shellQuote(instance.ID)
	executable := shellQuote(instance.ExecutablePath)
	port := strconv.Itoa(instance.ConfiguredPort)
	var env strings.Builder
	keys := make([]string, 0, len(instance.Environment))
	for key := range instance.Environment {
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		fmt.Fprintf(&env, " %s", shellQuote(key+"="+instance.Environment[key]))
	}
	var args strings.Builder
	for _, argument := range instance.Arguments {
		fmt.Fprintf(&args, " %s", shellQuote(argument))
	}
	return fmt.Sprintf(`set -u
umask 077
id=%s
exe=%s
port=%s
root="$HOME/.echomanager"
meta="$root/instances/$id.meta"
log="$root/logs/$id.log"
mkdir -p "$root/instances" "$root/logs"
%s
if [ ! -x "$exe" ]; then printf '__EM_ERROR__|程序不存在或不可执行'; exit 4; fi
if [ -f "$meta" ]; then
  old_pid=$(sed -n '1p' "$meta")
  old_token=$(sed -n '2p' "$meta")
  old_exe=$(sed -n '3p' "$meta")
  old_started=$(sed -n '4p' "$meta")
  if [ -n "$old_pid" ] && process_matches "$old_pid" "$old_exe" "$old_token"; then
    printf '__EM_ERROR__|此实例已在目标设备运行（PID %%s）' "$old_pid"; exit 5
  fi
  if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
    current_token=$(process_start_token "$old_pid")
    if [ -n "$old_token" ] && [ "$current_token" = "$old_token" ]; then
      write_adopted_record "$old_pid" "$current_token" "$old_started"
      exit 0
    fi
    if adopted=$(adopt_port_listener); then printf '%%s\n' "$adopted"; exit 0; fi
    printf '__EM_ERROR__|远端进程身份已变化，且无法唯一确认配置端口的监听进程；为避免启动重复实例，未执行接管'; exit 8
  fi
  if adopted=$(adopt_port_listener); then printf '%%s\n' "$adopted"; exit 0; fi
fi
nohup env%s "$exe"%s >>"$log" 2>&1 </dev/null &
pid=$!
sleep 0.15
if ! kill -0 "$pid" 2>/dev/null; then printf '__EM_ERROR__|进程启动后立即退出，请检查远程日志'; exit 6; fi
token=$(process_start_token "$pid")
started=$(date +%%s)
if [ -z "$token" ]; then kill -TERM "$pid" 2>/dev/null || true; printf '__EM_ERROR__|无法核验远程进程身份'; exit 7; fi
tmp="$meta.tmp.$$"
printf '%%s\n%%s\n%%s\n%%s\n' "$pid" "$token" "$exe" "$started" >"$tmp"
mv "$tmp" "$meta"
printf '__EM_STARTED__|%%s|%%s\n' "$pid" "$started"`, id, executable, port, unixProcessIdentityHelpers, env.String(), args.String())
}

func unixStatusScript(instance Instance) string {
	id := shellQuote(instance.ID)
	port := strconv.Itoa(instance.ConfiguredPort)
	return fmt.Sprintf(`set -u
id=%s
port=%s
meta="$HOME/.echomanager/instances/$id.meta"
%s
port_listening() {
  if command -v ss >/dev/null 2>&1 && ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .; then return 0; fi
  if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | grep -q .; then return 0; fi
  if command -v netstat >/dev/null 2>&1 && netstat -an 2>/dev/null | awk -v port="$port" '$NF == "LISTEN" && $0 ~ ("[.:]" port "[[:space:]]") { found=1 } END { exit !found }'; then return 0; fi
  { echo >/dev/tcp/127.0.0.1/$port; } 2>/dev/null || { echo >/dev/tcp/::1/$port; } 2>/dev/null
}
if [ ! -f "$meta" ]; then printf '__EM_STATE__|stopped|0|0|\n'; exit 0; fi
old_pid=$(sed -n '1p' "$meta"); old_token=$(sed -n '2p' "$meta"); old_exe=$(sed -n '3p' "$meta"); old_started=$(sed -n '4p' "$meta")
pid=$old_pid; token=$old_token; exe=$old_exe; started=$old_started
if ! process_matches "$pid" "$exe" "$token"; then
  current_token=$(process_start_token "$pid")
  if kill -0 "$pid" 2>/dev/null && [ -n "$token" ] && [ "$current_token" = "$token" ]; then
    write_adopted_record "$pid" "$current_token" "$started" >/dev/null
    exe=__ECHOMANAGER_ADOPTED__
    token=$current_token
  elif adopted=$(adopt_port_listener); then
    pid=$(sed -n '1p' "$meta"); token=$(sed -n '2p' "$meta"); exe=$(sed -n '3p' "$meta"); started=$(sed -n '4p' "$meta")
  else
    if kill -0 "$pid" 2>/dev/null; then printf '__EM_STATE__|stale|0|0|%%s\n' "$started"; else printf '__EM_STATE__|stopped|0|0|%%s\n' "$started"; fi
    exit 0
  fi
fi
now=$(date +%%s)
if port_listening; then state=running; actual=$port
elif [ $((now-started)) -lt %d ]; then state=starting; actual=0
else state=exception; actual=0; fi
	printf '__EM_STATE__|%%s|%%s|%%s|%%s\n' "$state" "$pid" "$actual" "$started"`, id, port, unixProcessIdentityHelpers, int(remoteStartTimeout.Seconds()))
}

func unixStopScript(instance Instance) string {
	id := shellQuote(instance.ID)
	port := strconv.Itoa(instance.ConfiguredPort)
	return fmt.Sprintf(`set -u
id=%s
port=%s
meta="$HOME/.echomanager/instances/$id.meta"
if [ ! -f "$meta" ]; then printf '__EM_STOPPED__\n'; exit 0; fi
%s
old_pid=$(sed -n '1p' "$meta"); old_token=$(sed -n '2p' "$meta"); old_exe=$(sed -n '3p' "$meta"); old_started=$(sed -n '4p' "$meta")
pid=$old_pid; token=$old_token; exe=$old_exe
if ! process_matches "$pid" "$exe" "$token"; then
  current_token=$(process_start_token "$pid")
  if kill -0 "$pid" 2>/dev/null && [ -n "$token" ] && [ "$current_token" = "$token" ]; then
    write_adopted_record "$pid" "$current_token" "$old_started" >/dev/null
    exe=__ECHOMANAGER_ADOPTED__
  elif adopted=$(adopt_port_listener); then
    pid=$(sed -n '1p' "$meta"); token=$(sed -n '2p' "$meta"); exe=$(sed -n '3p' "$meta")
  else
    if kill -0 "$pid" 2>/dev/null; then printf '__EM_STALE__\n'; exit 8; fi
    rm -f "$meta"; printf '__EM_STOPPED__\n'; exit 0
  fi
fi
kill -TERM "$pid" 2>/dev/null || true
i=0
while [ "$i" -lt 20 ] && process_matches "$pid" "$exe" "$token"; do sleep 0.25; i=$((i+1)); done
if process_matches "$pid" "$exe" "$token"; then kill -KILL "$pid" 2>/dev/null || true; fi
i=0
while [ "$i" -lt 8 ] && process_matches "$pid" "$exe" "$token"; do sleep 0.25; i=$((i+1)); done
if process_matches "$pid" "$exe" "$token"; then printf '__EM_ERROR__|进程未能退出'; exit 9; fi
rm -f "$meta"
	printf '__EM_STOPPED__\n'`, id, port, unixProcessIdentityHelpers)
}

const windowsRemoteAdoptionHelpers = `function Get-ManagerRawProcess($record) {
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
}`

func windowsStartScript(instance Instance) string {
	id := powershellQuote(instance.ID)
	executable := powershellQuote(instance.ExecutablePath)
	port := strconv.Itoa(instance.ConfiguredPort)
	environmentJSON, err := json.Marshal(instance.Environment)
	if err != nil {
		return "$ErrorActionPreference = 'Stop'; throw " + powershellQuote("无法编码远程环境变量："+err.Error())
	}
	launcherScript := windowsLauncherScript(instance, string(environmentJSON))
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
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
throw '独立进程启动器未能及时回报结果；请检查目标设备远程日志'`, id, executable, port, windowsRemoteAdoptionHelpers, powershellQuote(launcherScript))
}

func windowsLauncherScript(instance Instance, environmentJSON string) string {
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
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
}`, powershellQuote(instance.ID), powershellQuote(instance.ExecutablePath), powershellQuote(joinWindowsArguments(instance.Arguments)), powershellQuote(environmentJSON))
}

func decodePowerShellError(message string) string {
	message = strings.TrimSpace(message)
	start := strings.Index(message, "<Objs")
	if start < 0 {
		return strings.TrimSpace(strings.TrimPrefix(message, "#< CLIXML"))
	}

	decoder := xml.NewDecoder(strings.NewReader(message[start:]))
	var messages []string
	var current strings.Builder
	inError := false
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Local != "S" {
				continue
			}
			for _, attribute := range value.Attr {
				if attribute.Name.Local == "S" && attribute.Value == "Error" {
					inError = true
					current.Reset()
					break
				}
			}
		case xml.CharData:
			if inError {
				current.Write([]byte(value))
			}
		case xml.EndElement:
			if inError && value.Name.Local == "S" {
				text := decodePowerShellEscapes(current.String())
				text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
				text = strings.TrimSpace(text)
				if text != "" {
					messages = append(messages, text)
				}
				inError = false
			}
		}
	}
	if len(messages) == 0 {
		return strings.TrimSpace(strings.TrimPrefix(message, "#< CLIXML"))
	}
	return strings.Join(messages, "\n")
}

var powerShellEscapePattern = regexp.MustCompile(`(?i)_x([0-9a-f]{4})_`)

func decodePowerShellEscapes(value string) string {
	matches := powerShellEscapePattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value
	}
	var result strings.Builder
	position := 0
	for i := 0; i < len(matches); i++ {
		match := matches[i]
		result.WriteString(value[position:match[0]])
		codeUnit, err := strconv.ParseUint(value[match[2]:match[3]], 16, 16)
		if err != nil {
			result.WriteString(value[match[0]:match[1]])
			position = match[1]
			continue
		}
		unit := uint16(codeUnit)
		if 0xD800 <= unit && unit <= 0xDBFF && i+1 < len(matches) {
			next := matches[i+1]
			if next[0] == match[1] {
				low, parseErr := strconv.ParseUint(value[next[2]:next[3]], 16, 16)
				if parseErr == nil && 0xDC00 <= low && low <= 0xDFFF {
					result.WriteRune(utf16.DecodeRune(rune(unit), rune(low)))
					position = next[1]
					i++
					continue
				}
			}
		}
		result.WriteRune(utf16.Decode([]uint16{unit})[0])
		position = match[1]
	}
	result.WriteString(value[position:])
	return result.String()
}

func windowsStatusScript(instance Instance) string {
	id := powershellQuote(instance.ID)
	port := strconv.Itoa(instance.ConfiguredPort)
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$id = %s
$port = %s
$metaPath = Join-Path (Join-Path $env:USERPROFILE '.echomanager\instances') ($id + '.json')
function Get-ManagerRecord { if (Test-Path -LiteralPath $metaPath) { return (Get-Content -LiteralPath $metaPath -Raw | ConvertFrom-Json) }; return $null }
function Get-ManagerProcess($record) {
  if ($null -eq $record) { return $null }
  $process = Get-Process -Id ([int]$record.pid) -ErrorAction SilentlyContinue
  if ($null -eq $process) { return [pscustomobject]@{ kind = 'gone'; process = $null } }
  try {
    if ([string]$record.executable -ne '__ECHOMANAGER_ADOPTED__' -and [IO.Path]::GetFullPath($process.Path) -ine [IO.Path]::GetFullPath([string]$record.executable)) { return [pscustomobject]@{ kind = 'stale'; process = $null } }
    if ([long]$process.StartTime.ToUniversalTime().Ticks -ne [long]$record.startedTicks) { return [pscustomobject]@{ kind = 'stale'; process = $null } }
  } catch { return [pscustomobject]@{ kind = 'stale'; process = $null } }
  return [pscustomobject]@{ kind = 'active'; process = $process }
}
%s
$record = Get-ManagerRecord
if ($null -eq $record) { Write-Output '__EM_STATE__|stopped|0|0|'; exit 0 }
$check = Get-ManagerProcess $record
if ($check.kind -ne 'active') {
  $adopted = Adopt-ManagerProcess $record
  if ($null -ne $adopted) { $record = Get-ManagerRecord; $check = Get-ManagerProcess $record }
  if ($check.kind -eq 'gone') { Remove-Item -LiteralPath $metaPath -Force -ErrorAction SilentlyContinue; Write-Output ('__EM_STATE__|stopped|0|0|' + $record.startedAt); exit 0 }
  if ($check.kind -ne 'active') { Write-Output ('__EM_STATE__|stale|0|0|' + $record.startedAt); exit 0 }
}
$ready = $false
try {
  $listener = [Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpListeners() | Where-Object { $_.Port -eq $port } | Select-Object -First 1
  $ready = $null -ne $listener
} catch {}
if (-not $ready) {
  foreach ($address in @('127.0.0.1', '::1')) {
    $client = New-Object Net.Sockets.TcpClient
    try { $async = $client.BeginConnect($address, $port, $null, $null); if ($async.AsyncWaitHandle.WaitOne(500)) { $client.EndConnect($async); $ready = $true }; $async.AsyncWaitHandle.Close() } catch {} finally { $client.Close() }
    if ($ready) { break }
  }
}
$state = if ($ready) { 'running' } elseif (([DateTimeOffset]::UtcNow.ToUnixTimeSeconds() - [long]$record.startedAt) -lt %d) { 'starting' } else { 'exception' }
$actualPort = if ($ready) { $port } else { 0 }
	Write-Output ('__EM_STATE__|' + $state + '|' + $record.pid + '|' + $actualPort + '|' + $record.startedAt)`, id, port, windowsRemoteAdoptionHelpers, int(remoteStartTimeout.Seconds()))
}

func windowsStopScript(instance Instance) string {
	id := powershellQuote(instance.ID)
	port := strconv.Itoa(instance.ConfiguredPort)
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$id = %s
$port = %s
$metaPath = Join-Path (Join-Path $env:USERPROFILE '.echomanager\instances') ($id + '.json')
function Get-ManagerRecord { if (Test-Path -LiteralPath $metaPath) { return (Get-Content -LiteralPath $metaPath -Raw | ConvertFrom-Json) }; return $null }
function Get-ManagerProcess($record) {
  if ($null -eq $record) { return [pscustomobject]@{ kind = 'gone'; process = $null } }
  $process = Get-Process -Id ([int]$record.pid) -ErrorAction SilentlyContinue
  if ($null -eq $process) { return [pscustomobject]@{ kind = 'gone'; process = $null } }
  try {
    if ([string]$record.executable -ne '__ECHOMANAGER_ADOPTED__' -and [IO.Path]::GetFullPath($process.Path) -ine [IO.Path]::GetFullPath([string]$record.executable)) { return [pscustomobject]@{ kind = 'stale'; process = $null } }
    if ([long]$process.StartTime.ToUniversalTime().Ticks -ne [long]$record.startedTicks) { return [pscustomobject]@{ kind = 'stale'; process = $null } }
  } catch { return [pscustomobject]@{ kind = 'stale'; process = $null } }
  return [pscustomobject]@{ kind = 'active'; process = $process }
}
%s
$record = Get-ManagerRecord
if ($null -eq $record) { Write-Output '__EM_STOPPED__'; exit 0 }
$check = Get-ManagerProcess $record
if ($check.kind -ne 'active') {
  $adopted = Adopt-ManagerProcess $record
  if ($null -ne $adopted) {
    $record = Get-ManagerRecord
    $check = Get-ManagerProcess $record
  }
  if ($check.kind -eq 'stale') { Write-Output '__EM_STALE__'; exit 8 }
}
if ($check.kind -eq 'gone') { Remove-Item -LiteralPath $metaPath -Force; Write-Output '__EM_STOPPED__'; exit 0 }
$process = $check.process
try { [void]$process.CloseMainWindow() } catch {}
try { $process.WaitForExit(5000) } catch {}
$check = Get-ManagerProcess $record
if ($check.kind -eq 'stale') { Write-Output '__EM_STALE__'; exit 8 }
if ($check.kind -eq 'active') { Stop-Process -Id ([int]$record.pid) -Force; try { Wait-Process -Id ([int]$record.pid) -Timeout 5 -ErrorAction SilentlyContinue } catch {} }
Remove-Item -LiteralPath $metaPath -Force -ErrorAction SilentlyContinue
Write-Output '__EM_STOPPED__'`, id, port, windowsRemoteAdoptionHelpers)
}

func windowsLogsScript(instance Instance) string {
	id := powershellQuote(instance.ID)
	return fmt.Sprintf(`$ErrorActionPreference = 'SilentlyContinue'
$id = %s
$logDir = Join-Path (Join-Path $env:USERPROFILE '.echomanager') 'logs'
foreach ($suffix in @('.out.log', '.err.log')) {
  $path = Join-Path $logDir ($id + $suffix)
  if (Test-Path -LiteralPath $path) {
    Write-Output ('--- ' + $suffix + ' ---')
    $stream = [IO.File]::OpenRead($path)
    try {
      $length = [Math]::Min(262144, $stream.Length)
      [void]$stream.Seek(-$length, [IO.SeekOrigin]::End)
      $buffer = New-Object byte[] ([int]$length)
      [void]$stream.Read($buffer, 0, $buffer.Length)
      Write-Output ([Text.Encoding]::UTF8.GetString($buffer))
    } finally { $stream.Dispose() }
  }
}`, id)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func powershellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quoteWindowsArgument(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n\v\"") {
		return value
	}
	var result strings.Builder
	result.WriteByte('"')
	backslashes := 0
	for _, character := range value {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			result.WriteString(strings.Repeat("\\", backslashes*2+1))
			result.WriteRune(character)
			backslashes = 0
			continue
		}
		result.WriteString(strings.Repeat("\\", backslashes))
		backslashes = 0
		result.WriteRune(character)
	}
	result.WriteString(strings.Repeat("\\", backslashes*2))
	result.WriteByte('"')
	return result.String()
}

func joinWindowsArguments(arguments []string) string {
	quoted := make([]string, len(arguments))
	for i, argument := range arguments {
		quoted[i] = quoteWindowsArgument(argument)
	}
	return strings.Join(quoted, " ")
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func findRemoteMarker(output, prefix string) []string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	var found []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, prefix) {
			found = strings.Split(line, "|")
		}
	}
	return found
}

func parseRemoteState(output string) (remoteInstanceState, error) {
	parts := findRemoteMarker(output, "__EM_STATE__|")
	if len(parts) != 5 {
		return remoteInstanceState{}, fmt.Errorf("目标设备返回了无法识别的状态报告：%s", strings.TrimSpace(output))
	}
	pid, err := strconv.Atoi(parts[2])
	if err != nil {
		return remoteInstanceState{}, err
	}
	port, err := strconv.Atoi(parts[3])
	if err != nil {
		return remoteInstanceState{}, err
	}
	state := remoteInstanceState{Status: parts[1], PID: pid, ActualPort: port}
	if parts[4] != "" {
		if epoch, err := strconv.ParseInt(parts[4], 10, 64); err == nil && epoch > 0 {
			state.StartedAt = time.Unix(epoch, 0).UTC().Format(time.RFC3339)
		}
	}
	switch state.Status {
	case statusStopped, statusStarting, statusRunning, statusException, "stale":
		return state, nil
	default:
		return remoteInstanceState{}, fmt.Errorf("目标设备返回了未知状态：%s", state.Status)
	}
}

func (m *instanceManager) startRemote(id string) (resultErr error) {
	startedAt := time.Now()
	m.appendRemoteDebugLog(id, "START request received")
	log.Printf("Remote task startup diagnostics for %s: %s", id, m.remoteDebugLogPath(id))
	defer func() {
		if resultErr != nil {
			m.appendRemoteDebugLog(id, fmt.Sprintf("START failed elapsed=%s error=%q", time.Since(startedAt), resultErr.Error()))
		}
	}()

	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return apiError{status: 404, message: "找不到此实例。"}
	}
	instance := cloneInstance(m.instances[index])
	if instance.Status == statusStarting || instance.Status == statusRunning || instance.Status == statusStopping || instance.PID > 0 {
		m.mu.Unlock()
		return apiError{status: 409, message: "请先刷新目标设备状态；此实例可能仍在运行。"}
	}
	device, found := m.deviceLocked(instance.DeviceID)
	if !found {
		m.mu.Unlock()
		return apiError{status: 409, message: "目标设备不存在；请重新选择设备。"}
	}
	generation := m.nextRemoteActionGenerationLocked(id)
	m.instances[index].Status = statusStarting
	m.instances[index].Error = "正在通过远程管理通道启动…"
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	m.appendRemoteDebugLog(id, fmt.Sprintf(
		"START dispatch instance=%q deviceID=%q transport=%q address=%q port=%d previousStatus=%q previousPID=%d",
		instance.Name, device.ID, device.Transport, net.JoinHostPort(device.Address, strconv.Itoa(device.Port)), instance.ConfiguredPort, instance.Status, instance.PID,
	))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	commandStartedAt := time.Now()
	output, err := runRemoteCommand(ctx, device, remoteStartScript(device, instance))
	m.appendRemoteDebugLog(id, fmt.Sprintf(
		"START remote-command elapsed=%s error=%q output=%q",
		time.Since(commandStartedAt), errorString(err), strings.TrimSpace(output),
	))
	parts := findRemoteMarker(output, "__EM_STARTED__|")
	if len(parts) != 3 {
		if err != nil {
			if !m.setRemoteStartError(id, generation, err) {
				return nil
			}
			return apiError{status: 502, message: deviceError(err)}
		}
		err := fmt.Errorf("目标设备未返回启动确认：%s", strings.TrimSpace(output))
		if !m.setRemoteStartError(id, generation, err) {
			return nil
		}
		return apiError{status: 502, message: deviceError(err)}
	}
	if err != nil {
		m.appendRemoteDebugLog(id, fmt.Sprintf(
			"START valid confirmation marker received despite remote-command error=%q; treating launch as accepted",
			err.Error(),
		))
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil {
		if !m.setRemoteStartError(id, generation, err) {
			return nil
		}
		return apiError{status: 502, message: deviceError(err)}
	}
	epoch, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		if !m.setRemoteStartError(id, generation, err) {
			return nil
		}
		return apiError{status: 502, message: deviceError(err)}
	}
	m.mu.Lock()
	index = m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return apiError{status: 404, message: "启动确认后找不到此实例。"}
	}
	if !m.remoteActionIsCurrentLocked(id, generation) {
		current := cloneInstance(m.instances[index])
		m.mu.Unlock()
		m.appendRemoteDebugLog(id, fmt.Sprintf(
			"START late confirmation ignored because a newer remote action superseded it currentStatus=%q currentPID=%d generation=%d",
			current.Status, current.PID, generation,
		))
		return nil
	}
	if current := &m.instances[index]; current.Status == statusRunning && current.PID > 0 {
		currentPID := current.PID
		currentStartedAt := current.StartedAt
		m.nextRemoteActionGenerationLocked(id)
		m.mu.Unlock()
		m.appendRemoteDebugLog(id, fmt.Sprintf(
			"START preserved newer running state currentPID=%d currentStartedAt=%q markerPID=%d markerStartedAt=%q",
			currentPID, currentStartedAt, pid, time.Unix(epoch, 0).UTC().Format(time.RFC3339),
		))
		return nil
	}
	m.instances[index].Status = statusStarting
	m.instances[index].PID = pid
	m.instances[index].ActualPort = 0
	m.instances[index].StartedAt = time.Unix(epoch, 0).UTC().Format(time.RFC3339)
	m.instances[index].LastExitCode = nil
	m.instances[index].Error = "等待目标服务端口就绪…"
	m.nextRemoteActionGenerationLocked(id)
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	m.appendRemoteDebugLog(id, fmt.Sprintf(
		"START accepted pid=%d remoteStartedAt=%s localStatus=%q elapsed=%s",
		pid, time.Unix(epoch, 0).UTC().Format(time.RFC3339), statusStarting, time.Since(startedAt),
	))
	return nil
}

func (m *instanceManager) nextRemoteActionGenerationLocked(id string) uint64 {
	if m.remoteActionGeneration == nil {
		m.remoteActionGeneration = make(map[string]uint64)
	}
	m.remoteActionGeneration[id]++
	return m.remoteActionGeneration[id]
}

func (m *instanceManager) remoteActionIsCurrentLocked(id string, generation uint64) bool {
	return m.remoteActionGeneration[id] == generation
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (m *instanceManager) setRemoteStartError(id string, generation uint64, cause error) bool {
	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return false
	}
	if !m.remoteActionIsCurrentLocked(id, generation) {
		m.mu.Unlock()
		m.appendRemoteDebugLog(id, fmt.Sprintf(
			"START late failure ignored because a newer remote action superseded it generation=%d error=%q",
			generation, cause.Error(),
		))
		return false
	}
	current := &m.instances[index]
	if current.Status == statusRunning && current.PID > 0 {
		currentPID := current.PID
		m.nextRemoteActionGenerationLocked(id)
		m.mu.Unlock()
		m.appendRemoteDebugLog(id, fmt.Sprintf(
			"START late failure ignored because a refresh already confirmed running PID=%d error=%q",
			currentPID, cause.Error(),
		))
		return false
	}
	current.Status = statusException
	current.PID = 0
	current.ActualPort = 0
	current.Error = cause.Error()
	m.nextRemoteActionGenerationLocked(id)
	saveErr := m.saveLocked()
	m.mu.Unlock()
	if saveErr != nil {
		m.appendRemoteDebugLog(id, fmt.Sprintf("START failure state could not be persisted: %q", saveErr.Error()))
	}
	return true
}

func (m *instanceManager) stopRemote(id string) (resultErr error) {
	startedAt := time.Now()
	m.appendRemoteDebugLog(id, "STOP request received")
	defer func() {
		if resultErr != nil {
			m.appendRemoteDebugLog(id, fmt.Sprintf("STOP failed elapsed=%s error=%q", time.Since(startedAt), resultErr.Error()))
		}
	}()

	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return apiError{status: 404, message: "找不到此实例。"}
	}
	instance := cloneInstance(m.instances[index])
	if instance.PID <= 0 {
		m.mu.Unlock()
		return apiError{status: 409, message: "没有可核验的远程进程；为安全起见不会按端口停止程序。请先刷新状态。"}
	}
	device, found := m.deviceLocked(instance.DeviceID)
	if !found {
		m.mu.Unlock()
		return apiError{status: 409, message: "目标设备不存在。"}
	}
	generation := m.nextRemoteActionGenerationLocked(id)
	m.instances[index].Status = statusStopping
	m.instances[index].Error = "正在核验远程进程身份…"
	_ = m.saveLocked()
	m.mu.Unlock()

	m.appendRemoteDebugLog(id, fmt.Sprintf(
		"STOP dispatch deviceID=%q transport=%q address=%q pid=%d executable=%q",
		device.ID, device.Transport, net.JoinHostPort(device.Address, strconv.Itoa(device.Port)), instance.PID, instance.ExecutablePath,
	))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := runRemoteCommand(ctx, device, remoteStopScript(device, instance))
	m.appendRemoteDebugLog(id, fmt.Sprintf(
		"STOP remote-command error=%q output=%q",
		errorString(err), strings.TrimSpace(output),
	))
	if err != nil {
		if !m.setRemoteStopError(id, generation, err) {
			return nil
		}
		return apiError{status: 502, message: deviceError(err)}
	}
	if len(findRemoteMarker(output, "__EM_STALE__")) > 0 {
		err := errors.New("远端进程身份发生变化，且无法唯一确认配置端口的监听进程；为避免误停其他程序，未停止")
		if !m.setRemoteStopError(id, generation, err) {
			return nil
		}
		return apiError{status: 409, message: err.Error()}
	}
	if len(findRemoteMarker(output, "__EM_STOPPED__")) == 0 {
		err := fmt.Errorf("目标设备未确认停止：%s", strings.TrimSpace(output))
		if !m.setRemoteStopError(id, generation, err) {
			return nil
		}
		return apiError{status: 502, message: deviceError(err)}
	}
	m.mu.Lock()
	index = m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return nil
	}
	if !m.remoteActionIsCurrentLocked(id, generation) {
		current := cloneInstance(m.instances[index])
		m.mu.Unlock()
		m.appendRemoteDebugLog(id, fmt.Sprintf(
			"STOP late confirmation ignored because a newer remote action superseded it currentStatus=%q currentPID=%d generation=%d",
			current.Status, current.PID, generation,
		))
		return nil
	}
	m.nextRemoteActionGenerationLocked(id)
	m.instances[index].Status = statusStopped
	m.instances[index].PID = 0
	m.instances[index].ActualPort = 0
	m.instances[index].StartedAt = ""
	m.instances[index].Error = ""
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	m.appendRemoteDebugLog(id, fmt.Sprintf("STOP confirmed pid=%d elapsed=%s", instance.PID, time.Since(startedAt)))
	return nil
}

func (m *instanceManager) setRemoteStopError(id string, generation uint64, cause error) bool {
	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 || !m.remoteActionIsCurrentLocked(id, generation) {
		m.mu.Unlock()
		m.appendRemoteDebugLog(id, fmt.Sprintf(
			"STOP late failure ignored because a newer remote action superseded it generation=%d error=%q",
			generation, cause.Error(),
		))
		return false
	}
	if m.instances[index].Status == statusStopped && m.instances[index].PID == 0 {
		m.nextRemoteActionGenerationLocked(id)
		m.mu.Unlock()
		return false
	}
	m.instances[index].Status = statusException
	m.instances[index].Error = cause.Error()
	m.nextRemoteActionGenerationLocked(id)
	_ = m.saveLocked()
	m.mu.Unlock()
	return true
}

func (m *instanceManager) refreshRemoteStates(ctx context.Context, onlyInstance string) error {
	type target struct {
		instance   Instance
		device     Device
		generation uint64
	}
	m.mu.Lock()
	targets := make([]target, 0)
	for _, instance := range m.instances {
		if instance.DeviceID == "" || (onlyInstance != "" && instance.ID != onlyInstance) {
			continue
		}
		if device, ok := m.deviceLocked(instance.DeviceID); ok {
			targets = append(targets, target{
				instance:   cloneInstance(instance),
				device:     device,
				generation: m.remoteActionGeneration[instance.ID],
			})
		}
	}
	m.mu.Unlock()
	for _, item := range targets {
		if err := ctx.Err(); err != nil {
			m.appendRemoteDebugLog(item.instance.ID, fmt.Sprintf("STATUS probe aborted before command: %q", err.Error()))
			return err
		}
		probeStartedAt := time.Now()
		m.appendRemoteDebugLog(item.instance.ID, fmt.Sprintf(
			"STATUS probe begin deviceID=%q transport=%q address=%q port=%d localStatus=%q localPID=%d localActualPort=%d",
			item.device.ID, item.device.Transport, net.JoinHostPort(item.device.Address, strconv.Itoa(item.device.Port)),
			item.instance.ConfiguredPort, item.instance.Status, item.instance.PID, item.instance.ActualPort,
		))
		commandCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		commandStartedAt := time.Now()
		output, err := runRemoteCommand(commandCtx, item.device, remoteStatusScript(item.device, item.instance))
		cancel()
		m.appendRemoteDebugLog(item.instance.ID, fmt.Sprintf(
			"STATUS remote-command elapsed=%s error=%q output=%q",
			time.Since(commandStartedAt), errorString(err), strings.TrimSpace(output),
		))
		if err != nil {
			m.setDeviceProbeResult(item.device.ID, err)
			m.setRemoteStateError(item.instance.ID, err)
			m.appendRemoteDebugLog(item.instance.ID, fmt.Sprintf(
				"STATUS probe failed elapsed=%s error=%q localStateUpdated=error",
				time.Since(probeStartedAt), err.Error(),
			))
			continue
		}
		remoteState, err := parseRemoteState(output)
		if err != nil {
			m.setDeviceProbeResult(item.device.ID, err)
			m.setRemoteStateError(item.instance.ID, err)
			m.appendRemoteDebugLog(item.instance.ID, fmt.Sprintf(
				"STATUS parse failed elapsed=%s error=%q output=%q",
				time.Since(probeStartedAt), err.Error(), strings.TrimSpace(output),
			))
			continue
		}
		if remoteState.PID > 0 && remoteState.PID != item.instance.PID {
			m.appendRemoteDebugLog(item.instance.ID, fmt.Sprintf(
				"STATUS adopted remote process oldPID=%d newPID=%d port=%d",
				item.instance.PID, remoteState.PID, remoteState.ActualPort,
			))
		}
		m.setDeviceProbeResult(item.device.ID, nil)
		m.mu.Lock()
		var localAfter Instance
		applied := false
		staleResult := false
		var saveErr error
		if index := m.indexLocked(item.instance.ID); index >= 0 && m.instances[index].DeviceID == item.device.ID {
			if !m.remoteActionIsCurrentLocked(item.instance.ID, item.generation) {
				localAfter = cloneInstance(m.instances[index])
				staleResult = true
			} else {
				switch remoteState.Status {
				case "stale":
					m.instances[index].Status = statusException
					m.instances[index].PID = 0
					m.instances[index].ActualPort = 0
					m.instances[index].Error = "远端进程身份已变化，且无法唯一确认配置端口的监听进程；未接管。请检查目标设备的端口占用。"
				case statusStopped:
					m.instances[index].Status = statusStopped
					m.instances[index].PID = 0
					m.instances[index].ActualPort = 0
					m.instances[index].StartedAt = ""
					m.instances[index].Error = ""
				default:
					m.instances[index].Status = remoteState.Status
					m.instances[index].PID = remoteState.PID
					m.instances[index].ActualPort = remoteState.ActualPort
					m.instances[index].StartedAt = remoteState.StartedAt
					m.instances[index].Error = ""
					if remoteState.Status == statusException {
						m.instances[index].Error = fmt.Sprintf("远端进程仍在运行，但端口 %d 尚未就绪。", m.instances[index].ConfiguredPort)
					}
				}
				saveErr = m.saveLocked()
				localAfter = cloneInstance(m.instances[index])
				applied = true
			}
		}
		m.mu.Unlock()
		if staleResult {
			m.appendRemoteDebugLog(item.instance.ID, fmt.Sprintf(
				"STATUS result discarded because a newer remote action superseded it generation=%d remoteStatus=%q remotePID=%d",
				item.generation, remoteState.Status, remoteState.PID,
			))
		}
		m.appendRemoteDebugLog(item.instance.ID, fmt.Sprintf(
			"STATUS applied=%t remoteStatus=%q remotePID=%d remoteActualPort=%d remoteStartedAt=%q localStatus=%q localPID=%d localActualPort=%d localError=%q elapsed=%s persistError=%q",
			applied, remoteState.Status, remoteState.PID, remoteState.ActualPort, remoteState.StartedAt,
			localAfter.Status, localAfter.PID, localAfter.ActualPort, localAfter.Error, time.Since(probeStartedAt), errorString(saveErr),
		))
	}
	return nil
}

func (m *instanceManager) setRemoteStateError(id string, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index := m.indexLocked(id); index >= 0 {
		instance := &m.instances[index]
		instance.Error = "无法连接或核验目标设备：" + cause.Error()
		if instance.PID <= 0 || (instance.Status != statusStarting && instance.Status != statusRunning && instance.Status != statusStopping) {
			instance.Status = statusException
		}
		_ = m.saveLocked()
	}
}
