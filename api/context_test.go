package api

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// newServer は、接続ごとに handler を実行するTCPサーバーを立てる。
func newServer(t *testing.T, handler func(net.Conn)) *Client {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				handler(c)
			}()
		}
	}()
	return NewClient("127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
}

// 応答しないEDCBに対しても、キャンセルで直ちに戻る（既定のタイムアウト15秒を待たない）。
func TestSendCmdAbortsOnCancel(t *testing.T) {
	c := newServer(t, func(conn net.Conn) { io.Copy(io.Discard, conn) }) // 読むだけで応答しない

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := c.EnumService(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestSendCmdHonorsContextDeadline(t *testing.T) {
	c := newServer(t, func(conn net.Conn) { io.Copy(io.Discard, conn) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.EnumService(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestSendCmdWithCancelledContextDoesNotConnect(t *testing.T) {
	c := newServer(t, func(conn net.Conn) { t.Error("must not connect") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.EnumService(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// SrvPipeの準備待ちのリトライ中にキャンセルされたら、残りの待ち時間を使わずに戻る。
func TestOpenViewStreamWithRetryAbortsOnCancel(t *testing.T) {
	c := newServer(t, func(conn net.Conn) { // 常に ret=0（準備できていない）
		io.CopyN(io.Discard, conn, 12)
		conn.Write(make([]byte, 8))
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(250*time.Millisecond, cancel)

	start := time.Now()
	_, err := c.OpenViewStreamWithRetry(ctx, 1, 10*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

// 接続確立後は、リクエストのctxがキャンセルされてもストリームの接続は生きている
// （停止は呼び出し側が conn.Close で行う）。
func TestOpenViewStreamConnSurvivesLaterCancel(t *testing.T) {
	c := newServer(t, func(conn net.Conn) {
		io.CopyN(io.Discard, conn, 12)
		conn.Write(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, 1), 0)) // ret=1 size=0
		time.Sleep(300 * time.Millisecond)                                                        // キャンセルより後にデータを流す
		conn.Write([]byte("TSDATA"))
	})
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := c.OpenViewStreamWithRetry(ctx, 1, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()

	buf := make([]byte, 6)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "TSDATA" {
		t.Fatalf("read = %q, %v", buf, err)
	}
}
