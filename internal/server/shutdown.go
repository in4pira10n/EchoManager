package server

import "net/http"

func managerShutdownAPIHandler(auth *authManager, requestShutdown func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": uiText("backend.manager.message073")})
			return
		}
		if !sameOriginRequest(r) {
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

		writeJSON(w, http.StatusAccepted, map[string]string{"message": uiText("backend.shutdown.message001")})
		if requestShutdown != nil {
			requestShutdown()
		}
	})
}
