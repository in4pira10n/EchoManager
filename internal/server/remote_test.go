package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWindowsStartScriptAllowsNoArguments(t *testing.T) {
	instance := Instance{
		ID:             "instance-1",
		ExecutablePath: `C:\Apps\EchoGallery\EchoGallery.exe`,
	}
	script := windowsStartScript(instance)
	launcher := windowsLauncherScript(instance, `{}`)

	if !strings.Contains(launcher, "if ($arguments.Length -gt 0) { $startOptions.ArgumentList = $arguments }") {
		t.Fatal("start script must only pass ArgumentList when arguments are present")
	}
	if !strings.Contains(launcher, "$process = Start-Process @startOptions") {
		t.Fatal("detached launcher must invoke Start-Process with the optional argument list")
	}
	if strings.Contains(launcher, "Start-Process -FilePath $exe -ArgumentList $arguments") {
		t.Fatal("start script still unconditionally passes an empty ArgumentList")
	}
	if !strings.Contains(script, `[WMIClass]'\\.\root\cimv2:Win32_Process'`) || !strings.Contains(script, "$processClass.Create($commandLine,") {
		t.Fatal("WinRM start script must create the launcher outside its remote command job")
	}
}

func TestWindowsStartScriptKeepsConfiguredArguments(t *testing.T) {
	instance := Instance{
		ID:             "instance-1",
		ExecutablePath: `C:\Apps\EchoGallery\EchoGallery.exe`,
		Arguments:      []string{"--config", `C:\Data Files\app.json`},
	}
	script := windowsLauncherScript(instance, `{}`)

	if !strings.Contains(script, `$arguments = '--config "C:\Data Files\app.json"'`) {
		t.Fatalf("configured arguments were not preserved in start script:\n%s", script)
	}
}

func TestWinRMScriptStdinKeepsLongScriptOutOfCommandLine(t *testing.T) {
	script := strings.Repeat("Write-Output '启动任务';\n", 5000)
	bootstrap, payload := winRMScriptStdin(script)
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode stdin payload: %v", err)
	}
	if string(decoded) != script {
		t.Fatal("stdin payload did not preserve the original PowerShell script")
	}
	if len(bootstrap) > 512 {
		t.Fatalf("WinRM command bootstrap unexpectedly grew to %d bytes", len(bootstrap))
	}
	if len(payload) <= 8191 {
		t.Fatal("test payload should exceed the typical Windows command-line limit")
	}
	if strings.Contains(bootstrap, payload) || !strings.Contains(bootstrap, "[Console]::In.ReadToEnd()") {
		t.Fatal("long script must be read from stdin rather than embedded in the WinRM command")
	}
}

func TestDecodePowerShellErrorExtractsReadableCLIXML(t *testing.T) {
	input := `#< CLIXML
<Objs Version="1.1.0.1" xmlns="http://schemas.microsoft.com/powershell/2004/04"><S S="Error">Start-Process : Cannot validate argument on parameter 'ArgumentList'._x000D__x000A_The argument is null or empty.</S></Objs>`
	want := "Start-Process : Cannot validate argument on parameter 'ArgumentList'.\nThe argument is null or empty."
	if got := decodePowerShellError(input); got != want {
		t.Fatalf("decodePowerShellError() = %q, want %q", got, want)
	}
}

func TestDecodePowerShellEscapesHandlesUnicodeSurrogatePairs(t *testing.T) {
	if got, want := decodePowerShellEscapes("ready _xD83D__xDE80_"), "ready 🚀"; got != want {
		t.Fatalf("decodePowerShellEscapes() = %q, want %q", got, want)
	}
}

func TestRemoteStatusScriptsDetectNonIPv4LoopbackListeners(t *testing.T) {
	instance := Instance{ID: "instance-1", ConfiguredPort: 8080}
	unixScript := unixStatusScript(instance)
	for _, expected := range []string{"ss -H -ltn", "lsof -nP -iTCP:", "netstat -an", "/dev/tcp/::1/$port", "-lt 60"} {
		if !strings.Contains(unixScript, expected) {
			t.Fatalf("Unix status script is missing listener probe %q", expected)
		}
	}
	windowsScript := windowsStatusScript(instance)
	for _, expected := range []string{"GetActiveTcpListeners()", "'::1'", "-lt 60"} {
		if !strings.Contains(windowsScript, expected) {
			t.Fatalf("Windows status script is missing listener probe %q", expected)
		}
	}
}

