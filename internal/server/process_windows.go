//go:build windows

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const createNoWindow = 0x08000000

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func terminateManagedProcess(command *exec.Cmd, force bool) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	return terminateManagedPID(command.Process.Pid, force)
}

func terminateManagedPID(pid int, force bool) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	args := []string{"/PID", fmt.Sprint(pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	if err := runTaskkill(args); err != nil {
		if running, checkErr := windowsProcessRunning(pid); checkErr == nil && !running {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func windowsProcessRunning(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if code, ok := err.(syscall.Errno); ok && code == syscall.Errno(87) {
			return false, nil
		}
		return false, err
	}
	defer syscall.CloseHandle(handle)

	state, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	switch state {
	case syscall.WAIT_OBJECT_0:
		return false, nil
	case syscall.WAIT_TIMEOUT:
		return true, nil
	default:
		return false, fmt.Errorf("unexpected process wait state: %d", state)
	}
}

func runTaskkill(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "taskkill", args...)
	configureCommand(command)
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if message := strings.TrimSpace(decodeWindowsCommandOutput(output)); message != "" {
			return fmt.Errorf("taskkill 失败：%s: %w", message, err)
		}
		return err
	}
	return nil
}

func discoverPortListeners(ports []int) (map[int][]processIdentity, error) {
	result := make(map[int][]processIdentity, len(ports))
	for _, port := range ports {
		result[port] = []processIdentity{}
	}
	if len(ports) == 0 {
		return result, nil
	}
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, fmt.Sprint(port))
	}
	script := `$ports = @(` + strings.Join(values, ",") + `); $items = @(Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $ports -contains $_.LocalPort } | ForEach-Object { $connection = $_; $runtime = Get-Process -Id $connection.OwningProcess -ErrorAction SilentlyContinue; $process = Get-CimInstance Win32_Process -Filter ("ProcessId=" + $connection.OwningProcess) -ErrorAction SilentlyContinue; $startedAt = ''; $startToken = ''; $executable = ''; if ($null -ne $runtime) { $startedAt = $runtime.StartTime.ToUniversalTime().ToString('o'); $startToken = [string]$runtime.StartTime.ToUniversalTime().Ticks }; if ($null -ne $process) { $executable = [string]$process.ExecutablePath }; [pscustomobject]@{ port = [int]$connection.LocalPort; pid = [int]$connection.OwningProcess; executable = $executable; startToken = $startToken; startedAt = $startedAt } }); if ($items.Count -eq 0) { '[]' } else { ConvertTo-Json -InputObject $items -Compress }`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	configureCommand(command)
	output, err := command.Output()
	if err != nil {
		return result, fmt.Errorf("inspect Windows port listeners: %w", err)
	}
	type portProcess struct {
		Port int `json:"port"`
		processIdentity
	}
	var items []portProcess
	if err := json.Unmarshal(output, &items); err != nil {
		var single portProcess
		if singleErr := json.Unmarshal(output, &single); singleErr != nil {
			return result, fmt.Errorf("decode Windows process list: %w", err)
		}
		items = []portProcess{single}
	}
	for _, item := range items {
		if _, knownPort := result[item.Port]; knownPort {
			result[item.Port] = appendUniqueIdentity(result[item.Port], item.processIdentity)
		}
	}
	return result, nil
}

func inspectProcessIdentity(pid int) (processIdentity, bool, error) {
	identities, err := inspectProcessIdentities([]int{pid})
	if err != nil {
		return processIdentity{}, false, err
	}
	identity, exists := identities[pid]
	return identity, exists, nil
}

func inspectProcessIdentities(pids []int) (map[int]processIdentity, error) {
	result := make(map[int]processIdentity, len(pids))
	if len(pids) == 0 {
		return result, nil
	}
	values := make([]string, 0, len(pids))
	for _, pid := range pids {
		if pid > 0 {
			values = append(values, fmt.Sprint(pid))
		}
	}
	if len(values) == 0 {
		return result, nil
	}
	script := `$pids = @(` + strings.Join(values, ",") + `); $items = @(foreach ($id in $pids) { $runtime = Get-Process -Id $id -ErrorAction SilentlyContinue; if ($null -eq $runtime) { continue }; $process = Get-CimInstance Win32_Process -Filter ("ProcessId=" + $id) -ErrorAction SilentlyContinue; if ($null -ne $process) { [pscustomobject]@{ pid = [int]$runtime.Id; executable = [string]$process.ExecutablePath; startToken = [string]$runtime.StartTime.ToUniversalTime().Ticks; startedAt = $runtime.StartTime.ToUniversalTime().ToString('o') } } }); if ($items.Count -eq 0) { '[]' } else { ConvertTo-Json -InputObject $items -Compress }`
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	configureCommand(command)
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, err
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return result, nil
	}
	var identities []processIdentity
	if err := json.Unmarshal(output, &identities); err != nil {
		var identity processIdentity
		if singleErr := json.Unmarshal(output, &identity); singleErr != nil {
			return result, err
		}
		identities = []processIdentity{identity}
	}
	for _, identity := range identities {
		result[identity.PID] = identity
	}
	return result, nil
}

func processExecutableMatches(configured, actual string) bool {
	return strings.EqualFold(filepath.Clean(configured), filepath.Clean(actual))
}

func executablePermission(_ string, _ os.FileInfo) error { return nil }

func replaceFile(source, destination string) error {
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	moveFile := syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")
	result, _, callErr := moveFile.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0x1|0x8)
	if result == 0 {
		return callErr
	}
	return nil
}
