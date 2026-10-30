package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ka74mi/iptv/api"
)

type epgFixture struct {
	c      *epgCache
	fetchN atomic.Int32
	offset atomic.Int64 // 偽の時計（ナノ秒）
	errMu  sync.Mutex
	errVal error         // fetch が返すエラー（nilなら成功）
	gate   chan struct{} // 非nilなら fetch はこれが閉じるまで待つ
	from   atomic.Value  // time.Time
	to     atomic.Value
}

var epgBase = time.Date(2026, 10, 28, 12, 0, 0, 0, time.FixedZone("JST", 9*3600))

func newEPGFixture(ttl time.Duration) *epgFixture {
	f := &epgFixture{}
	fetch := func(_ context.Context, from, to time.Time) ([]api.ServiceEventInfo, error) {
		f.fetchN.Add(1)
		f.from.Store(from)
		f.to.Store(to)
		if f.gate != nil {
			<-f.gate
		}
		f.errMu.Lock()
		err := f.errVal
		f.errMu.Unlock()
		if err != nil {
			return nil, err
		}
		return []api.ServiceEventInfo{{
			ServiceInfo: api.ServiceInfo{Onid: 4, Tsid: 2, Sid: 100, ServiceType: api.ServiceTypeDigitalTV, ServiceName: "テスト"},
			EventList: []api.EventInfo{{
				StartTime: epgBase, HasTime: true, DurationSec: 1800, HasDuration: true,
				EventName: "番組", TextChar: "説明",
			}},
		}}, nil
	}
	now := func() time.Time { return epgBase.Add(time.Duration(f.offset.Load())) }
	f.c = newEPGCache(fetch, func() *api.Catalog { return nil }, "http://x", ttl, now)
	return f
}

func (f *epgFixture) setErr(err error) {
	f.errMu.Lock()
	f.errVal = err
	f.errMu.Unlock()
}

func (f *epgFixture) advance(d time.Duration) { f.offset.Add(int64(d)) }

func (f *epgFixture) get(hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/epg", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.c.serveHTTP(w, req)
	return w
}

func TestEPGCachesWithinTTL(t *testing.T) {
	f := newEPGFixture(5 * time.Minute)
	a := f.get(nil)
	b := f.get(nil)
	if a.Code != 200 || b.Code != 200 || !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Fatalf("codes %d %d", a.Code, b.Code)
	}
	if n := f.fetchN.Load(); n != 1 {
		t.Fatalf("fetch = %d, want 1", n)
	}
	f.advance(6 * time.Minute)
	f.get(nil)
	if n := f.fetchN.Load(); n != 2 {
		t.Fatalf("after TTL fetch = %d, want 2", n)
	}
	// 取得範囲: 放送中の番組を含めるため過去に遡り、7日先まで
	from, to := f.from.Load().(time.Time), f.to.Load().(time.Time)
	now := f.c.now()
	if !from.Equal(now.Add(-epgLookBehind)) || !to.Equal(now.Add(epgLookAhead)) {
		t.Fatalf("window = %v .. %v (now %v)", from, to, now)
	}
}

func TestEPGTTLZeroDisablesCache(t *testing.T) {
	f := newEPGFixture(0)
	f.get(nil)
	f.get(nil)
	if n := f.fetchN.Load(); n != 2 {
		t.Fatalf("fetch = %d, want 2", n)
	}
}

// 期限切れ時に要求が重なっても、EDCBへの問い合わせは1回だけ。
func TestEPGConcurrentRequestsFetchOnce(t *testing.T) {
	f := newEPGFixture(5 * time.Minute)
	f.gate = make(chan struct{})

	const n = 10
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes[i] = f.get(nil).Code }(i)
	}
	time.Sleep(100 * time.Millisecond) // 全員がロック待ちに入るのを待つ
	close(f.gate)
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("req %d = %d", i, c)
		}
	}
	if got := f.fetchN.Load(); got != 1 {
		t.Fatalf("fetch = %d, want 1", got)
	}
}

func TestEPGServesStaleOnErrorThenGivesUp(t *testing.T) {
	f := newEPGFixture(5 * time.Minute)
	first := f.get(nil).Body.String()

	f.setErr(errors.New("edcb down"))
	f.advance(6 * time.Minute) // 期限切れ
	w := f.get(nil)
	if w.Code != 200 || w.Body.String() != first {
		t.Fatalf("stale = %d", w.Code)
	}
	if n := f.fetchN.Load(); n != 2 {
		t.Fatalf("fetch = %d, want 2", n)
	}
	// 失敗直後は再試行しない（毎回タイムアウトを待たせない）
	f.get(nil)
	if n := f.fetchN.Load(); n != 2 {
		t.Fatalf("retried within backoff: fetch = %d", n)
	}
	// 猶予を過ぎたら再試行する
	f.advance(epgRetryBackoff + time.Second)
	f.get(nil)
	if n := f.fetchN.Load(); n != 3 {
		t.Fatalf("no retry after backoff: fetch = %d", n)
	}
	// 古すぎるキャッシュは返さない
	f.advance(epgMaxStale + time.Minute)
	if w := f.get(nil); w.Code != http.StatusBadGateway {
		t.Fatalf("too old = %d, want 502", w.Code)
	}
}

