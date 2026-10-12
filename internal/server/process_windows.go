//go:build windows

package server

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

func configureCommand(command *exec.Cmd) {}

func terminateManagedProcess(command *exec.Cmd, force bool) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	args := []string{"/PID", fmt.Sprint(command.Process.Pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	return exec.Command("taskkill", args...).Run()
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
