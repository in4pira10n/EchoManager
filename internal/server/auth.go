package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	managerSessionCookie = "em_session"
	managerSessionAge    = 7 * 24 * time.Hour
)

type managerUser struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

type managerUserFile struct {
	Users []managerUser `json:"users"`
}

type authManager struct {
	mu           sync.RWMutex
	path         string
	sessionsPath string
	users        []managerUser
	sessions     map[string]managerSession
}

type managerSession struct {
	username string
	expires  time.Time
}

type managerSessionRecord struct {
	Username string    `json:"username"`
	Expires  time.Time `json:"expires"`
}

type managerSessionFile struct {
	Sessions map[string]managerSessionRecord `json:"sessions"`
}

func newAuthManager(dataDir string) (*authManager, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create manager data directory: %w", err)
	}
	manager := &authManager{
		path:         filepath.Join(dataDir, "users.json"),
		sessionsPath: filepath.Join(dataDir, "sessions.json"),
		sessions:     make(map[string]managerSession),
	}
	data, err := os.ReadFile(manager.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read manager users: %w", err)
	}
	if len(data) > 0 {
		var saved managerUserFile
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, fmt.Errorf("parse manager users: %w", err)
		}
		for _, user := range saved.Users {
			if strings.TrimSpace(user.Username) == "" || user.PasswordHash == "" {
				continue
			}
			if _, exists := manager.findUserLocked(user.Username); !exists {
				manager.users = append(manager.users, user)
			}
		}
	}
	if err := manager.loadSessions(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (a *authManager) loadSessions() error {
	data, err := os.ReadFile(a.sessionsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read manager sessions: %w", err)
	}
	var saved managerSessionFile
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("parse manager sessions: %w", err)
	}
	now := time.Now()
	for token, record := range saved.Sessions {
		if token == "" || strings.TrimSpace(record.Username) == "" || !now.Before(record.Expires) {
			continue
		}
		if _, exists := a.findUserLocked(record.Username); !exists {
			continue
		}
		a.sessions[token] = managerSession{username: record.Username, expires: record.Expires}
	}
	if len(a.sessions) != len(saved.Sessions) {
		if err := a.saveSessionsLocked(); err != nil {
			return fmt.Errorf("clean expired manager sessions: %w", err)
		}
	}
	return nil
}

func (a *authManager) hasUsers() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.users) > 0
}

func (a *authManager) findUserLocked(username string) (managerUser, bool) {
	for _, user := range a.users {
		if strings.EqualFold(strings.TrimSpace(user.Username), strings.TrimSpace(username)) {
			return user, true
		}
	}
	return managerUser{}, false
}

func validateManagerCredentials(username, password string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 64 || strings.ContainsAny(username, "\r\n\x00") {
		return "", errors.New("用户名不能为空，且不能超过 64 个字符")
	}
	if len(password) < 6 {
		return "", errors.New("密码至少需要 6 个字符")
	}
	if len(password) > 72 {
		return "", errors.New("密码不能超过 72 个字节")
	}
	return username, nil
}

func (a *authManager) createInitialAdmin(username, password string) error {
	username, err := validateManagerCredentials(username, password)
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("生成密码摘要失败：%w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.users) != 0 {
		return errors.New("初始管理员已经创建，请登录后注册其他账号")
	}
	a.users = append(a.users, managerUser{Username: username, PasswordHash: string(hash)})
	if err := a.saveLocked(); err != nil {
		a.users = nil
		return err
	}
	return nil
}

