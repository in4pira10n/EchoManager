package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	statusStopped     = "stopped"
	statusStarting    = "starting"
	statusRunning     = "running"
	statusStopping    = "stopping"
	statusException   = "exception"
	statusUnavailable = "unavailable"
	startTimeout      = 25 * time.Second
	stopTimeout       = 5 * time.Second
)

var managerRestartWarning = uiText("backend.manager.message001")

func isActiveStatus(status string) bool {
	switch status {
	case statusStarting, statusRunning, statusStopping, statusException:
		return true
	default:
		return false
	}
}

type Instance struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	DeviceID          string            `json:"deviceId,omitempty"`
	ExecutablePath    string            `json:"executablePath"`
	ConfiguredPort    int               `json:"configuredPort"`
	Arguments         []string          `json:"arguments"`
	Environment       map[string]string `json:"environment"`
	IconFile          string            `json:"-"`
	Status            string            `json:"status"`
	PID               int               `json:"pid,omitempty"`
	ActualPort        int               `json:"actualPort,omitempty"`
	StartedAt         string            `json:"startedAt,omitempty"`
	ProcessStartToken string            `json:"-"`
	LastExitCode      *int              `json:"lastExitCode,omitempty"`
	Error             string            `json:"error,omitempty"`
}

type persistedState struct {
	Instances []persistedInstance `json:"instances"`
	Devices   []persistedDevice   `json:"devices,omitempty"`
}

type persistedInstance struct {
	Instance
	IconFile          string `json:"iconFile,omitempty"`
	ProcessStartToken string `json:"processStartToken,omitempty"`
}

type persistedDevice struct {
	Device
	Password          string `json:"password,omitempty"`
	EncryptedPassword string `json:"encryptedPassword,omitempty"`
}

type instanceResponse struct {
	Instance
	IconURL        string `json:"iconURL,omitempty"`
	DeviceName     string `json:"deviceName"`
	DeviceAddress  string `json:"deviceAddress"`
	DevicePlatform string `json:"devicePlatform"`
}

type managedProcess struct {
	cmd      *exec.Cmd
	logFile  *os.File
	done     chan struct{}
	expected bool
}

type instanceManager struct {
	mu                     sync.Mutex
	statePath              string
	logDir                 string
	iconDir                string
	settings               *managerSettingsStore
	instances              []Instance
	devices                []Device
	secretKey              []byte
	secretKeyPath          string
	remoteActionGeneration map[string]uint64
	remoteDebugMu          sync.Mutex
	running                map[string]*managedProcess
}

var (
	idPattern     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func newInstanceManager(dataDir string) (*instanceManager, error) {
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, err
	}
	iconDir := filepath.Join(dataDir, "icons")
	if err := os.MkdirAll(iconDir, 0o700); err != nil {
		return nil, err
	}
	m := &instanceManager{
		statePath: filepath.Join(dataDir, "instances.json"),
		logDir:    logDir, iconDir: iconDir, secretKeyPath: filepath.Join(dataDir, "device-secrets.key"),
		remoteActionGeneration: make(map[string]uint64),
		running:                make(map[string]*managedProcess),
	}
	if key, err := loadDeviceSecretKey(m.secretKeyPath); err != nil {
		return nil, err
	} else {
		m.secretKey = key
	}
	content, err := os.ReadFile(m.statePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(content) > 0 {
		var state persistedState
		if err := json.Unmarshal(content, &state); err != nil {
			return nil, fmt.Errorf("read instance database: %w", err)
		}
		seenDevices := make(map[string]bool)
		for _, saved := range state.Devices {
			device := saved.Device
			device.Password = saved.Password
			if saved.EncryptedPassword != "" {
				device.Password, err = m.openDeviceSecret(saved.EncryptedPassword)
				if err != nil {
					return nil, fmt.Errorf("decrypt device credentials: %w", err)
				}
			}
			if !idPattern.MatchString(device.ID) || seenDevices[device.ID] {
				continue
			}
			seenDevices[device.ID] = true
			device.Status = deviceStatusUnknown
			m.devices = append(m.devices, device)
		}
		seen := make(map[string]bool)
		for _, saved := range state.Instances {
			instance := saved.Instance
			instance.IconFile = saved.IconFile
			instance.ProcessStartToken = saved.ProcessStartToken
			if instance.DeviceID == localDeviceID {
				instance.DeviceID = ""
			}
			if !idPattern.MatchString(instance.ID) || seen[instance.ID] {
				continue
			}
			seen[instance.ID] = true
			if !validIconFileName(instance.ID, instance.IconFile) {
				instance.IconFile = ""
			} else if _, err := os.Stat(filepath.Join(iconDir, instance.IconFile)); err != nil {
				instance.IconFile = ""
			}
			previousStatus := instance.Status
			previousPID := instance.PID
			instance.Status = statusStopped
			instance.PID = 0
			instance.ActualPort = 0
			instance.StartedAt = ""
			if instance.Arguments == nil {
				instance.Arguments = []string{}
			}
			if instance.Environment == nil {
				instance.Environment = map[string]string{}
			}
			if instance.DeviceID != "" {
				if m.deviceIndexLocked(instance.DeviceID) < 0 {
					instance.Status = statusUnavailable
					instance.Error = uiText("backend.manager.message002")
				} else if previousPID > 0 && isActiveStatus(previousStatus) {
					instance.Status = statusException
					instance.PID = previousPID
					instance.StartedAt = saved.StartedAt
					instance.Error = uiText("backend.manager.message003")
				} else {
					instance.Error = ""
				}
			} else if err := validateExecutable(instance.ExecutablePath); err != nil {
				instance.Status = statusUnavailable
				instance.Error = uiText("backend.manager.message004") + err.Error()
			} else if previousPID > 0 && (previousStatus == statusStarting || previousStatus == statusRunning || previousStatus == statusStopping || previousStatus == statusException) && portAccepting(instance.ConfiguredPort) {
				instance.Status = statusException
				instance.PID = previousPID
				instance.ActualPort = instance.ConfiguredPort
				instance.Error = fmt.Sprintf(uiText("backend.manager.message005"), instance.ConfiguredPort)
				m.appendLogLocked(instance.ID, instance.Error)
			} else if instance.Error == "" && (previousPID > 0 || isActiveStatus(previousStatus)) {
				instance.Error = managerRestartWarning
			}
			m.instances = append(m.instances, instance)
		}
	}
	if err := m.saveLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *instanceManager) saveLocked() error {
	instances := make([]persistedInstance, 0, len(m.instances))
	for _, instance := range m.instances {
		instances = append(instances, persistedInstance{
			Instance: instance, IconFile: instance.IconFile, ProcessStartToken: instance.ProcessStartToken,
		})
	}
	devices := make([]persistedDevice, 0, len(m.devices))
	for _, device := range m.devices {
		saved := persistedDevice{Device: device}
		if device.Password != "" {
			encrypted, err := m.sealDeviceSecret(device.Password)
			if err != nil {
				return err
			}
			saved.EncryptedPassword = encrypted
		}
		devices = append(devices, saved)
	}
	content, err := json.MarshalIndent(persistedState{Instances: instances, Devices: devices}, "", "  ")
	if err != nil {
		return err
	}
	temp := m.statePath + ".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temp)
		return err
	}
	return replaceFile(temp, m.statePath)
}

