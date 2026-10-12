package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestManagerShutdownRequiresAuthenticatedSameOriginPost(t *testing.T) {
	auth, err := newAuthManager(filepath.Join(t.TempDir(), "auth"))
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.createInitialAdmin("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	session, _, err := auth.login("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		method     string
		origin     string
		auth       bool
		wantStatus int
		wantSignal bool
	}{
		{name: "unauthenticated", method: http.MethodPost, origin: "http://example.com", wantStatus: http.StatusUnauthorized},
		{name: "cross origin", method: http.MethodPost, origin: "http://attacker.example", auth: true, wantStatus: http.StatusForbidden},
		{name: "wrong method", method: http.MethodGet, origin: "http://example.com", auth: true, wantStatus: http.StatusMethodNotAllowed},
		{name: "authorized", method: http.MethodPost, origin: "http://example.com", auth: true, wantStatus: http.StatusAccepted, wantSignal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requested := false
			handler := managerShutdownAPIHandler(auth, func() { requested = true })
			request := httptest.NewRequest(test.method, "/api/manager/shutdown", nil)
			request.Header.Set("Origin", test.origin)
			if test.auth {
				request.AddCookie(&http.Cookie{Name: managerSessionCookie, Value: session})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("shutdown returned %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if requested != test.wantSignal {
				t.Fatalf("shutdown signal = %t, want %t", requested, test.wantSignal)
			}
		})
	}
}
