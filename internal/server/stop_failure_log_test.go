package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendStopFailureDiagnosticWritesContextWithoutCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), stopFailureLogName)
	diagnostic := stopFailureDiagnostic{
		OccurredAt:  time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC),
		RequestID:   "task-1",
		Instance:    Instance{ID: "task-1", Name: "Gallery", PID: 42, ExecutablePath: `C:\Apps\Gallery.exe`, ConfiguredPort: 8080},
		StatusAfter: statusException,
		Device:      Device{ID: "device-1", Name: "Windows Host", Address: "192.168.1.10", Transport: "winrm", Port: 5986, Password: "must-not-be-logged"},
		Error:       "taskkill failed: exit status 128",
	}
	if err := appendStopFailureDiagnostic(path, diagnostic); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"Stop failure", "Gallery", "Process ID: 42", "192.168.1.10", "exit status 128"} {
		if !strings.Contains(text, expected) {
			t.Errorf("stop-failure log is missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, diagnostic.Device.Password) {
		t.Fatal("stop-failure log must not contain device credentials")
	}
}