func cloneInstance(instance Instance) Instance {
	arguments := make([]string, len(instance.Arguments))
	copy(arguments, instance.Arguments)
	instance.Arguments = arguments
	instance.Environment = cloneEnvironment(instance.Environment)
	if instance.LastExitCode != nil {
		code := *instance.LastExitCode
		instance.LastExitCode = &code
	}
	return instance
}

func cloneEnvironment(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (m *instanceManager) list() []Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Instance, 0, len(m.instances))
	for _, instance := range m.instances {
		result = append(result, cloneInstance(instance))
	}
	return result
}

func (m *instanceManager) refreshLocalStates() {
	instances := m.list()
	ports := make([]int, 0, len(instances))
	seenPorts := make(map[int]struct{})
	for _, instance := range instances {
		if instance.DeviceID != "" || instance.ConfiguredPort < 1 {
			continue
		}
		if _, exists := seenPorts[instance.ConfiguredPort]; !exists {
			seenPorts[instance.ConfiguredPort] = struct{}{}
			ports = append(ports, instance.ConfiguredPort)
		}
	}
	listeners, err := discoverPortListeners(ports)
	if err != nil {
		log.Printf("local process discovery failed: %v", err)
		return
	}
	stalePIDs := make([]int, 0)
	for _, instance := range instances {
		if instance.DeviceID == "" && instance.PID > 0 && len(listeners[instance.ConfiguredPort]) == 0 {
			stalePIDs = append(stalePIDs, instance.PID)
		}
	}
	staleProcesses, processInspectErr := inspectProcessIdentities(stalePIDs)
	if processInspectErr != nil {
		log.Printf("inspect local processes without listeners: %v", processInspectErr)
	}

	claims := make(map[int]map[int]int)
	for _, instance := range instances {
		if instance.DeviceID != "" {
			continue
		}
		for _, identity := range listeners[instance.ConfiguredPort] {
			if identity.PID == 0 || identity.Executable == "" || !sameExecutablePath(instance.ExecutablePath, identity.Executable) {
				continue
			}
			if claims[instance.ConfiguredPort] == nil {
				claims[instance.ConfiguredPort] = make(map[int]int)
			}
			claims[instance.ConfiguredPort][identity.PID]++
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, snapshot := range instances {
		if snapshot.DeviceID != "" || m.running[snapshot.ID] != nil {
			continue
		}
		index := m.indexLocked(snapshot.ID)
		if index < 0 {
			continue
		}
		current := &m.instances[index]
		portListeners := listeners[snapshot.ConfiguredPort]
		var match processIdentity
		matchCount := 0
		for _, identity := range portListeners {
			if identity.PID > 0 && identity.Executable != "" && identity.StartToken != "" &&
				sameExecutablePath(snapshot.ExecutablePath, identity.Executable) && claims[snapshot.ConfiguredPort][identity.PID] == 1 {
				match = identity
				matchCount++
			}
		}
		if len(portListeners) == 1 && matchCount == 1 {
			startedAt := normalizeProcessStartTime(match.StartedAt)
			if startedAt == "" {
				startedAt = time.Now().Format(time.RFC3339)
			}
			if current.Status != statusRunning || current.PID != match.PID || current.ActualPort != snapshot.ConfiguredPort || current.ProcessStartToken != match.StartToken || current.Error != "" {
				current.Status = statusRunning
				current.PID = match.PID
				current.ActualPort = snapshot.ConfiguredPort
				current.StartedAt = startedAt
				current.ProcessStartToken = match.StartToken
				current.Error = ""
				changed = true
			}
			continue
		}
		if len(portListeners) > 0 {
			message := fmt.Sprintf(uiText("backend.manager.message006"), snapshot.ConfiguredPort)
			if current.Status != statusException || current.Error != message || current.PID != 0 || current.ActualPort != snapshot.ConfiguredPort {
				current.Status = statusException
				current.PID = 0
				current.ActualPort = snapshot.ConfiguredPort
				current.StartedAt = ""
				current.ProcessStartToken = ""
				current.Error = message
				changed = true
			}
			continue
		}
		if snapshot.PID > 0 {
			identity, exists := staleProcesses[snapshot.PID]
			if exists && processInspectErr == nil && identity.StartToken != "" && identity.StartToken == snapshot.ProcessStartToken && sameExecutablePath(snapshot.ExecutablePath, identity.Executable) {
				message := fmt.Sprintf(uiText("backend.manager.message007"), snapshot.PID, snapshot.ConfiguredPort)
				if current.Status != statusException || current.Error != message || current.ActualPort != 0 {
					current.Status = statusException
					current.ActualPort = 0
					current.Error = message
					current.StartedAt = normalizeProcessStartTime(identity.StartedAt)
					changed = true
				}
				continue
			}
			if processInspectErr != nil {
				continue
			}
			current.Status = statusStopped
			current.PID = 0
			current.ActualPort = 0
			current.StartedAt = ""
			current.ProcessStartToken = ""
			current.Error = ""
			changed = true
		} else if clearManagerRestartWarning(current) {
			changed = true
		}
	}
	if changed {
		if err := m.saveLocked(); err != nil {
			log.Printf("save refreshed local process state: %v", err)
		}
	}
}

// clearManagerRestartWarning clears the startup warning after local discovery
// confirms that no process or listener remains to verify.
func clearManagerRestartWarning(instance *Instance) bool {
	if instance == nil || instance.Error != managerRestartWarning || instance.PID > 0 {
		return false
	}
	instance.Status = statusStopped
	instance.ActualPort = 0
	instance.StartedAt = ""
	instance.ProcessStartToken = ""
	instance.Error = ""
	return true
}

func sameExecutablePath(configured, actual string) bool {
	configuredPath := normalizedExecutablePath(configured)
	actualPath := normalizedExecutablePath(actual)
	return configuredPath != "" && actualPath != "" && processExecutableMatches(configuredPath, actualPath)
}

func normalizedExecutablePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute)
}

func normalizeProcessStartTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.Format(time.RFC3339)
	}
	for _, layout := range []string{"Mon Jan _2 15:04:05 2006", "Mon Jan 2 15:04:05 2006"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed.Format(time.RFC3339)
		}
	}
	return ""
}

func (m *instanceManager) reorder(ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(ids) != len(m.instances) {
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message008")}
	}

	byID := make(map[string]Instance, len(m.instances))
	for _, instance := range m.instances {
		byID[instance.ID] = instance
	}
	ordered := make([]Instance, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		instance, exists := byID[id]
		if !exists || !idPattern.MatchString(id) {
			return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message009")}
		}
		if _, duplicate := seen[id]; duplicate {
			return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message010")}
		}
		seen[id] = struct{}{}
		ordered = append(ordered, instance)
	}

	previous := m.instances
	m.instances = ordered
	if err := m.saveLocked(); err != nil {
		m.instances = previous
		return err
	}
	return nil
}

func (m *instanceManager) instanceByID(id string) (Instance, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexLocked(id)
	if index < 0 {
		return Instance{}, false
	}
	return cloneInstance(m.instances[index]), true
}

func (m *instanceManager) publicInstance(instance Instance) instanceResponse {
	response := instanceResponse{Instance: cloneInstance(instance)}
	response.IconFile = ""
	if response.DeviceID == "" {
		response.DeviceID = localDeviceID
	}
	if response.DeviceID == localDeviceID {
		response.DeviceName, _ = os.Hostname()
		response.DeviceAddress = firstLANIPv4()
		response.DevicePlatform = runtime.GOOS
	} else {
		m.mu.Lock()
		device, found := m.deviceLocked(response.DeviceID)
		m.mu.Unlock()
		if found {
			response.DeviceName = device.Name
			response.DeviceAddress = device.Address
			response.DevicePlatform = device.Platform
		} else {
			response.DeviceName = uiText("ui.unknownDevice")
		}
	}
	if instance.IconFile != "" {
		response.IconURL = "/api/instances/" + instance.ID + "/icon?v=" + url.QueryEscape(instance.IconFile)
	}
	return response
}

