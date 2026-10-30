package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ka74mi/iptv/api"
	"github.com/ka74mi/iptv/handler"
	"github.com/ka74mi/iptv/internal/listen"
)

func main() {
	// 最初にロガーを設定する。標準の log パッケージの出力も、これ経由になる。
	logger, err := newLogger(os.Stderr, getEnv("LOG_LEVEL", "info"), getEnv("LOG_FORMAT", "text"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	slog.SetDefault(logger)

	edcbHost := getEnv("EDCB_HOST", "edcb")
	edcbPort, err := strconv.Atoi(getEnv("EDCB_PORT", "4510"))
	if err != nil {
		fatal("invalid EDCB_PORT", "value", os.Getenv("EDCB_PORT"), "err", err)
	}

	// 待ち受けアドレス。コンテナでは既定の :8080 のまま、公開側のポートを -p などで
	// 変えるのが簡単。ホスト上で直接動かす場合などに、127.0.0.1:8080 のように絞れる。
	listenAddr, err := listen.Parse(getEnv("LISTEN_ADDR", listen.DefaultAddr))
	if err != nil {
		fatal("invalid LISTEN_ADDR", "value", os.Getenv("LISTEN_ADDR"), "err", err)
	}

	// BASE_URL はスキームを含む完全なURLで指定する（M3U/XMLTV内のURLの基準になる）。
	// 例: http://192.168.0.100:8080 または https://your-host.ts.net
	// 未指定のときは、待ち受けポートに合わせた localhost のURLになる。
	baseURL, err := normalizeBaseURL(getEnv("BASE_URL", "http://localhost:"+listen.Port(listenAddr)))
	if err != nil {
		fatal("invalid BASE_URL", "value", os.Getenv("BASE_URL"), "err", err)
	}

	// 同時に配信できるストリーム数の上限（チューナー数に合わせて設定する）。
	maxStreams, err := strconv.Atoi(getEnv("MAX_STREAMS", "4"))
	if err != nil || maxStreams < 1 {
		fatal("invalid MAX_STREAMS (must be an integer >= 1)", "value", os.Getenv("MAX_STREAMS"))
	}

	client := api.NewClient(edcbHost, edcbPort)

	// /epg の生成結果をキャッシュする時間。EDCBへの問い合わせとXML生成は重いため、
	// 番組表クライアントが頻繁に取りに来ても、この間は同じ結果を返す。0 でキャッシュしない。
	epgCacheTTL, err := time.ParseDuration(getEnv("EPG_CACHE_TTL", "5m"))
	if err != nil || epgCacheTTL < 0 {
		fatal("invalid EPG_CACHE_TTL (e.g. 5m, 30s, 0)", "value", os.Getenv("EPG_CACHE_TTL"))
	}

	// サービス一覧・ロゴは起動後にバックグラウンドで取得し、定期的に再取得する
	// （EDCBの再スキャンやロゴ更新に再起動なしで追従する）。
	// 0 を指定すると再取得しない。
	refreshInterval, err := time.ParseDuration(getEnv("REFRESH_INTERVAL", "1h"))
	if err != nil || refreshInterval < 0 {
		fatal("invalid REFRESH_INTERVAL (e.g. 1h, 30m, 0)", "value", os.Getenv("REFRESH_INTERVAL"))
	}

	// SIGINT/SIGTERM を受けたらグレースフルに停止する（配信中のチューナーを閉じてから終了）。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := &catalogStore{}
	go runCatalog(ctx, store, func(ctx context.Context, prev *api.Catalog) (*api.Catalog, error) {
		return api.LoadCatalog(ctx, client, prev)
	}, refreshInterval)

	mux := http.NewServeMux()
	mux.HandleFunc(handler.PlaylistPattern, handler.PlaylistHandler(store.get, baseURL))
	mux.HandleFunc(handler.EPGPattern, handler.EPGHandler(client, store.get, baseURL, epgCacheTTL))
	mux.HandleFunc(handler.StreamPattern, handler.StreamHandler(client, store.get, maxStreams))
	mux.HandleFunc(handler.LogoPattern, handler.LogoHandler(store.get))
	mux.HandleFunc(handler.HealthPattern, handler.HealthHandler())

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fatal("listen failed", "err", err)
	}
	slog.Info("listening", "addr", ln.Addr().String(), "base_url", baseURL, "max_streams", maxStreams)
	if err := serve(ctx, ln, mux); err != nil {
		fatal("server failed", "err", err)
	}
	slog.Info("stopped")
}

// normalizeBaseURL は BASE_URL を検証して正規化する。
//
// M3U/XMLTV 内のURLはすべてここから組み立てるため、末尾の "/" は取り除く
// （残すと "//stream/..." になる）。リバースプロキシのパスプレフィックス
// （例: https://host/secret）は、そのまま保持する。
func normalizeBaseURL(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("must start with http:// or https://: %q", s)
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host: %q", s)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("must not contain query, fragment or user info: %q", s)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