func TestRemoteScriptsAdoptChangedProcessIdentityWithPortOwnershipCheck(t *testing.T) {
	instance := Instance{ID: "instance-1", ExecutablePath: "/opt/example/server", ConfiguredPort: 8080}
	for name, script := range map[string]string{
		"unix start":  unixStartScript(instance),
		"unix status": unixStatusScript(instance),
		"unix stop":   unixStopScript(instance),
	} {
		if !strings.Contains(script, "__ECHOMANAGER_ADOPTED__") || !strings.Contains(script, "adopt_port_listener") || !strings.Contains(script, `[ "$#" -eq 1 ]`) {
			t.Errorf("%s does not revalidate and adopt the unique port owner", name)
		}
	}
	for name, script := range map[string]string{
		"Windows start":  windowsStartScript(instance),
		"Windows status": windowsStatusScript(instance),
		"Windows stop":   windowsStopScript(instance),
	} {
		if !strings.Contains(script, "Adopt-ManagerProcess") || !strings.Contains(script, "Get-NetTCPConnection") || !strings.Contains(script, "netstat.exe") || !strings.Contains(script, "-ano -p tcp") || !strings.Contains(script, "Get-PortListenerOwnerIds") || !strings.Contains(script, "$owners.Count -ne 1") || !strings.Contains(script, "record.executable -ne '__ECHOMANAGER_ADOPTED__'") || !strings.Contains(script, "Set-ManagerRecordProperty") || !strings.Contains(script, "Add-Member -MemberType NoteProperty") || strings.Contains(script, "$record.executable =") {
			t.Errorf("%s does not revalidate and adopt the unique port owner", name)
		}
	}
	if !strings.Contains(windowsStartScript(instance), "已尝试 Get-NetTCPConnection 和 netstat") {
		t.Fatal("Windows start error should report both listener discovery methods")
	}
}

func TestRemoteScriptsAdoptUniquePortOwnerWithoutPreviousMetadata(t *testing.T) {
	instance := Instance{ID: "instance-1", ExecutablePath: `C:\Apps\EchoGallery\EchoGallery.exe`, ConfiguredPort: 8080}
	for name, script := range map[string]string{
		"Unix start":  unixStartScript(instance),
		"Unix status": unixStatusScript(instance),
		"Unix stop":   unixStopScript(instance),
	} {
		if !strings.Contains(script, `if adopted=$(adopt_port_listener); then`) {
			t.Errorf("%s does not attempt port-owner adoption when metadata is absent", name)
		}
	}
	for name, script := range map[string]string{
		"Windows start":  windowsStartScript(instance),
		"Windows status": windowsStatusScript(instance),
		"Windows stop":   windowsStopScript(instance),
	} {
		if !strings.Contains(script, "Adopt-ManagerProcess ([pscustomobject]@{})") {
			t.Errorf("%s does not attempt port-owner adoption when metadata is absent", name)
		}
	}
}

func TestRemoteStopConfirmationTakesPrecedenceOverSSHExitError(t *testing.T) {
	commandErr := errors.New("SSH 远程命令失败（退出代码 255）：unexpected EOF")
	confirmed, stale, resultErr := remoteStopResult("diagnostic output\n__EM_STOPPED__\n", commandErr)
	if !confirmed || stale || resultErr != nil {
		t.Fatalf("explicit stopped marker should win over a late SSH error: confirmed=%t stale=%t err=%v", confirmed, stale, resultErr)
	}

	confirmed, stale, resultErr = remoteStopResult("__EM_STOPPED__|unexpected\n", commandErr)
	if confirmed || stale || resultErr == nil || !strings.Contains(resultErr.Error(), "255") || !strings.Contains(resultErr.Error(), "停止确认") {
		t.Fatalf("missing exact confirmation should preserve the SSH error code: confirmed=%t stale=%t err=%v", confirmed, stale, resultErr)
	}

	confirmed, stale, resultErr = remoteStopResult("__EM_STALE__\n__EM_STOPPED__\n", commandErr)
	if confirmed || !stale || resultErr == nil {
		t.Fatalf("stale identity must take precedence over stopped marker: confirmed=%t stale=%t err=%v", confirmed, stale, resultErr)
	}
}