func (m *instanceManager) create(input Instance) (Instance, error) {
	if input.DeviceID == localDeviceID {
		input.DeviceID = ""
	}
	platform, err := m.platformForDevice(input.DeviceID)
	if err != nil {
		return Instance{}, err
	}
	if err := validateInstanceForPlatform(&input, platform); err != nil {
		return Instance{}, err
	}
	id, err := randomID()
	if err != nil {
		return Instance{}, err
	}
	input.ID = id
	input.Status = statusStopped
	if input.DeviceID == "" {
		if err := validateExecutable(input.ExecutablePath); err != nil {
			input.Status = statusUnavailable
			input.Error = uiText("backend.manager.message004") + err.Error()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if input.DeviceID != "" && m.deviceIndexLocked(input.DeviceID) < 0 {
		return Instance{}, apiError{status: http.StatusConflict, message: uiText("backend.manager.message011")}
	}
	m.instances = append(m.instances, input)
	if err := m.saveLocked(); err != nil {
		m.instances = m.instances[:len(m.instances)-1]
		return Instance{}, err
	}
	return cloneInstance(input), nil
}

func (m *instanceManager) update(id string, input Instance) (Instance, error) {
	if input.DeviceID == localDeviceID {
		input.DeviceID = ""
	}
	platform, err := m.platformForDevice(input.DeviceID)
	if err != nil {
		return Instance{}, err
	}
	if err := validateInstanceForPlatform(&input, platform); err != nil {
		return Instance{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, active := m.running[id]; active {
		return Instance{}, apiError{status: http.StatusConflict, message: uiText("backend.manager.message012")}
	}
	index := m.indexLocked(id)
	if index < 0 {
		return Instance{}, apiError{status: http.StatusNotFound, message: uiText("backend.manager.message013")}
	}
	previous := m.instances[index]
	if previous.PID > 0 && previous.ProcessStartToken != "" {
		return Instance{}, apiError{status: http.StatusConflict, message: uiText("backend.manager.message014")}
	}
	if input.DeviceID != "" && m.deviceIndexLocked(input.DeviceID) < 0 {
		return Instance{}, apiError{status: http.StatusConflict, message: uiText("backend.manager.message011")}
	}
	input.ID = id
	input.IconFile = previous.IconFile
	input.Status = statusStopped
	input.PID = 0
	input.ActualPort = 0
	input.StartedAt = ""
	input.ProcessStartToken = ""
	input.LastExitCode = previous.LastExitCode
	if input.DeviceID == "" {
		if err := validateExecutable(input.ExecutablePath); err != nil {
			input.Status = statusUnavailable
			input.Error = uiText("backend.manager.message004") + err.Error()
		} else {
			input.Error = ""
		}
	} else {
		input.Error = ""
	}
	m.instances[index] = input
	if err := m.saveLocked(); err != nil {
		m.instances[index] = previous
		return Instance{}, err
	}
	return cloneInstance(input), nil
}

func (m *instanceManager) delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexLocked(id)
	if index < 0 {
		return apiError{status: http.StatusNotFound, message: uiText("backend.manager.message013")}
	}
	instance := m.instances[index]
	if _, active := m.running[id]; active || instance.Status == statusStarting || instance.Status == statusRunning || instance.Status == statusStopping {
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message015")}
	}
	previous := append([]Instance(nil), m.instances...)
	m.instances = append(m.instances[:index], m.instances[index+1:]...)
	if err := m.saveLocked(); err != nil {
		m.instances = previous
		return err
	}
	if instance.IconFile != "" {
		_ = os.Remove(filepath.Join(m.iconDir, instance.IconFile))
	}
	return nil
}

func (m *instanceManager) updateIcon(id, iconData string) (Instance, error) {
	imageData, err := decodeIconData(iconData)
	if err != nil {
		return Instance{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexLocked(id)
	if index < 0 {
		return Instance{}, apiError{status: http.StatusNotFound, message: uiText("backend.manager.message013")}
	}
	fileID, err := randomID()
	if err != nil {
		return Instance{}, err
	}
	fileName := id + "-" + fileID + ".png"
	tempPath := filepath.Join(m.iconDir, fileName+".tmp")
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Instance{}, err
	}
	if _, err = file.Write(imageData); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tempPath, filepath.Join(m.iconDir, fileName))
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return Instance{}, err
	}
	previous := m.instances[index].IconFile
	m.instances[index].IconFile = fileName
	if err := m.saveLocked(); err != nil {
		m.instances[index].IconFile = previous
		_ = os.Remove(filepath.Join(m.iconDir, fileName))
		return Instance{}, err
	}
	if previous != "" {
		_ = os.Remove(filepath.Join(m.iconDir, previous))
	}
	return cloneInstance(m.instances[index]), nil
}

func validIconFileName(id, name string) bool {
	if name == "" || filepath.Base(name) != name || !strings.HasPrefix(name, id+"-") || !strings.HasSuffix(name, ".png") {
		return false
	}
	return regexp.MustCompile(`^[a-f0-9]{32}-[a-f0-9]{32}\.png$`).MatchString(name)
}

func (m *instanceManager) readIcon(id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexLocked(id)
	if index < 0 {
		return nil, apiError{status: http.StatusNotFound, message: uiText("backend.manager.message013")}
	}
	name := m.instances[index].IconFile
	if !validIconFileName(id, name) {
		return nil, apiError{status: http.StatusNotFound, message: uiText("backend.manager.message016")}
	}
	imageData, err := os.ReadFile(filepath.Join(m.iconDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, apiError{status: http.StatusNotFound, message: uiText("backend.manager.message017")}
	}
	return imageData, err
}

func (m *instanceManager) start(id string) error {
	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return apiError{status: http.StatusNotFound, message: uiText("backend.manager.message013")}
	}
	instance := m.instances[index]
	if instance.DeviceID != "" {
		m.mu.Unlock()
		return m.startRemote(id)
	}
	if active := m.running[id]; active != nil {
		m.mu.Unlock()
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message018")}
	}
	if instance.PID > 0 && instance.ProcessStartToken != "" && instance.Status == statusException {
		m.mu.Unlock()
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message019")}
	}
	if instance.Status == statusStarting || instance.Status == statusRunning || instance.Status == statusStopping {
		m.mu.Unlock()
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message020")}
	}
	if err := validateExecutable(instance.ExecutablePath); err != nil {
		m.instances[index].Status = statusUnavailable
		m.instances[index].Error = err.Error()
		m.appendLogLocked(id, uiText("backend.manager.message021")+err.Error())
		_ = m.saveLocked()
		m.mu.Unlock()
		return apiError{status: http.StatusBadRequest, message: err.Error()}
	}
	if err := checkPortAvailable(instance.ConfiguredPort); err != nil {
		m.instances[index].Status = statusException
		m.instances[index].Error = err.Error()
		m.appendLogLocked(id, err.Error())
		_ = m.saveLocked()
		m.mu.Unlock()
		return apiError{status: http.StatusConflict, message: err.Error()}
	}

	logPath := m.logPath(id)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		m.mu.Unlock()
		return apiError{status: http.StatusInternalServerError, message: uiText("backend.manager.message022") + err.Error()}
	}
	_, _ = fmt.Fprintf(logFile, uiText("backend.manager.message023"), time.Now().Format(time.RFC3339), instance.Name)
	cmd := exec.Command(instance.ExecutablePath, instance.Arguments...)
	cmd.Dir = filepath.Dir(instance.ExecutablePath)
	cmd.Env = mergedEnvironment(instance.Environment)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureCommand(cmd)
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(logFile, uiText("backend.manager.message024"), err)
		_ = logFile.Close()
		m.instances[index].Status = statusException
		m.instances[index].Error = uiText("backend.manager.message025") + err.Error()
		_ = m.saveLocked()
		m.mu.Unlock()
		return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message025") + err.Error()}
	}
	mp := &managedProcess{cmd: cmd, logFile: logFile, done: make(chan struct{})}
	m.running[id] = mp
	m.instances[index].Status = statusStarting
	m.instances[index].PID = cmd.Process.Pid
	m.instances[index].ProcessStartToken = ""
	m.instances[index].ActualPort = 0
	m.instances[index].StartedAt = time.Now().Format(time.RFC3339)
	m.instances[index].LastExitCode = nil
	m.instances[index].Error = uiText("backend.manager.message026")
	if err := m.saveLocked(); err != nil {
		delete(m.running, id)
		m.instances[index].Status = statusException
		m.instances[index].PID = 0
		m.instances[index].Error = uiText("backend.manager.message027") + err.Error()
		_ = terminateManagedProcess(cmd, true)
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()

	go m.waitForExit(id, mp)
	go m.monitorReadiness(id, mp, instance.ConfiguredPort)
	return nil
}

func (m *instanceManager) monitorReadiness(id string, process *managedProcess, port int) {
	timeout := m.readinessTimeout()
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-process.done:
			return
		case <-deadline.C:
			m.mu.Lock()
			if m.running[id] == process {
				process.expected = true
				if index := m.indexLocked(id); index >= 0 {
					m.instances[index].Status = statusException
					m.instances[index].ActualPort = 0
					m.instances[index].Error = fmt.Sprintf(uiText("backend.manager.message028"), port, int(timeout.Seconds()))
					m.appendLogLocked(id, m.instances[index].Error)
					_ = m.saveLocked()
				}
			}
			m.mu.Unlock()
			_ = terminateManagedProcess(process.cmd, true)
			return
		case <-ticker.C:
			if !portAccepting(port) {
				continue
			}
			becameReady := false
			m.mu.Lock()
			if m.running[id] == process {
				if index := m.indexLocked(id); index >= 0 && m.instances[index].Status == statusStarting {
					m.instances[index].Status = statusRunning
					m.instances[index].ActualPort = port
					m.instances[index].Error = ""
					_ = m.saveLocked()
					becameReady = true
				}
			}
			m.mu.Unlock()
			if becameReady {
				go m.monitorHealth(id, process, port)
			}
			return
		}
	}
}

