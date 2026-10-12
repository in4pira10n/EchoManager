package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthSessionSurvivesRestartAndLogoutRevokesIt(t *testing.T) {
	dataDir := t.TempDir()
	auth, err := newAuthManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.createInitialAdmin("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	token, _, err := auth.login("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dataDir, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved managerSessionFile
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if record, ok := saved.Sessions[token]; !ok || record.Username != "admin" || record.Expires.IsZero() {
		t.Fatalf("persisted session = %+v, want username and expiry", record)
	}

	restarted, err := newAuthManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: managerSessionCookie, Value: token})
	if username, ok := restarted.currentUser(request); !ok || username != "admin" {
		t.Fatalf("session after restart = (%q, %t), want (admin, true)", username, ok)
	}
	if err := restarted.logout(request); err != nil {
		t.Fatal(err)
	}

	afterLogout, err := newAuthManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if username, ok := afterLogout.currentUser(request); ok {
		t.Fatalf("logged-out session remains valid after restart as %q", username)
	}
}
