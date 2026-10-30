package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ka74mi/iptv/internal/listen"
)

// ヘルスチェック用バイナリ。サービスと同じ LISTEN_ADDR の /health にGETして、
// 200以外（または接続できない）なら終了コード1を返す。
func main() {
	addr, err := listen.Parse(envOr("LISTEN_ADDR", listen.DefaultAddr))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid LISTEN_ADDR:", err)
		os.Exit(1)
	}
	// http.Get は既定でタイムアウトがないため、サーバーが応答しないと待ち続ける。
	client := &http.Client{Timeout: 3 * time.Second}
	if err := check(client, listen.HealthURL(addr)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// check は url にGETして、200が返ればnilを返す。
func check(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: %s returned %s", url, resp.Status)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