func (m *instanceManager) readinessTimeout() time.Duration {
	if m.settings == nil {
		return startTimeout
	}
	settings, _ := m.settings.get()
	if settings.StartTimeoutSeconds < minStartTimeoutSeconds || settings.StartTimeoutSeconds > maxStartTimeoutSeconds {
		return startTimeout
	}
	return time.Duration(settings.StartTimeoutSeconds) * time.Second
}

func (m *instanceManager) monitorHealth(id string, process *managedProcess, port int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	consecutiveFailures := 0
	for {
		select {
		case <-process.done:
			return
		case <-ticker.C:
			if portAccepting(port) {
				consecutiveFailures = 0
				m.mu.Lock()
				if m.running[id] == process {
					if index := m.indexLocked(id); index >= 0 && m.instances[index].Status == statusException && strings.Contains(m.instances[index].Error, uiText("backend.manager.message029")) {
						m.instances[index].Status = statusRunning
						m.instances[index].ActualPort = port
						m.instances[index].Error = ""
						_ = m.saveLocked()
					}
				}
				m.mu.Unlock()
				continue
			}
			consecutiveFailures++
			if consecutiveFailures < 3 {
				continue
			}
			m.mu.Lock()
			if m.running[id] == process {
				if index := m.indexLocked(id); index >= 0 && m.instances[index].Status == statusRunning {
					m.instances[index].Status = statusException
					m.instances[index].Error = fmt.Sprintf(uiText("backend.manager.message030"), port)
					m.appendLogLocked(id, m.instances[index].Error)
					_ = m.saveLocked()
				}
			}
			m.mu.Unlock()
			consecutiveFailures = 0
		}
	}
}

func (m *instanceManager) waitForExit(id string, process *managedProcess) {
	err := process.cmd.Wait()
	if err != nil {
		_, _ = fmt.Fprintf(process.logFile, uiText("backend.manager.message031"), time.Now().Format(time.RFC3339), err)
	} else {
		_, _ = fmt.Fprintf(process.logFile, uiText("backend.manager.message032"), time.Now().Format(time.RFC3339))
	}
	_ = process.logFile.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running[id] != process {
		close(process.done)
		return
	}
	delete(m.running, id)
	if index := m.indexLocked(id); index >= 0 {
		instance := &m.instances[index]
		if instance.Status == statusStopping {
			instance.Status = statusStopped
			instance.Error = ""
		} else if process.expected && instance.Status == statusException {
			// Preserve the readiness failure that caused the process to be terminated.
		} else {
			instance.Status = statusException
			instance.Error = uiText("backend.manager.message033")
		}
		instance.PID = 0
		instance.ProcessStartToken = ""
		instance.ActualPort = 0
		if exitError, ok := err.(*exec.ExitError); ok {
			code := exitError.ExitCode()
			instance.LastExitCode = &code
		} else if err == nil {
			code := 0
			instance.LastExitCode = &code
		} else {
			instance.LastExitCode = nil
			instance.Error += " " + err.Error()
		}
		_ = m.saveLocked()
	}
	close(process.done)
}

