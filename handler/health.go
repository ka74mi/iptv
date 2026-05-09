package handler

import (
	"encoding/json"
	"net/http"

	"github.com/ka74mi/iptv/api"
)

// HealthHandler はEDCBへの疎通確認を行い、結果をJSONで返す。
// EDCBが応答すれば200 OK、失敗すれば503 Service Unavailableを返す。
func HealthHandler(client *api.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := client.EnumService(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "ng", "error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
