package main

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// 停止要求で、配信中のハンドラが（リクエストのコンテキスト経由で）止まり、
// その後始末が終わってから serve が戻ること。
func TestServeGracefulShutdownCancelsInflightRequests(t *testing.T) {
	var cleaned atomic.Bool
	started := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()               // ストリーム配信中を模す（自然には終わらない）
		time.Sleep(200 * time.Millisecond) // チューナーを閉じる後始末を模す
		cleaned.Store(true)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, h) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started

	begin := time.Now()
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(shutdownTimeout + 2*time.Second):
		t.Fatal("serve did not return")
	}
	if !cleaned.Load() {
		t.Fatal("serve returned before the handler finished its cleanup")
	}
	if d := time.Since(begin); d > shutdownTimeout {
		t.Fatalf("shutdown took %v", d)
	}
}

// ヘッダーを送り切らない接続が、ReadHeaderTimeout で切断されること。
func TestServeClosesSlowHeaderConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about readHeaderTimeout")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go serve(ctx, ln, http.NotFoundHandler())

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n")) // ヘッダーを終端しない
	c.SetReadDeadline(time.Now().Add(readHeaderTimeout + 3*time.Second))
	buf := make([]byte, 1024)
	start := time.Now()
	for {
		if _, err := c.Read(buf); err != nil {
			break // サーバーが閉じた（400応答の後にEOF）
		}
	}
	if d := time.Since(start); d > readHeaderTimeout+2*time.Second {
		t.Fatalf("connection stayed open for %v", d)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	ok := map[string]string{
		"http://192.168.0.100:8080":   "http://192.168.0.100:8080",
		"http://192.168.0.100:8080/":  "http://192.168.0.100:8080",
		"https://host.ts.net//":       "https://host.ts.net",
		" https://h.example/secret/ ": "https://h.example/secret", // リバースプロキシのパスプレフィックスは保持
		"https://h.example/a/b/":      "https://h.example/a/b",
	}
	for in, want := range ok {
		got, err := normalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("normalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "localhost:8080", "ftp://h", "http://", "http://h?x=1", "http://h#frag", "http://user:pw@h", "://bad"} {
		if got, err := normalizeBaseURL(in); err == nil {
			t.Errorf("normalizeBaseURL(%q) = %q, want error", in, got)
		}
	}
}
