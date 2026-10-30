package handler

import (
	"log/slog"
	"net/http"
)

// PlaylistPattern はプレイリスト（M3U）のルーティングパターン。
const PlaylistPattern = "GET /playlist"

// PlaylistHandler は現在のサービス一覧から M3U プレイリストを返すハンドラ。
// サービス一覧の初回取得前は503を返す。
func PlaylistHandler(catalog CatalogFunc, baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cat := catalog()
		if cat == nil {
			NotReady(w)
			return
		}
		w.Header().Set("Content-Type", "application/x-mpegurl; charset=utf-8")
		if _, err := w.Write([]byte(GenerateM3U(cat.Services, baseURL, cat.Logos))); err != nil {
			slog.Debug("playlist write failed", "err", err)
		}
	}
}
