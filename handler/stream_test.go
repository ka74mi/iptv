package handler

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ka74mi/iptv/api"
)

type fakeEDCB struct {
	ln     net.Listener
	mu     sync.Mutex
	setIDs []int32
	closed []int32

	// テスト用の障害注入
	setChHang atomic.Bool // SetChを受けたまま応答しない（接続が閉じられるまで待つ）
	viewFail  atomic.Bool // RELAY_VIEW_STREAM が常に失敗する（SrvPipeが準備できない状態）
}

func (f *fakeEDCB) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			h := make([]byte, 8)
			if _, err := io.ReadFull(c, h); err != nil {
				return
			}
			cmd := binary.LittleEndian.Uint32(h)
			size := binary.LittleEndian.Uint32(h[4:])
			p := make([]byte, size)
			io.ReadFull(c, p)
			resp := func(payload []byte) {
				o := make([]byte, 8)
				binary.LittleEndian.PutUint32(o, 1)
				binary.LittleEndian.PutUint32(o[4:], uint32(len(payload)))
				c.Write(append(o, payload...))
			}
			switch cmd {
			case 1073:
				// use_bon_ch / id / mode を検証
				id := int32(binary.LittleEndian.Uint32(p[4+4+6+4:]))
				mode := binary.LittleEndian.Uint32(p[4+4+6+8:])
				useBon := binary.LittleEndian.Uint32(p[4+4+6:])
				if useBon != 1 || mode != 2 {
					panic("bad setch")
				}
				f.mu.Lock()
				f.setIDs = append(f.setIDs, id)
				f.mu.Unlock()
				if f.setChHang.Load() {
					io.Copy(io.Discard, c)
					return
				}
				pl := make([]byte, 4)
				binary.LittleEndian.PutUint32(pl, 4242)
				resp(pl)
			case 1074:
				f.mu.Lock()
				f.closed = append(f.closed, int32(binary.LittleEndian.Uint32(p)))
				f.mu.Unlock()
				resp(nil)
			case 301:
				if f.viewFail.Load() {
					c.Write(append(binary.LittleEndian.AppendUint32(nil, 0), 0, 0, 0, 0)) // ret=0, size=0
					return
				}
				resp(nil)
				pkt := make([]byte, 188)
				pkt[0] = 0x47
				for {
					if _, err := c.Write(pkt); err != nil {
						return
					}
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
}

func TestStreamConcurrentAndCleanup(t *testing.T) {
	// 偽tsreadex（catで素通し）
	dir := t.TempDir()
	os.WriteFile(dir+"/tsreadex", []byte("#!/bin/sh\nexec cat\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	f := &fakeEDCB{ln: ln}
	go f.serve()
	port := ln.Addr().(*net.TCPAddr).Port
	cl := api.NewClient("127.0.0.1", port)

	srv := newStreamServer(cl, testServices(), 8)
	defer srv.Close()
	base := runtime.NumGoroutine()

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := http.Get(srv.URL + "/stream/1/2/" + strconv.Itoa(100+i)) // 100〜102
			if err != nil {
				t.Error(err)
				return
			}
			buf := make([]byte, 188*60)
			io.ReadFull(resp.Body, buf)
			resp.Body.Close()
		}(i)
	}
	wg.Wait()
	time.Sleep(1500 * time.Millisecond)

	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[int32]bool{}
	for _, id := range f.setIDs {
		seen[id] = true
	}
	if len(seen) != 3 || len(f.closed) != 3 {
		t.Fatalf("ids=%v closed=%v", f.setIDs, f.closed)
	}
	for _, id := range f.closed {
		if !seen[id] {
			t.Fatalf("closed unknown id %d", id)
		}
	}
	if n := runtime.NumGoroutine(); n > base+2 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutine leak: base=%d now=%d\n%s", base, n, buf[:runtime.Stack(buf, true)])
	}
}

// ---- 以降: ④ 受け付けるリクエストの検証 ----

