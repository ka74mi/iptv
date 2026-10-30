package handler

import (
	"log/slog"
	"net/http"
	"strings"
)

// LogoPattern はロゴ画像のルーティングパターン。sid には ".png" が付いていてもよい。
const LogoPattern = "GET /logo/{onid}/{sid}"

func LogoHandler(catalog CatalogFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cat := catalog()
		if cat == nil {
			NotReady(w)
			return
		}
		onid, ok1 := parseUint16(r.PathValue("onid"))
		sid, ok2 := parseUint16(strings.TrimSuffix(r.PathValue("sid"), ".png"))
		if !ok1 || !ok2 {
			http.NotFound(w, r)
			return
		}
		data := cat.Logos.Get(onid, sid)
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// ロゴは滅多に変わらないため、クライアント側にキャッシュさせる。
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if _, err := w.Write(data); err != nil {
			slog.Debug("logo write failed", "err", err)
		}
	}
}
