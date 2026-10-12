package server

import "net/http"

func managerShutdownAPIHandler(auth *authManager, requestShutdown func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "不支持此请求方法。"})
			return
		}
		if !sameOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "跨站请求已拒绝。"})
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "请先登录。"})
			return
		}
		if _, ok := auth.currentUser(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "登录状态已失效，请重新登录。"})
			return
		}

		writeJSON(w, http.StatusAccepted, map[string]string{"message": "EchoManager 正在关闭；受管进程将继续运行。"})
		if requestShutdown != nil {
			requestShutdown()
		}
	})
}