func TestEPGErrorWithoutCache(t *testing.T) {
	f := newEPGFixture(5 * time.Minute)
	f.setErr(errors.New("edcb down"))
	if w := f.get(nil); w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d", w.Code)
	}
	if w := f.get(nil); w.Code != http.StatusBadGateway { // バックオフ中は即座に失敗
		t.Fatalf("code = %d", w.Code)
	}
	if n := f.fetchN.Load(); n != 1 {
		t.Fatalf("fetch = %d, want 1", n)
	}
	// 復旧したら、猶予後に取得できる
	f.setErr(nil)
	f.advance(epgRetryBackoff + time.Second)
	if w := f.get(nil); w.Code != 200 {
		t.Fatalf("recovered = %d", w.Code)
	}
}

func TestEPGGzipNegotiation(t *testing.T) {
	f := newEPGFixture(5 * time.Minute)
	plain := f.get(nil)
	if plain.Header().Get("Content-Encoding") != "" || plain.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("plain headers: %v", plain.Header())
	}

	gz := f.get(map[string]string{"Accept-Encoding": "br, gzip;q=0.8"})
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip headers: %v", gz.Header())
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatal(err)
	}
	dec, _ := io.ReadAll(zr)
	if !bytes.Equal(dec, plain.Body.Bytes()) {
		t.Fatal("decompressed body differs from identity body")
	}
	if gz.Body.Len() >= plain.Body.Len() && plain.Body.Len() > 1000 {
		t.Fatalf("gzip not smaller: %d >= %d", gz.Body.Len(), plain.Body.Len())
	}
	if gz.Header().Get("ETag") == plain.Header().Get("ETag") {
		t.Fatal("ETag must differ between encodings")
	}

	for _, ae := range []string{"gzip;q=0", "identity", "deflate"} {
		if w := f.get(map[string]string{"Accept-Encoding": ae}); w.Header().Get("Content-Encoding") != "" {
			t.Errorf("Accept-Encoding %q got gzip", ae)
		}
	}
}

func TestEPGConditionalGet(t *testing.T) {
	f := newEPGFixture(5 * time.Minute)
	for _, ae := range []string{"", "gzip"} {
		first := f.get(map[string]string{"Accept-Encoding": ae})
		etag := first.Header().Get("ETag")
		w := f.get(map[string]string{"Accept-Encoding": ae, "If-None-Match": etag})
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Fatalf("ae=%q: %d body=%d", ae, w.Code, w.Body.Len())
		}
		if w := f.get(map[string]string{"Accept-Encoding": ae, "If-None-Match": `"other"`}); w.Code != 200 {
			t.Fatalf("mismatch = %d", w.Code)
		}
	}
	// 内容が変われば（再生成されて別のETagになれば）200で返る
	old := f.get(nil).Header().Get("ETag")
	f.advance(6 * time.Minute)
	f.c.fetch = func(_ context.Context, from, to time.Time) ([]api.ServiceEventInfo, error) {
		return []api.ServiceEventInfo{{ServiceInfo: api.ServiceInfo{Onid: 4, Sid: 1, ServiceType: api.ServiceTypeDigitalTV, ServiceName: "別"}}}, nil
	}
	if w := f.get(map[string]string{"If-None-Match": old}); w.Code != 200 {
		t.Fatalf("changed content = %d", w.Code)
	}
}

// 生成を待っている間にクライアントが切断したら、待たずに抜ける（生成自体は続く）。
func TestEPGWaiterLeavesOnCancel(t *testing.T) {
	f := newEPGFixture(5 * time.Minute)
	f.gate = make(chan struct{})

	builder := make(chan int, 1)
	go func() { builder <- f.get(nil).Code }() // 生成を始めて、fetchで止まる
	waitUntilTrue(t, func() bool { return f.fetchN.Load() == 1 })

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/epg", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { f.c.serveHTTP(w, req); close(done) }()

	time.Sleep(50 * time.Millisecond) // ロック待ちに入るのを待つ
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not leave after cancel")
	}
	if w.Body.Len() != 0 {
		t.Fatalf("body written for a cancelled request: %d bytes", w.Body.Len())
	}

	close(f.gate) // 生成は続行され、先頭の要求は成功する
	if code := <-builder; code != 200 {
		t.Fatalf("builder = %d", code)
	}
	if n := f.fetchN.Load(); n != 1 {
		t.Fatalf("fetch = %d, want 1", n)
	}
}

func waitUntilTrue(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
