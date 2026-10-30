package handler

import "net/http"

// HealthPattern はヘルスチェックのルーティングパターン。
const HealthPattern = "GET /health"

// HealthHandler はプロセスが応答できることだけを返す（EDCBの状態には依存しない）。
func HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}` + "\n"))
	}
}
