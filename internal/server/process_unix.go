//go:build !windows

package server

import (
	"os"
	"os/exec"
	"syscall"
)

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateManagedProcess(command *exec.Cmd, force bool) error {
	if command == nil || command.Process == nil {
		return os.ErrProcessDone
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(-command.Process.Pid, signal)
	if err == syscall.ESRCH {
		return os.ErrProcessDone
	}
	return err
}

func executablePermission(path string, info os.FileInfo) error {
	if info.Mode()&0o111 == 0 {
		return os.ErrPermission
	}
	return nil
}

func replaceFile(source, destination string) error { return os.Rename(source, destination) }