func (m *instanceManager) stop(id string) (resultErr error) {
	var attempted Instance
	defer func() {
		if resultErr != nil {
			m.appendStopFailureLog(id, attempted, resultErr)
		}
	}()

	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return apiError{status: http.StatusNotFound, message: uiText("backend.manager.message013")}
	}
	attempted = cloneInstance(m.instances[index])
	if m.instances[index].DeviceID != "" {
		m.mu.Unlock()
		return m.stopRemote(id)
	}
	if m.instances[index].Status == statusStopping {
		m.mu.Unlock()
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message034")}
	}
	process := m.running[id]
	if process == nil {
		instance := m.instances[index]
		if instance.PID > 0 && instance.ProcessStartToken != "" &&
			(instance.Status == statusRunning || instance.Status == statusException || instance.Status == statusStopping) {
			m.instances[index].Status = statusStopping
			m.instances[index].Error = uiText("backend.manager.message035")
			_ = m.saveLocked()
			m.mu.Unlock()
			return m.stopDiscoveredProcess(instance)
		}
		m.mu.Unlock()
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message036")}
	}
	process.expected = true
	m.instances[index].Status = statusStopping
	m.instances[index].Error = uiText("backend.manager.message037")
	_ = m.saveLocked()
	m.mu.Unlock()

	var gracefulStopErr error
	if err := terminateManagedProcess(process.cmd, false); err != nil && !errors.Is(err, os.ErrProcessDone) {
		gracefulStopErr = err
	}
	select {
	case <-process.done:
		return nil
	case <-time.After(stopTimeout):
	}
	if err := terminateManagedProcess(process.cmd, true); err != nil && !errors.Is(err, os.ErrProcessDone) {
		stopErr := withGracefulStopError(gracefulStopErr, fmt.Errorf(uiText("backend.manager.message038"), err))
		m.markStopError(id, process, stopErr)
		return apiError{status: http.StatusInternalServerError, message: stopErr.Error()}
	}
	select {
	case <-process.done:
		return nil
	case <-time.After(2 * time.Second):
		stopErr := withGracefulStopError(gracefulStopErr, errors.New(uiText("backend.manager.message039")))
		m.markStopError(id, process, stopErr)
		return apiError{status: http.StatusInternalServerError, message: uiText("backend.manager.message040")}
	}
}

func withGracefulStopError(gracefulErr, finalErr error) error {
	if finalErr == nil {
		return gracefulErr
	}
	if gracefulErr == nil {
		return finalErr
	}
	return fmt.Errorf(uiText("backend.manager.message041"), finalErr, gracefulErr)
}

func (m *instanceManager) markStopError(id string, process *managedProcess, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running[id] != process {
		return
	}
	if index := m.indexLocked(id); index >= 0 {
		m.instances[index].Status = statusException
		m.instances[index].Error = uiText("backend.manager.message042") + err.Error()
		_ = m.saveLocked()
	}
}

func (m *instanceManager) stopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.running))
	for id := range m.running {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.stop(id)
	}
}

