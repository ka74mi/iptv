package handler

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ka74mi/iptv/api"
)

// EPGPattern はXMLTVのルーティングパターン。
const EPGPattern = "GET /epg"

const (
	// EDCBは番組の「開始時刻」で範囲を絞るため、現在放送中の番組（すでに開始済み）を
	// 含めるには始点を過去に遡らせる必要がある。長時間番組を考慮して余裕を持たせる。
	epgLookBehind = 12 * time.Hour
	epgLookAhead  = 7 * 24 * time.Hour

	// 取得に失敗した後、再び取得を試みるまでの間隔。
	// EDCBが応答しない間にリクエストが重なっても、毎回タイムアウトを待たせないための猶予。
	epgRetryBackoff = 30 * time.Second
	// EDCBに接続できないとき、期限切れのキャッシュを返し続ける上限。
	epgMaxStale = time.Hour
)

type epgFetchFunc func(ctx context.Context, from, to time.Time) ([]api.ServiceEventInfo, error)

// epgEntry は生成済みのXMLTV（読み取り専用）。gzip版は初回要求時に1度だけ作る。
type epgEntry struct {
	xml  []byte
	etag string // 引用符なしのハッシュ
	at   time.Time

	gzOnce sync.Once
	gz     []byte
	gzErr  error
}

func (e *epgEntry) gzipped() ([]byte, error) {
	e.gzOnce.Do(func() {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(e.xml); err != nil {
			e.gzErr = err
			return
		}
		e.gzErr = zw.Close()
		e.gz = buf.Bytes()
	})
	return e.gz, e.gzErr
}

type epgCache struct {
	fetch   epgFetchFunc
	catalog CatalogFunc
	baseURL string
	ttl     time.Duration
	now     func() time.Time

	cur atomic.Pointer[epgEntry] // 最新の生成結果（読み取りはロック不要）

	// 生成を直列化する（同時要求でもEDCBへの問い合わせは1回）。
	// sync.Mutex と違い、待っている間にクライアントが切断したら抜けられる。
	sem      chan struct{}
	failedAt time.Time
	lastErr  error
}

// EPGHandler は XMLTV を返すハンドラ。
//
// EDCBへの問い合わせとXML生成は重いため、結果を ttl の間キャッシュする。
//   - 期限切れ時に複数の要求が重なっても、問い合わせは1回だけ行う。
//   - EDCBに接続できないときは、期限切れでも一定時間（epgMaxStale）はキャッシュを返す。
//   - Accept-Encoding: gzip に対応し、ETag / If-None-Match で304を返す。
//
// ttl <= 0 の場合はキャッシュせず、要求ごとに取得する。
func EPGHandler(client *api.Client, catalog CatalogFunc, baseURL string, ttl time.Duration) http.HandlerFunc {
	c := newEPGCache(client.EnumPgInfoEx, catalog, baseURL, ttl, time.Now)
	return c.serveHTTP
}

func newEPGCache(fetch epgFetchFunc, catalog CatalogFunc, baseURL string, ttl time.Duration, now func() time.Time) *epgCache {
	return &epgCache{fetch: fetch, catalog: catalog, baseURL: baseURL, ttl: ttl, now: now, sem: make(chan struct{}, 1)}
}

func (c *epgCache) fresh(e *epgEntry) bool {
	return c.ttl > 0 && c.now().Sub(e.at) < c.ttl
}

func (c *epgCache) get(ctx context.Context) (*epgEntry, error) {
	if e := c.cur.Load(); e != nil && c.fresh(e) {
		return e, nil
	}

	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	old := c.cur.Load()
	if old != nil && c.fresh(old) {
		return old, nil // ロックを待っている間に他の要求が更新済み
	}

	now := c.now()
	if !c.failedAt.IsZero() && now.Sub(c.failedAt) < epgRetryBackoff {
		// 直前に失敗したばかりなので、再試行せずに結果を返す。
		if old != nil && now.Sub(old.at) < epgMaxStale {
			return old, nil
		}
		return nil, c.lastErr
	}

	e, err := c.build(ctx)
	if err != nil {
		c.failedAt, c.lastErr = now, err
		if old != nil && now.Sub(old.at) < epgMaxStale {
			slog.Warn("EPG refresh failed, serving stale cache", "age", now.Sub(old.at).Round(time.Second).String(), "err", err)
			return old, nil
		}
		return nil, err
	}
	c.failedAt, c.lastErr = time.Time{}, nil
	c.cur.Store(e)
	return e, nil
}

// build はEDCBから番組表を取得してXMLTVを生成する。
//
// 結果は他のリクエストにも共有されるため、取得そのものは、この要求のクライアントが
// 切断してもキャンセルしない（WithoutCancel）。EDCBへの問い合わせにはClient側のタイムアウトがある。
func (c *epgCache) build(ctx context.Context) (*epgEntry, error) {
	at := c.now()
	seis, err := c.fetch(context.WithoutCancel(ctx), at.Add(-epgLookBehind), at.Add(epgLookAhead))
	if err != nil {
		return nil, err
	}
	// ロゴは一覧の取得前でも番組表自体は返せるよう、無ければ nil（ロゴなし）にする。
	var logos *api.LogoCache
	if cat := c.catalog(); cat != nil {
		logos = cat.Logos
	}
	xmltv, err := GenerateXMLTV(seis, c.baseURL, logos)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(xmltv)
	return &epgEntry{xml: xmltv, etag: hex.EncodeToString(sum[:8]), at: at}, nil
}

func (c *epgCache) serveHTTP(w http.ResponseWriter, r *http.Request) {
	e, err := c.get(r.Context())
	if err != nil {
		if r.Context().Err() != nil {
			return // 待っている間にクライアントが切断した
		}
		slog.Error("EPG unavailable", "err", err)
		http.Error(w, "EDCB unavailable", http.StatusBadGateway)
		return
	}

	body, etag, gz := e.xml, `"`+e.etag+`"`, acceptsGzip(r.Header)
	if gz {
		if zb, err := e.gzipped(); err != nil {
			slog.Warn("EPG gzip failed, sending uncompressed", "err", err)
			gz = false
		} else {
			body, etag = zb, `"`+e.etag+`-gzip"`
		}
	}

	h := w.Header()
	h.Set("Vary", "Accept-Encoding")
	h.Set("ETag", etag)
	// キャッシュは毎回ETagで再検証させる（更新されていなければ304で済む）。
	h.Set("Cache-Control", "no-cache")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/xml; charset=utf-8")
	if gz {
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if _, err := w.Write(body); err != nil {
		slog.Debug("EPG write failed", "err", err)
	}
}

// acceptsGzip は Accept-Encoding が gzip を（q=0 でなく）受け付けているかを返す。
func acceptsGzip(h http.Header) bool {
	for _, v := range h.Values("Accept-Encoding") {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
				continue
			}
			q := 1.0
			for _, p := range strings.Split(params, ";") {
				if k, val, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
					if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
						q = f
					}
				}
			}
			return q > 0
		}
	}
	return false
}

// etagMatches は If-None-Match が etag に一致するか（弱い比較）を返す。
func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}
