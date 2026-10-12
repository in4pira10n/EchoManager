package server

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const stopFailureLogName = "EchoManager-stop-failures.log"

var stopFailureLogMu sync.Mutex

type stopFailureDiagnostic struct {
	OccurredAt  time.Time
	RequestID   string
	Instance    Instance
	StatusAfter string
	Device      Device
	Error       string
}

func (m *instanceManager) appendStopFailureLog(requestID string, attempted Instance, cause error) {
	if cause == nil {
		return
	}

	diagnostic := stopFailureDiagnostic{
		OccurredAt: time.Now(),
		RequestID:  requestID,
		Instance:   attempted,
		Error:      cause.Error(),
	}
	m.mu.Lock()
	if index := m.indexLocked(requestID); index >= 0 {
		diagnostic.Instance = cloneInstance(m.instances[index])
		diagnostic.StatusAfter = m.instances[index].Status
	}
	if diagnostic.Instance.DeviceID != "" {
		diagnostic.Device, _ = m.deviceLocked(diagnostic.Instance.DeviceID)
	}
	m.mu.Unlock()

	executable, err := os.Executable()
	if err != nil {
		log.Printf("could not locate program directory for stop-failure log: %v", err)
		return
	}
	programPath := filepath.Join(filepath.Dir(executable), stopFailureLogName)
	if err := appendStopFailureDiagnostic(programPath, diagnostic); err == nil {
		return
	} else {
		fallbackPath := filepath.Join(m.logDir, stopFailureLogName)
		if fallbackPath != programPath {
			if fallbackErr := appendStopFailureDiagnostic(fallbackPath, diagnostic); fallbackErr == nil {
				log.Printf("could not write stop-failure log beside the program (%s): %v; saved to %s", programPath, err, fallbackPath)
				return
			}
		}
		log.Printf("could not write stop-failure log to %s: %v", programPath, err)
	}
}

func appendStopFailureDiagnostic(path string, diagnostic stopFailureDiagnostic) error {
	stopFailureLogMu.Lock()
	defer stopFailureLogMu.Unlock()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write([]byte(formatStopFailureDiagnostic(diagnostic))); err != nil {
		return err
	}
	return file.Sync()
}

func formatStopFailureDiagnostic(diagnostic stopFailureDiagnostic) string {
	var output strings.Builder
	instance := diagnostic.Instance
	fmt.Fprintf(&output, "[%s] Stop failure\n", diagnostic.OccurredAt.Format(time.RFC3339Nano))
	fmt.Fprintf(&output, "Request ID: %s\n", diagnostic.RequestID)
	fmt.Fprintf(&output, "Instance: %s (%s)\n", instance.Name, instance.ID)
	fmt.Fprintf(&output, "Process ID: %d\n", instance.PID)
	fmt.Fprintf(&output, "Executable: %s\n", instance.ExecutablePath)
	fmt.Fprintf(&output, "Configured port: %d\n", instance.ConfiguredPort)
	fmt.Fprintf(&output, "Status after attempt: %s\n", diagnostic.StatusAfter)
	if diagnostic.Device.ID != "" {
		fmt.Fprintf(&output, "Device: %s (%s)\n", diagnostic.Device.Name, diagnostic.Device.ID)
		fmt.Fprintf(&output, "Connection: %s://%s\n", diagnostic.Device.Transport, diagnostic.Device.Address)
		fmt.Fprintf(&output, "Transport port: %d\n", diagnostic.Device.Port)
	}
	fmt.Fprintf(&output, "Error details:\n%s\n\n", diagnostic.Error)
	return output.String()
}