func (m *instanceManager) logs(id string) (string, error) {
	m.mu.Lock()
	index := m.indexLocked(id)
	if index < 0 {
		m.mu.Unlock()
		return "", apiError{status: http.StatusNotFound, message: uiText("backend.manager.message013")}
	}
	instance := cloneInstance(m.instances[index])
	if instance.DeviceID != "" {
		device, found := m.deviceLocked(instance.DeviceID)
		m.mu.Unlock()
		if !found {
			return "", apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message043")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		content, err := runRemoteCommand(ctx, device, remoteLogsScript(device, instance))
		return content, err
	}
	defer m.mu.Unlock()
	file, err := os.Open(m.logPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return uiText("backend.manager.message044"), nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	const maxBytes = 256 * 1024
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > maxBytes {
		_, _ = file.Seek(-maxBytes, io.SeekEnd)
	} else {
		_, _ = file.Seek(0, io.SeekStart)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes))
	if err != nil {
		return "", err
	}
	if info.Size() > maxBytes {
		if newline := bytes.IndexByte(content, '\n'); newline >= 0 {
			content = content[newline+1:]
		}
		return uiText("backend.manager.message045") + string(content), nil
	}
	return string(content), nil
}

func (m *instanceManager) indexLocked(id string) int {
	for i := range m.instances {
		if m.instances[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *instanceManager) logPath(id string) string {
	return filepath.Join(m.logDir, id+".log")
}

func (m *instanceManager) appendLogLocked(id, message string) {
	file, err := os.OpenFile(m.logPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(file, "\n[%s] %s\n", time.Now().Format(time.RFC3339), message)
	_ = file.Close()
}

func (m *instanceManager) remoteDebugLogPath(id string) string {
	return filepath.Join(m.logDir, id+"-remote-debug.log")
}

func (m *instanceManager) appendRemoteDebugLog(id, message string) {
	if id == "" {
		return
	}
	message = strings.ReplaceAll(message, "\r", "\\r")
	message = strings.ReplaceAll(message, "\n", "\\n")
	m.remoteDebugMu.Lock()
	defer m.remoteDebugMu.Unlock()
	file, err := os.OpenFile(m.remoteDebugLogPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("Could not write remote task diagnostic log for %s: %v", id, err)
		return
	}
	if _, err := fmt.Fprintf(file, "[%s] %s\n", time.Now().Format(time.RFC3339Nano), message); err != nil {
		log.Printf("Could not append remote task diagnostic log for %s: %v", id, err)
	}
	if err := file.Close(); err != nil {
		log.Printf("Could not close remote task diagnostic log for %s: %v", id, err)
	}
}

func validateInstance(instance *Instance) error {
	return validateInstanceForPlatform(instance, runtime.GOOS)
}

func validateInstanceForPlatform(instance *Instance, platform string) error {
	instance.Name = strings.TrimSpace(instance.Name)
	instance.ExecutablePath = strings.TrimSpace(instance.ExecutablePath)
	if instance.Name == "" || len(instance.Name) > 120 {
		return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message046")}
	}
	if instance.ExecutablePath == "" {
		return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message047")}
	}
	if platform == "win32" {
		platform = "windows"
	}
	if err := validateTargetExecutable(instance.ExecutablePath, platform); err != nil {
		return apiError{status: http.StatusBadRequest, message: err.Error()}
	}
	if instance.ConfiguredPort < 1 || instance.ConfiguredPort > 65535 {
		return apiError{status: http.StatusBadRequest, message: uiText("error.invalidPort")}
	}
	if instance.Arguments == nil {
		instance.Arguments = []string{}
	}
	if instance.Environment == nil {
		instance.Environment = map[string]string{}
	}
	if len(instance.Environment) > 100 {
		return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message048")}
	}
	for key, value := range instance.Environment {
		if !envKeyPattern.MatchString(key) || strings.ContainsRune(value, '\x00') {
			return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message049")}
		}
	}
	return nil
}

func (m *instanceManager) stopDiscoveredProcess(instance Instance) error {
	identity, exists, err := inspectProcessIdentity(instance.PID)
	if err != nil {
		m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, err)
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message050") + err.Error()}
	}
	if !exists {
		m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, nil)
		return nil
	}
	if identity.StartToken != instance.ProcessStartToken || !sameExecutablePath(instance.ExecutablePath, identity.Executable) {
		err := errors.New(uiText("backend.manager.message051"))
		m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, err)
		return apiError{status: http.StatusConflict, message: uiText("backend.manager.message052")}
	}

	var gracefulStopErr error
	if err := terminateManagedPID(instance.PID, false); err != nil && !errors.Is(err, os.ErrProcessDone) {
		gracefulStopErr = err
		stillMatches, inspectErr := discoveredProcessMatches(instance)
		if inspectErr != nil {
			stopErr := withGracefulStopError(gracefulStopErr, fmt.Errorf(uiText("backend.manager.message053"), inspectErr))
			m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, stopErr)
			return apiError{status: http.StatusInternalServerError, message: uiText("backend.manager.message054") + stopErr.Error()}
		}
		if !stillMatches {
			m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, nil)
			return nil
		}
	}
	if waitForProcessExit(instance.PID, instance.ProcessStartToken, stopTimeout) {
		m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, nil)
		return nil
	}
	stillMatches, inspectErr := discoveredProcessMatches(instance)
	if inspectErr != nil {
		stopErr := withGracefulStopError(gracefulStopErr, fmt.Errorf(uiText("backend.manager.message055"), inspectErr))
		m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, stopErr)
		return apiError{status: http.StatusInternalServerError, message: uiText("backend.manager.message054") + stopErr.Error()}
	}
	if !stillMatches {
		m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, nil)
		return nil
	}
	if err := terminateManagedPID(instance.PID, true); err != nil && !errors.Is(err, os.ErrProcessDone) {
		stillMatches, inspectErr := discoveredProcessMatches(instance)
		if inspectErr == nil && !stillMatches {
			m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, nil)
			return nil
		}
		stopErr := withGracefulStopError(gracefulStopErr, fmt.Errorf(uiText("backend.manager.message056"), err))
		if inspectErr != nil {
			stopErr = fmt.Errorf(uiText("backend.manager.message057"), stopErr, inspectErr)
		}
		m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, stopErr)
		return apiError{status: http.StatusInternalServerError, message: stopErr.Error()}
	}
	if !waitForProcessExit(instance.PID, instance.ProcessStartToken, 2*time.Second) {
		stopErr := withGracefulStopError(gracefulStopErr, errors.New(uiText("backend.manager.message039")))
		m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, stopErr)
		return apiError{status: http.StatusInternalServerError, message: uiText("backend.manager.message040")}
	}
	m.finishDiscoveredStop(instance.ID, instance.PID, instance.ProcessStartToken, nil)
	return nil
}

func discoveredProcessMatches(instance Instance) (bool, error) {
	identity, exists, err := inspectProcessIdentity(instance.PID)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if identity.StartToken != instance.ProcessStartToken || !sameExecutablePath(instance.ExecutablePath, identity.Executable) {
		return false, errors.New(uiText("backend.manager.message051"))
	}
	return true, nil
}

func waitForProcessExit(pid int, startToken string, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		identity, exists, err := inspectProcessIdentity(pid)
		if err == nil && (!exists || identity.StartToken != startToken) {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func (m *instanceManager) finishDiscoveredStop(id string, pid int, startToken string, stopErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexLocked(id)
	if index < 0 || m.instances[index].PID != pid || m.instances[index].ProcessStartToken != startToken {
		return
	}
	if stopErr != nil {
		m.instances[index].Status = statusException
		m.instances[index].Error = uiText("backend.manager.message042") + stopErr.Error()
		_ = m.saveLocked()
		return
	}
	m.instances[index].Status = statusStopped
	m.instances[index].PID = 0
	m.instances[index].ActualPort = 0
	m.instances[index].StartedAt = ""
	m.instances[index].ProcessStartToken = ""
	m.instances[index].Error = ""
	_ = m.saveLocked()
}

func decodeIconData(iconData string) ([]byte, error) {
	if iconData == "" {
		return nil, apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message058")}
	}
	const prefix = "data:image/png;base64,"
	if len(iconData) > 1_500_000 || !strings.HasPrefix(iconData, prefix) {
		return nil, apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message059")}
	}
	imageData, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(iconData, prefix))
	if err != nil || !bytes.HasPrefix(imageData, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		return nil, apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message060")}
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(imageData))
	if err != nil || format != "png" || config.Width != 512 || config.Height != 512 {
		return nil, apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message061")}
	}
	return imageData, nil
}

func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf(uiText("backend.manager.message062"), err)
	}
	if info.IsDir() {
		return errors.New(uiText("backend.manager.message063"))
	}
	if err := executablePermission(path, info); err != nil {
		return err
	}
	return nil
}

