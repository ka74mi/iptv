package handler

import (
	"net/http"

	"github.com/ka74mi/iptv/api"
)

// CatalogFunc は現在のサービス一覧・ロゴのスナップショットを返す。
// EDCBから最初の取得が完了するまでは nil を返す。
type CatalogFunc func() *api.Catalog

// NotReady は、サービス一覧の初回取得がまだ終わっていないときの応答を書く。
func NotReady(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	http.Error(w, "service list not loaded yet", http.StatusServiceUnavailable)
}
