// Package listen は、待ち受けアドレス（LISTEN_ADDR）の検証と、
// それに対応するヘルスチェックURLの組み立てを提供する。
// サービス本体と healthcheck コマンドが、同じ解釈を共有するためのパッケージ。
package listen

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultAddr は LISTEN_ADDR が未指定のときの待ち受けアドレス（全インターフェースの8080番）。
const DefaultAddr = ":8080"

// Parse は host:port 形式の待ち受けアドレスを検証して返す。
// host は省略できる（":8080" は全インターフェース）。port は 1〜65535 の数値のみ。
func Parse(s string) (string, error) {
	s = strings.TrimSpace(s)
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("must be in host:port form (e.g. :8080 or 127.0.0.1:8080): %q", s)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("port must be a number between 1 and 65535: %q", s)
	}
	return net.JoinHostPort(host, port), nil
}

// Port は Parse で検証済みのアドレスからポート番号を返す。
func Port(addr string) string {
	_, port, _ := net.SplitHostPort(addr)
	return port
}

// HealthURL は、同じマシン（コンテナ内）から /health に到達するためのURLを返す。
// 全インターフェース指定（ホスト省略、0.0.0.0、::）の場合は localhost、
// 特定のアドレスを指定している場合はそのアドレスに接続する。
func HealthURL(addr string) string {
	host, port, _ := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health"
}