func checkPortAvailable(port int) error {
	if portAccepting(port) {
		return fmt.Errorf(uiText("backend.manager.message064"), port)
	}
	for _, address := range []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), net.JoinHostPort("::1", strconv.Itoa(port))} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			if portAccepting(port) {
				return fmt.Errorf(uiText("backend.manager.message064"), port)
			}
			if strings.Contains(address, "::1") {
				continue
			}
			return fmt.Errorf(uiText("backend.manager.message065"), port, err)
		}
		_ = listener.Close()
	}
	return nil
}

func portAccepting(port int) bool {
	for _, address := range []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), net.JoinHostPort("::1", strconv.Itoa(port))} {
		connection, err := net.DialTimeout("tcp", address, 180*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return true
		}
	}
	return false
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", bytes), nil
}

func (m *instanceManager) apiHandler(auth *authManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": uiText("backend.auth.message011")})
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": uiText("backend.auth.message010")})
			return
		}
		if _, ok := auth.currentUser(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": uiText("backend.manager.message066")})
			return
		}
		if r.URL.Path == "/api/devices" {
			m.handleDevices(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/devices/") {
			m.handleDevice(w, r, strings.TrimPrefix(r.URL.Path, "/api/devices/"))
			return
		}
		if r.URL.Path == "/api/instances" || r.URL.Path == "/api/instances/refresh" {
			m.handleInstances(w, r)
			return
		}
		if r.URL.Path == "/api/instances/order" {
			m.handleInstanceOrder(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/instances/") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": uiText("backend.manager.message067")})
			return
		}
		m.handleInstance(w, r, strings.TrimPrefix(r.URL.Path, "/api/instances/"))
	})
}

func (m *instanceManager) handleInstanceOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeAPIError(w, err)
		return
	}
	if input.IDs == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": uiText("backend.manager.message068")})
		return
	}
	if err := m.reorder(input.IDs); err != nil {
		writeAPIError(w, err)
		return
	}
	instances := m.list()
	response := make([]instanceResponse, 0, len(instances))
	for _, instance := range instances {
		response = append(response, m.publicInstance(instance))
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": response})
}

func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}
	expectedScheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		expectedScheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, expectedScheme)
}

func (m *instanceManager) handleInstances(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/instances/refresh" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		instanceFilter := r.URL.Query().Get("instance")
		m.refreshLocalStates()
		if err := m.refreshRemoteStates(r.Context(), instanceFilter); err != nil {
			writeAPIError(w, err)
			return
		}
		instances := m.list()
		response := make([]instanceResponse, 0, len(instances))
		for _, instance := range instances {
			response = append(response, m.publicInstance(instance))
		}
		for _, instance := range response {
			if instance.DeviceID == "" || (instanceFilter != "" && instance.ID != instanceFilter) {
				continue
			}
			m.appendRemoteDebugLog(instance.ID, fmt.Sprintf(
				"STATUS API response status=%q pid=%d actualPort=%d startedAt=%q error=%q",
				instance.Status, instance.PID, instance.ActualPort, instance.StartedAt, instance.Error,
			))
		}
		writeJSON(w, http.StatusOK, map[string]any{"instances": response})
		return
	}
	switch r.Method {
	case http.MethodGet:
		instances := m.list()
		response := make([]instanceResponse, 0, len(instances))
		for _, instance := range instances {
			response = append(response, m.publicInstance(instance))
		}
		writeJSON(w, http.StatusOK, map[string]any{"instances": response})
	case http.MethodPost:
		var input Instance
		if err := decodeJSON(w, r, &input); err != nil {
			writeAPIError(w, err)
			return
		}
		instance, err := m.create(input)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, m.publicInstance(instance))
	default:
		methodNotAllowed(w)
	}
}

func (m *instanceManager) handleInstance(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || !idPattern.MatchString(parts[0]) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": uiText("backend.manager.message069")})
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "icon" {
		if r.Method == http.MethodGet {
			imageData, err := m.readIcon(id)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(imageData)
			return
		}
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			IconData string `json:"iconData"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeAPIError(w, err)
			return
		}
		instance, err := m.updateIcon(id, input.IconData)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, m.publicInstance(instance))
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodPut:
			var input Instance
			if err := decodeJSON(w, r, &input); err != nil {
				writeAPIError(w, err)
				return
			}
			instance, err := m.update(id, input)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, m.publicInstance(instance))
		case http.MethodDelete:
			if err := m.delete(id); err != nil {
				writeAPIError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			methodNotAllowed(w)
		}
		return
	}
	if len(parts) != 2 {
		methodNotAllowed(w)
		return
	}
	if parts[1] == "logs" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		content, err := m.logs(id)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"logs": content})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	switch parts[1] {
	case "start":
		if err := m.start(id); err != nil {
			writeAPIError(w, err)
			return
		}
		instance, found := m.instanceByID(id)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": uiText("backend.manager.message013")})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"status": instance.Status, "instance": m.publicInstance(instance)})
	case "stop":
		if err := m.stop(id); err != nil {
			writeAPIError(w, err)
			return
		}
		instance, found := m.instanceByID(id)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": uiText("backend.manager.message013")})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": instance.Status, "instance": m.publicInstance(instance)})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": uiText("backend.manager.message070")})
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message071") + err.Error()}
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return apiError{status: http.StatusBadRequest, message: uiText("backend.manager.message072")}
	}
	return nil
}

type apiError struct {
	status  int
	message string
}

func (e apiError) Error() string { return e.message }

func writeAPIError(w http.ResponseWriter, err error) {
	var apiErr apiError
	if errors.As(err, &apiErr) {
		writeJSON(w, apiErr.status, map[string]string{"error": apiErr.message})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST, PUT")
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": uiText("backend.manager.message073")})
}
