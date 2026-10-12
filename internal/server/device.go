package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	localDeviceID       = "local"
	deviceStatusUnknown = "unknown"
	deviceStatusOnline  = "online"
	deviceStatusOffline = "offline"
)

type Device struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Address     string `json:"address"`
	Platform    string `json:"platform"`
	Transport   string `json:"transport"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	AuthType    string `json:"authType"`
	KeyPath     string `json:"keyPath,omitempty"`
	InsecureTLS bool   `json:"insecureTLS,omitempty"`
	Password    string `json:"-"`
	Status      string `json:"status"`
	LastSeenAt  string `json:"lastSeenAt,omitempty"`
	Error       string `json:"error,omitempty"`
}

type deviceInput struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Platform    string `json:"platform"`
	Transport   string `json:"transport"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	AuthType    string `json:"authType"`
	KeyPath     string `json:"keyPath"`
	Password    string `json:"password"`
	InsecureTLS bool   `json:"insecureTLS"`
}

type deviceResponse struct {
	Device
	PasswordConfigured bool `json:"passwordConfigured"`
	InstanceCount      int  `json:"instanceCount"`
}

var deviceAddressPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

func (m *instanceManager) deviceIndexLocked(id string) int {
	for i := range m.devices {
		if m.devices[i].ID == id {
			return i
		}
	}
	return -1
}

func (m *instanceManager) deviceLocked(id string) (Device, bool) {
	index := m.deviceIndexLocked(id)
	if index < 0 {
		return Device{}, false
	}
	return m.devices[index], true
}

func normalizeDeviceInput(input deviceInput) Device {
	device := Device{
		Name:        strings.TrimSpace(input.Name),
		Address:     strings.Trim(strings.TrimSpace(input.Address), "[]"),
		Platform:    strings.ToLower(strings.TrimSpace(input.Platform)),
		Transport:   strings.ToLower(strings.TrimSpace(input.Transport)),
		Port:        input.Port,
		Username:    strings.TrimSpace(input.Username),
		AuthType:    strings.ToLower(strings.TrimSpace(input.AuthType)),
		KeyPath:     strings.TrimSpace(input.KeyPath),
		Password:    input.Password,
		InsecureTLS: input.InsecureTLS,
		Status:      deviceStatusUnknown,
	}
	if device.Platform == "macos" || device.Platform == "linux" || device.Platform == "darwin" {
		device.Platform = "unix"
	}
	if device.AuthType == "" {
		if device.KeyPath != "" {
			device.AuthType = "key"
		} else {
			device.AuthType = "password"
		}
	}
	if device.Port == 0 {
		if device.Transport == "winrm" {
			device.Port = 5986
		} else {
			device.Port = 22
		}
	}
	return device
}