func TestUnixRemoteScriptsPassShellSyntaxCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell")
	}
	instance := Instance{ID: "instance-1", ExecutablePath: "/opt/example/server", ConfiguredPort: 8080}
	for name, script := range map[string]string{
		"start":  unixStartScript(instance),
		"status": unixStatusScript(instance),
		"stop":   unixStopScript(instance),
	} {
		command := exec.Command("sh", "-n")
		command.Stdin = strings.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("%s script has invalid shell syntax: %v: %s", name, err, output)
		}
	}
}

func TestRemoteStateErrorPreservesLastKnownActiveProcess(t *testing.T) {
	for _, status := range []string{statusStarting, statusRunning, statusStopping} {
		t.Run(status, func(t *testing.T) {
			instance := Instance{ID: "instance-1", Status: status, PID: 42}
			manager := &instanceManager{
				statePath: filepath.Join(t.TempDir(), "instances.json"),
				instances: []Instance{instance},
			}

			manager.setRemoteStateError(instance.ID, errors.New("temporary transport error"))

			updated := manager.instances[0]
			if updated.Status != status || updated.PID != instance.PID {
				t.Fatalf("transient status query changed active instance: status=%q pid=%d", updated.Status, updated.PID)
			}
			if !strings.Contains(updated.Error, "temporary transport error") {
				t.Fatalf("transient query error was not retained: %q", updated.Error)
			}
		})
	}
}

func TestLateRemoteStartErrorPreservesConfirmedRunningState(t *testing.T) {
	startedAt := time.Date(2026, time.October, 11, 12, 30, 0, 0, time.UTC).Format(time.RFC3339)
	instance := Instance{
		ID:         "instance-1",
		Status:     statusRunning,
		PID:        2448,
		ActualPort: 8080,
		StartedAt:  startedAt,
	}
	statePath := filepath.Join(t.TempDir(), "instances.json")
	manager := &instanceManager{
		statePath:              statePath,
		logDir:                 filepath.Dir(statePath),
		instances:              []Instance{instance},
		remoteActionGeneration: map[string]uint64{instance.ID: 1},
	}
	if err := manager.saveLocked(); err != nil {
		t.Fatalf("save initial running state: %v", err)
	}

	manager.setRemoteStartError(instance.ID, 1, errors.New("context deadline exceeded"))

	updated := manager.instances[0]
	if updated.Status != statusRunning || updated.PID != instance.PID || updated.ActualPort != instance.ActualPort || updated.StartedAt != startedAt {
		t.Fatalf("late start error changed confirmed running state: %+v", updated)
	}
	if updated.Error != instance.Error {
		t.Fatalf("late start error replaced the current error state: got %q, want %q", updated.Error, instance.Error)
	}

	content, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read persisted manager state: %v", err)
	}
	var saved persistedState
	if err := json.Unmarshal(content, &saved); err != nil {
		t.Fatalf("decode persisted manager state: %v", err)
	}
	if len(saved.Instances) != 1 {
		t.Fatalf("persisted instance count = %d, want 1", len(saved.Instances))
	}
	persisted := saved.Instances[0].Instance
	if persisted.Status != statusRunning || persisted.PID != instance.PID || persisted.ActualPort != instance.ActualPort || persisted.StartedAt != startedAt {
		t.Fatalf("late start error persisted an altered state: %+v", persisted)
	}
}

func TestStaleRemoteStartErrorCannotOverwriteNewerStop(t *testing.T) {
	instance := Instance{ID: "instance-1", Status: statusStopped}
	statePath := filepath.Join(t.TempDir(), "instances.json")
	manager := &instanceManager{
		statePath:              statePath,
		logDir:                 filepath.Dir(statePath),
		instances:              []Instance{instance},
		remoteActionGeneration: map[string]uint64{instance.ID: 2},
	}
	if err := manager.saveLocked(); err != nil {
		t.Fatalf("save initial stopped state: %v", err)
	}

	if applied := manager.setRemoteStartError(instance.ID, 1, errors.New("late start timeout")); applied {
		t.Fatal("stale start failure should not mutate state after a newer stop")
	}
	if got := manager.instances[0].Status; got != statusStopped {
		t.Fatalf("late start failure changed state to %q, want %q", got, statusStopped)
	}
}
