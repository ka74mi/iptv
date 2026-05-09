package main

import (
	"net/http"
	"os"
)

// ヘルスチェック用バイナリ。/health にGETして200以外なら終了コード1を返す。
func main() {
	resp, err := http.Get("http://localhost:8080/health")
	if err != nil {
		os.Exit(1)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