func validateDevice(device *Device, requireSecret bool) error {
	device.Name = strings.TrimSpace(device.Name)
	device.Address = strings.Trim(strings.TrimSpace(device.Address), "[]")
	device.Username = strings.TrimSpace(device.Username)
	device.KeyPath = strings.TrimSpace(device.KeyPath)
	if device.Name == "" || len(device.Name) > 120 {
		return apiError{status: http.StatusBadRequest, message: "设备名称必填，且最多 120 个字符。"}
	}
	ipAddress := net.ParseIP(device.Address)
	if device.Address == "" || len(device.Address) > 253 || (ipAddress == nil && !deviceAddressPattern.MatchString(device.Address)) {
		return apiError{status: http.StatusBadRequest, message: "设备地址应填写 IP 地址或主机名，不要填写协议、路径或端口。"}
	}
	if ipAddress != nil && (ipAddress.IsUnspecified() || ipAddress.IsMulticast()) {
		return apiError{status: http.StatusBadRequest, message: "设备地址不能是未指定或组播地址。"}
	}
	if device.Platform != "unix" && device.Platform != "windows" {
		return apiError{status: http.StatusBadRequest, message: "目标系统请选择 macOS / Linux 或 Windows。"}
	}
	if device.Transport != "ssh" && device.Transport != "winrm" {
		return apiError{status: http.StatusBadRequest, message: "远程连接方式请选择 SSH 或 WinRM。"}
	}
	if device.Transport == "winrm" && device.Platform != "windows" {
		return apiError{status: http.StatusBadRequest, message: "WinRM 仅支持 Windows 目标设备。"}
	}
	if device.Transport == "ssh" && device.Platform == "windows" {
		return apiError{status: http.StatusBadRequest, message: "Windows 设备请使用 HTTPS WinRM；SSH 远程进程控制暂不支持 Windows Shell。"}
	}
	if device.Port < 1 || device.Port > 65535 {
		return apiError{status: http.StatusBadRequest, message: "连接端口必须在 1–65535 范围内。"}
	}
	if device.Username == "" || len(device.Username) > 256 {
		return apiError{status: http.StatusBadRequest, message: "远程登录用户名必填，且最多 256 个字符。"}
	}
	if device.Transport == "ssh" {
		if device.AuthType != "password" && device.AuthType != "key" {
			return apiError{status: http.StatusBadRequest, message: "SSH 认证方式请选择密码或私钥。"}
		}
		if device.AuthType == "key" {
			if device.KeyPath == "" || !filepath.IsAbs(device.KeyPath) {
				return apiError{status: http.StatusBadRequest, message: "SSH 私钥路径必须是 Manager 主机上的绝对路径。"}
			}
			if _, err := os.Stat(device.KeyPath); err != nil {
				return apiError{status: http.StatusBadRequest, message: "无法读取 SSH 私钥：" + err.Error()}
			}
		} else if requireSecret && device.Password == "" {
			return apiError{status: http.StatusBadRequest, message: "SSH 密码认证需要填写密码。"}
		}
	} else if device.Password == "" && requireSecret {
		return apiError{status: http.StatusBadRequest, message: "WinRM 需要填写密码。"}
	}
	if len(device.Password) > 4096 {
		return apiError{status: http.StatusBadRequest, message: "凭据长度不能超过 4096 个字符。"}
	}
	return nil
}

func validateTargetExecutable(path, platform string) error {
	path = strings.TrimSpace(path)
	if platform == "windows" {
		if !regexp.MustCompile(`^(?:[A-Za-z]:\\|\\\\)[^\x00]*$`).MatchString(path) {
			return errors.New("Windows 目标程序路径必须是盘符路径或 UNC 绝对路径。")
		}
		return nil
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsRune(path, '\x00') {
		return errors.New("macOS / Linux 目标程序路径必须是以 / 开头的绝对路径。")
	}
	return nil
}

func (m *instanceManager) platformForDevice(id string) (string, error) {
	if id == "" || id == localDeviceID {
		return runtime.GOOS, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	device, ok := m.deviceLocked(id)
	if !ok {
		return "", apiError{status: http.StatusBadRequest, message: "所选目标设备不存在；请刷新设备列表。"}
	}
	return device.Platform, nil
}

func (m *instanceManager) createDevice(input deviceInput) (Device, error) {
	device := normalizeDeviceInput(input)
	if err := validateDevice(&device, true); err != nil {
		return Device{}, err
	}
	id, err := randomID()
	if err != nil {
		return Device{}, err
	}
	device.ID = id
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices = append(m.devices, device)
	if err := m.saveLocked(); err != nil {
		m.devices = m.devices[:len(m.devices)-1]
		return Device{}, err
	}
	return device, nil
}

func (m *instanceManager) updateDevice(id string, input deviceInput) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.deviceIndexLocked(id)
	if index < 0 {
		return Device{}, apiError{status: http.StatusNotFound, message: "找不到此设备。"}
	}
	for _, instance := range m.instances {
		if instance.DeviceID == id && (instance.PID > 0 || isActiveStatus(instance.Status)) {
			return Device{}, apiError{status: http.StatusConflict, message: "此设备仍有运行中的服务，请先停止服务再修改连接配置。"}
		}
	}
	device := normalizeDeviceInput(input)
	device.ID = id
	previous := m.devices[index]
	if device.Password == "" {
		device.Password = previous.Password
	}
	if err := validateDevice(&device, true); err != nil {
		return Device{}, err
	}
	m.devices[index] = device
	if err := m.saveLocked(); err != nil {
		m.devices[index] = previous
		return Device{}, err
	}
	return device, nil
}

func (m *instanceManager) deleteDevice(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.deviceIndexLocked(id)
	if index < 0 {
		return apiError{status: http.StatusNotFound, message: "找不到此设备。"}
	}
	for _, instance := range m.instances {
		if instance.DeviceID == id {
			return apiError{status: http.StatusConflict, message: "此设备仍关联服务实例；请先删除或迁移这些实例。"}
		}
	}
	previous := append([]Device(nil), m.devices...)
	m.devices = append(m.devices[:index], m.devices[index+1:]...)
	if err := m.saveLocked(); err != nil {
		m.devices = previous
		return err
	}
	return nil
}

func (m *instanceManager) listDeviceResponses() []deviceResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := make(map[string]int)
	for _, instance := range m.instances {
		id := instance.DeviceID
		if id == "" {
			id = localDeviceID
		}
		counts[id]++
	}

	hostname, _ := os.Hostname()
	localAddress := firstLANIPv4()
	local := Device{
		ID: localDeviceID, Name: hostname, Address: localAddress,
		Platform: runtime.GOOS, Transport: "local", Status: deviceStatusOnline,
	}
	result := []deviceResponse{{Device: local, InstanceCount: counts[localDeviceID]}}
	for _, device := range m.devices {
		result = append(result, deviceResponse{
			Device: device, PasswordConfigured: device.Password != "", InstanceCount: counts[device.ID],
		})
	}
	return result
}