func testServices() []api.ServiceInfo {
	return []api.ServiceInfo{
		{Onid: 1, Tsid: 2, Sid: 100, ServiceType: 0x01},
		{Onid: 1, Tsid: 2, Sid: 101, ServiceType: 0x01},
		{Onid: 1, Tsid: 2, Sid: 102, ServiceType: 0x01},
		{Onid: 1, Tsid: 2, Sid: 200, ServiceType: 0xC0}, // データ放送: 配信対象外
	}
}

func newStreamServer(cl *api.Client, services []api.ServiceInfo, max int) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc(StreamPattern, StreamHandler(cl, staticCatalog(services), max))
	return httptest.NewServer(mux)
}

func setupFake(t *testing.T) (*fakeEDCB, *api.Client) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(dir+"/tsreadex", []byte("#!/bin/sh\nexec cat\n"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	f := &fakeEDCB{ln: ln}
	go f.serve()
	return f, api.NewClient("127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
}

func (f *fakeEDCB) setChCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.setIDs)
}

func TestStreamRejectsInvalidRequests(t *testing.T) {
	f, cl := setupFake(t)
	srv := newStreamServer(cl, testServices(), 2)
	defer srv.Close()

	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/stream/1/2/999", http.StatusNotFound},       // 未知のサービス
		{"GET", "/stream/1/2/200", http.StatusNotFound},       // TV以外のサービス
		{"GET", "/stream/1/2/abc", http.StatusBadRequest},     // 数値でない
		{"GET", "/stream/1/2/70000", http.StatusBadRequest},   // uint16の範囲外
		{"GET", "/stream/1/2/100/extra", http.StatusNotFound}, // 余分なセグメント
		{"POST", "/stream/1/2/100", http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, resp.StatusCode, c.want)
		}
	}
	if n := f.setChCount(); n != 0 {
		t.Fatalf("EDCBにSetChが送られた: %d回", n)
	}
}

func TestStreamHeadDoesNotTune(t *testing.T) {
	f, cl := setupFake(t)
	srv := newStreamServer(cl, testServices(), 2)
	defer srv.Close()

	resp, err := http.Head(srv.URL + "/stream/1/2/100")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("HEAD = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if n := f.setChCount(); n != 0 {
		t.Fatalf("HEADでチューナーが起動した: %d回", n)
	}
}

func TestStreamConcurrencyLimit(t *testing.T) {
	f, cl := setupFake(t)
	srv := newStreamServer(cl, testServices(), 1)
	defer srv.Close()

	// 1本目: 開いたまま保持する
	resp1, err := http.Get(srv.URL + "/stream/1/2/100")
	if err != nil {
		t.Fatal(err)
	}
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("1本目 = %d", resp1.StatusCode)
	}
	io.ReadFull(resp1.Body, make([]byte, 188*10))

	// 2本目: 上限超過で503 + Retry-After、チューナーは起動しない
	resp2, err := http.Get(srv.URL + "/stream/1/2/101")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable || resp2.Header.Get("Retry-After") == "" {
		t.Fatalf("2本目 = %d Retry-After=%q", resp2.StatusCode, resp2.Header.Get("Retry-After"))
	}
	if n := f.setChCount(); n != 1 {
		t.Fatalf("SetCh回数 = %d, want 1", n)
	}

	// 1本目を閉じて枠が空いたら、再び受け付けられる
	resp1.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp3, err := http.Get(srv.URL + "/stream/1/2/101")
		if err != nil {
			t.Fatal(err)
		}
		code := resp3.StatusCode
		resp3.Body.Close()
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("枠が解放されない: %d", code)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// サーバー停止（リクエストの親コンテキストのキャンセル）でも、
// SrvPipe切断→チューナー終了まで行われること。
func TestStreamServerShutdownClosesTuner(t *testing.T) {
	f, cl := setupFake(t)
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	mux := http.NewServeMux()
	mux.HandleFunc(StreamPattern, StreamHandler(cl, staticCatalog(testServices()), 2))
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.BaseContext = func(net.Listener) context.Context { return baseCtx }
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream/1/2/100")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.ReadFull(resp.Body, make([]byte, 188*10))

	cancelBase()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.closed)
		f.mu.Unlock()
		if n == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("チューナーが閉じられなかった")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func staticCatalog(services []api.ServiceInfo) CatalogFunc {
	c := api.NewCatalog(services, nil)
	return func() *api.Catalog { return c }
}

