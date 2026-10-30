package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		out = append(out, rec)
	}
	return out
}

func findRecord(recs []map[string]any, msg string) map[string]any {
	for _, r := range recs {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

// 1本の配信のログは、同じ nwtv_id と、サービスを示す属性を持つ。
func TestStreamLogsCarryStructuredAttributes(t *testing.T) {
	var out lockedBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	f, cl := setupFake(t)
	srv := newStreamServer(cl, testServices(), 2)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stream/1/2/100")
	if err != nil {
		t.Fatal(err)
	}
	io.ReadFull(resp.Body, make([]byte, 188*60))
	resp.Body.Close()
	waitUntil(t, 3*time.Second, "tuner close", func() bool { return f.closedCount() == 1 })
	waitUntil(t, 3*time.Second, "close log", func() bool { return findRecord(out.records(t), "tuner closed") != nil })

	recs := out.records(t)
	req := findRecord(recs, "stream request")
	if req == nil {
		t.Fatalf("no 'stream request' record: %v", recs)
	}
	if req["level"] != "INFO" || req["onid"] != float64(1) || req["tsid"] != float64(2) || req["sid"] != float64(100) ||
		req["remote"] == nil || req["nwtv_id"] == nil {
		t.Fatalf("request record = %v", req)
	}
	closed := findRecord(recs, "tuner closed")
	if closed["nwtv_id"] != req["nwtv_id"] || closed["process_id"] != float64(4242) {
		t.Fatalf("closed record = %v (request %v)", closed, req)
	}
	if findRecord(recs, "stream stopped") == nil {
		t.Fatalf("no 'stream stopped' record: %v", recs)
	}
}

// 上限超過はWarnで記録され、チューナーは起動しない。
func TestStreamRejectionIsLoggedAsWarning(t *testing.T) {
	var out lockedBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	defer slog.SetDefault(prev)

	_, cl := setupFake(t)
	srv := newStreamServer(cl, testServices(), 1)
	defer srv.Close()

	resp1, err := http.Get(srv.URL + "/stream/1/2/100")
	if err != nil {
		t.Fatal(err)
	}
	defer resp1.Body.Close()
	io.ReadFull(resp1.Body, make([]byte, 188*10))
	resp2, err := http.Get(srv.URL + "/stream/1/2/101")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()

	rej := findRecord(out.records(t), "stream rejected: too many streams")
	if rej == nil || rej["level"] != "WARN" || rej["limit"] != float64(1) || rej["sid"] != float64(101) {
		t.Fatalf("rejection record = %v", rej)
	}
}
