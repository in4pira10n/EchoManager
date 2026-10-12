//go:build !windows

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var processIDPattern = regexp.MustCompile(`pid=([0-9]+)`)

func terminateManagedPID(pid int, force bool) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func discoverPortListeners(ports []int) (map[int][]processIdentity, error) {
	result := make(map[int][]processIdentity, len(ports))
	requested := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		result[port] = []processIdentity{}
		requested[port] = struct{}{}
	}
	if len(ports) == 0 {
		return result, nil
	}
	if runtime.GOOS == "linux" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "ss", "-H", "-ltnp").Output()
		if err != nil {
			return discoverPortListenersWithLsof(ports, result, requested)
		}
		pids := make(map[int]processIdentity)
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			port, ok := socketPort(fields[3])
			if !ok {
				continue
			}
			if _, wanted := requested[port]; !wanted {
				continue
			}
			matches := processIDPattern.FindAllStringSubmatch(line, -1)
			if len(matches) == 0 {
				result[port] = appendUniqueIdentity(result[port], processIdentity{})
				continue
			}
			for _, match := range matches {
				pid, err := strconv.Atoi(match[1])
				if err != nil {
					continue
				}
				identity, exists := pids[pid]
				if !exists {
					identity, _, _ = inspectProcessIdentity(pid)
					pids[pid] = identity
				}
				if identity.PID == 0 {
					identity.PID = pid
				}
				result[port] = appendUniqueIdentity(result[port], identity)
			}
		}
		return result, nil
	}
	return discoverPortListenersWithLsof(ports, result, requested)
}

func discoverPortListenersWithLsof(ports []int, result map[int][]processIdentity, requested map[int]struct{}) (map[int][]processIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpcn").Output()
	if err != nil {
		return result, fmt.Errorf("inspect local port listeners: %w", err)
	}
	identities := make(map[int]processIdentity)
	pid := 0
	for _, field := range strings.Split(string(output), "\n") {
		if len(field) < 2 {
			continue
		}
		switch field[0] {
		case 'p':
			pid, _ = strconv.Atoi(field[1:])
		case 'n':
			port, ok := socketPort(field[1:])
			if !ok {
				continue
			}
			if _, wanted := requested[port]; !wanted {
				continue
			}
			identity, exists := identities[pid]
			if !exists && pid > 0 {
				identity, _, _ = inspectProcessIdentity(pid)
				identities[pid] = identity
			}
			if identity.PID == 0 && pid > 0 {
				identity.PID = pid
			}
			result[port] = appendUniqueIdentity(result[port], identity)
		}
	}
	return result, nil
}

func socketPort(address string) (int, bool) {
	address = strings.TrimSuffix(address, "]")
	separator := strings.LastIndexByte(address, ':')
	if separator < 0 || separator == len(address)-1 {
		return 0, false
	}
	port, err := strconv.Atoi(address[separator+1:])
	return port, err == nil
}

func inspectProcessIdentity(pid int) (processIdentity, bool, error) {
	if pid <= 0 {
		return processIdentity{}, false, nil
	}
	if runtime.GOOS == "linux" {
		return inspectLinuxProcess(pid)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return processIdentity{}, false, nil
		}
		if !errors.Is(err, syscall.EPERM) {
			return processIdentity{}, false, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stateOutput, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		return processIdentity{}, false, nil
	}
	if strings.HasPrefix(strings.TrimSpace(string(stateOutput)), "Z") {
		return processIdentity{}, false, nil
	}
	output, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "txt", "-Fn").Output()
	if err != nil {
		return processIdentity{}, false, err
	}
	executable := ""
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "n/") {
			executable = strings.TrimPrefix(line, "n")
			break
		}
	}
	startOutput, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return processIdentity{}, false, err
	}
	startedAt := strings.TrimSpace(string(startOutput))
	if executable == "" || startedAt == "" {
		return processIdentity{}, false, nil
	}
	return processIdentity{PID: pid, Executable: filepath.Clean(executable), StartToken: startedAt, StartedAt: startedAt}, true, nil
}

func inspectProcessIdentities(pids []int) (map[int]processIdentity, error) {
	result := make(map[int]processIdentity, len(pids))
	for _, pid := range pids {
		identity, exists, err := inspectProcessIdentity(pid)
		if err != nil {
			return result, err
		}
		if exists {
			result[pid] = identity
		}
	}
	return result, nil
}

func inspectLinuxProcess(pid int) (processIdentity, bool, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(root, "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return processIdentity{}, false, nil
	}
	if err != nil {
		return processIdentity{}, false, err
	}
	closingParen := strings.LastIndexByte(string(stat), ')')
	if closingParen < 0 {
		return processIdentity{}, false, errors.New("invalid Linux process stat")
	}
	fields := strings.Fields(string(stat)[closingParen+1:])
	if len(fields) <= 19 {
		return processIdentity{}, false, errors.New("incomplete Linux process stat")
	}
	if fields[0] == "Z" {
		return processIdentity{}, false, nil
	}
	executable, err := os.Readlink(filepath.Join(root, "exe"))
	if errors.Is(err, os.ErrNotExist) {
		return processIdentity{}, false, nil
	}
	if err != nil {
		return processIdentity{}, false, err
	}
	startToken := fields[19]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	startOutput, _ := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	return processIdentity{
		PID: pid, Executable: strings.TrimSuffix(filepath.Clean(executable), " (deleted)"),
		StartToken: startToken, StartedAt: strings.TrimSpace(string(startOutput)),
	}, true, nil
}

func processExecutableMatches(configured, actual string) bool {
	return filepath.Clean(configured) == filepath.Clean(actual)
}