func (a *authManager) registerAdmin(username, password, confirmPassword, adminUsername, adminPassword string) error {
	username, err := validateManagerCredentials(username, password)
	if err != nil {
		return err
	}
	if password != confirmPassword {
		return errors.New("两次输入的密码不一致")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("生成密码摘要失败：%w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	admin, exists := a.findUserLocked(adminUsername)
	if !exists || bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(adminPassword)) != nil {
		return errors.New("管理员用户名或密码错误")
	}
	if _, exists := a.findUserLocked(username); exists {
		return errors.New("此用户名已被使用")
	}
	a.users = append(a.users, managerUser{Username: username, PasswordHash: string(hash)})
	if err := a.saveLocked(); err != nil {
		a.users = a.users[:len(a.users)-1]
		return err
	}
	return nil
}

func (a *authManager) login(username, password string) (string, string, error) {
	a.mu.RLock()
	user, exists := a.findUserLocked(username)
	a.mu.RUnlock()
	if !exists || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return "", "", errors.New("用户名或密码错误")
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	sessionToken := base64.RawURLEncoding.EncodeToString(bytes)
	expires := time.Now().Add(managerSessionAge)
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, stored := range a.sessions {
		if !time.Now().Before(stored.expires) {
			delete(a.sessions, key)
		}
	}
	a.sessions[sessionToken] = managerSession{username: user.Username, expires: expires}
	if err := a.saveSessionsLocked(); err != nil {
		delete(a.sessions, sessionToken)
		return "", "", fmt.Errorf("save manager session: %w", err)
	}
	return sessionToken, user.Username, nil
}

func (a *authManager) currentUser(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(managerSessionCookie)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	a.mu.RLock()
	session, exists := a.sessions[cookie.Value]
	a.mu.RUnlock()
	return session.username, exists && time.Now().Before(session.expires)
}

func (a *authManager) logout(r *http.Request) error {
	cookie, err := r.Cookie(managerSessionCookie)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, exists := a.sessions[cookie.Value]
	if !exists {
		return nil
	}
	delete(a.sessions, cookie.Value)
	if err := a.saveSessionsLocked(); err != nil {
		a.sessions[cookie.Value] = session
		return fmt.Errorf("remove manager session: %w", err)
	}
	return nil
}

func (a *authManager) saveSessionsLocked() error {
	if len(a.sessions) == 0 {
		if err := os.Remove(a.sessionsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	sessions := make(map[string]managerSessionRecord, len(a.sessions))
	for token, session := range a.sessions {
		sessions[token] = managerSessionRecord{Username: session.username, Expires: session.expires}
	}
	data, err := json.MarshalIndent(managerSessionFile{Sessions: sessions}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(a.sessionsPath), ".sessions-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFile(name, a.sessionsPath)
}

func (a *authManager) saveLocked() error {
	data, err := json.MarshalIndent(managerUserFile{Users: a.users}, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(a.path), ".users-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return replaceFile(name, a.path)
}

func (a *authManager) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth/session" && r.Method == http.MethodGet {
			username, ok := a.currentUser(r)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "请先登录。"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"username": username, "role": "admin"})
			return
		}
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !sameOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "跨站请求已拒绝。"})
			return
		}
		switch r.URL.Path {
		case "/api/auth/login":
			var input struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := decodeJSON(w, r, &input); err != nil {
				writeAPIError(w, err)
				return
			}
			session, username, err := a.login(input.Username, input.Password)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
				return
			}
			setManagerSessionCookie(w, r, session)
			writeJSON(w, http.StatusOK, map[string]string{"username": username, "redirect": "/"})
		case "/api/auth/register":
			var input struct {
				Username        string `json:"username"`
				Password        string `json:"password"`
				ConfirmPassword string `json:"confirm_password"`
				AdminUsername   string `json:"admin_username"`
				AdminPassword   string `json:"admin_password"`
			}
			if err := decodeJSON(w, r, &input); err != nil {
				writeAPIError(w, err)
				return
			}
			if err := a.registerAdmin(input.Username, input.Password, input.ConfirmPassword, input.AdminUsername, input.AdminPassword); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"message": "管理员账号创建成功，请登录后继续。"})
		case "/api/auth/logout":
			if err := a.logout(r); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法安全结束登录会话：" + err.Error()})
				return
			}
			clearManagerSessionCookie(w, r)
			writeJSON(w, http.StatusOK, map[string]string{"message": "已退出登录。"})
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "未找到认证 API。"})
		}
	})
}

func setManagerSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name: managerSessionCookie, Value: value, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(managerSessionAge), MaxAge: int(managerSessionAge.Seconds()),
	})
}

func clearManagerSessionCookie(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{Name: managerSessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