func publicDevice(device Device, instanceCount int) deviceResponse {
	return deviceResponse{Device: device, PasswordConfigured: device.Password != "", InstanceCount: instanceCount}
}

func (m *instanceManager) handleDevices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"devices": m.listDeviceResponses()})
	case http.MethodPost:
		var input deviceInput
		if err := decodeJSON(w, r, &input); err != nil {
			writeAPIError(w, err)
			return
		}
		device, err := m.createDevice(input)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		response := m.deviceResponsesByID(device.ID)
		writeJSON(w, http.StatusCreated, response)
	default:
		methodNotAllowed(w)
	}
}

func (m *instanceManager) handleDevice(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || !idPattern.MatchString(parts[0]) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "设备 ID 无效。"})
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "test" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		m.mu.Lock()
		device, found := m.deviceLocked(id)
		m.mu.Unlock()
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "找不到此设备。"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		_, err := runRemoteCommand(ctx, device, remoteProbeScript(device))
		m.setDeviceProbeResult(id, err)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": deviceError(err)})
			return
		}
		writeJSON(w, http.StatusOK, m.deviceResponsesByID(id))
		return
	}
	if len(parts) != 1 {
		methodNotAllowed(w)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var input deviceInput
		if err := decodeJSON(w, r, &input); err != nil {
			writeAPIError(w, err)
			return
		}
		if _, err := m.updateDevice(id, input); err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, m.deviceResponsesByID(id))
	case http.MethodDelete:
		if err := m.deleteDevice(id); err != nil {
			writeAPIError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w)
	}
}

func (m *instanceManager) deviceResponsesByID(id string) deviceResponse {
	for _, response := range m.listDeviceResponses() {
		if response.ID == id {
			return response
		}
	}
	return deviceResponse{}
}

func (m *instanceManager) setDeviceProbeResult(id string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.deviceIndexLocked(id)
	if index < 0 {
		return
	}
	device := &m.devices[index]
	if err != nil {
		device.Status = deviceStatusOffline
		device.Error = err.Error()
		return
	}
	device.Status = deviceStatusOnline
	device.Error = ""
	device.LastSeenAt = time.Now().Format(time.RFC3339)
}

func deviceError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("连接失败：%v", err)
}
