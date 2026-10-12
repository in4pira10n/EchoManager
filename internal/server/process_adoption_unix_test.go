//go:build !windows

package server

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestExternalProcessListenerHelper(t *testing.T) {
	if os.Getenv("ECHOMANAGER_PROCESS_HELPER") != "1" {
		return
	}
	port, err := strconv.Atoi(os.Getenv("ECHOMANAGER_PROCESS_HELPER_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for {
		time.Sleep(time.Hour)
	}
}

func TestRefreshAdoptsAndSafelyStopsExternalLocalProcess(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		if _, ssErr := exec.LookPath("ss"); ssErr != nil {
			t.Skip("requires lsof or ss to inspect local listeners")
		}
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestExternalProcessListenerHelper$")
	command.Env = append(os.Environ(), "ECHOMANAGER_PROCESS_HELPER=1", "ECHOMANAGER_PROCESS_HELPER_PORT="+strconv.Itoa(port))
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !portAccepting(port) && time.Now().Before(deadline) {
		time.Sleep(40 * time.Millisecond)
	}
	if !portAccepting(port) {
		t.Fatal("external helper did not start listening")
	}

	manager, err := newInstanceManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instance, err := manager.create(Instance{
		Name: "external helper", ExecutablePath: filepath.Clean(executable), ConfiguredPort: port,
		Arguments: []string{"-test.run=^TestExternalProcessListenerHelper$"},
		Environment: map[string]string{
			"ECHOMANAGER_PROCESS_HELPER":      "1",
			"ECHOMANAGER_PROCESS_HELPER_PORT": strconv.Itoa(port),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.refreshLocalStates()
	adopted, ok := manager.instanceByID(instance.ID)
	if !ok || adopted.Status != statusRunning || adopted.PID != command.Process.Pid || adopted.ProcessStartToken == "" {
		t.Fatalf("external process was not safely adopted: %+v", adopted)
	}
	if err := manager.stop(instance.ID); err != nil {
		t.Fatalf("stop adopted process: %v", err)
	}
	stopped, _ := manager.instanceByID(instance.ID)
	if stopped.Status != statusStopped || stopped.PID != 0 {
		t.Fatalf("adopted process state after stop = %+v", stopped)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("externally started helper unexpectedly exited successfully")
	}
	if portAccepting(port) {
		t.Fatalf("port %d is still accepting connections after stop", port)
	}
}