// サービス一覧の初回取得前は503を返し、チューナーに触れない。
func TestStreamNotReadyBeforeCatalogLoaded(t *testing.T) {
	f, cl := setupFake(t)
	mux := http.NewServeMux()
	mux.HandleFunc(StreamPattern, StreamHandler(cl, func() *api.Catalog { return nil }, 2))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream/1/2/100")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status=%d Retry-After=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if n := f.setChCount(); n != 0 {
		t.Fatalf("SetCh回数 = %d", n)
	}
}

// 一覧が差し替えられると、再起動なしで新しいサービスが配信対象になる。
func TestStreamFollowsCatalogRefresh(t *testing.T) {
	_, cl := setupFake(t)
	var cur atomic.Pointer[api.Catalog]
	cur.Store(api.NewCatalog([]api.ServiceInfo{{Onid: 1, Tsid: 2, Sid: 100, ServiceType: 0x01}}, nil))
	mux := http.NewServeMux()
	mux.HandleFunc(StreamPattern, StreamHandler(cl, cur.Load, 2))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	status := func(sid string) int {
		req, _ := http.NewRequest("HEAD", srv.URL+"/stream/1/2/"+sid, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := status("555"); got != http.StatusNotFound {
		t.Fatalf("before = %d", got)
	}
	cur.Store(api.NewCatalog([]api.ServiceInfo{{Onid: 1, Tsid: 2, Sid: 555, ServiceType: 0x01}}, nil))
	if got := status("555"); got != http.StatusOK {
		t.Fatalf("after = %d", got)
	}
	if got := status("100"); got != http.StatusNotFound {
		t.Fatalf("removed service = %d", got)
	}
}

func (f *fakeEDCB) closedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.closed)
}

func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SrvPipeの準備待ち（最大10秒のリトライ）の最中にクライアントが切断したら、
// リトライを打ち切って直ちにチューナーを閉じる。
func TestStreamClientDisconnectDuringViewRetry(t *testing.T) {
	f, cl := setupFake(t)
	f.viewFail.Store(true)
	srv := newStreamServer(cl, testServices(), 2)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/stream/1/2/100", nil)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		errCh <- err
	}()

	waitUntil(t, 3*time.Second, "SetCh", func() bool { return f.setChCount() == 1 })
	time.Sleep(300 * time.Millisecond) // リトライに入るのを待つ
	start := time.Now()
	cancel()
	// 10秒のリトライ満了を待たずに、チューナーが閉じられる
	waitUntil(t, 3*time.Second, "tuner close", func() bool { return f.closedCount() == 1 })
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("closing took %v after disconnect", d)
	}
	<-errCh
}

// SetChの応答待ち中に切断された場合も、EDCB側で起動済みの可能性があるため閉じる。
func TestStreamClientDisconnectDuringSetCh(t *testing.T) {
	f, cl := setupFake(t)
	f.setChHang.Store(true)
	srv := newStreamServer(cl, testServices(), 2)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/stream/1/2/100", nil)
	go func() {
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	waitUntil(t, 3*time.Second, "SetCh", func() bool { return f.setChCount() == 1 })
	cancel()
	waitUntil(t, 3*time.Second, "tuner close", func() bool { return f.closedCount() == 1 })

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed[0] != f.setIDs[0] {
		t.Fatalf("closed %d, want %d", f.closed[0], f.setIDs[0])
	}
}
