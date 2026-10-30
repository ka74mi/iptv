package handler

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ka74mi/iptv/api"
)

func testCatalog() *api.Catalog {
	logos := api.NewLogoCacheWith(map[[2]uint16][]byte{{4, 101}: []byte("PNG-DATA")})
	return api.NewCatalog([]api.ServiceInfo{
		{Onid: 4, Tsid: 16625, Sid: 101, ServiceType: 0x01, ServiceName: "ＮＨＫ\r\n総合\"1\""},
		{Onid: 4, Tsid: 16625, Sid: 102, ServiceType: 0xC0, ServiceName: "データ"},
		{Onid: 32736, Tsid: 32736, Sid: 1024, ServiceType: 0x01, ServiceName: "地上局"},
	}, logos)
}

func TestGenerateM3U(t *testing.T) {
	c := testCatalog()
	out := GenerateM3U(c.Services, "http://h:8080", c.Logos)

	if !strings.HasPrefix(out, "#EXTM3U\n") {
		t.Fatalf("missing header: %q", out)
	}
	// 行を壊す制御文字は除去され、属性内のクォートは置き換えられる
	for _, line := range strings.Split(out, "\n") {
		if strings.ContainsAny(line, "\r") {
			t.Fatalf("control char left in %q", line)
		}
	}
	want := []string{
		`#EXTINF:-1 tvg-id="4.16625.101" tvg-name="ＮＨＫ  総合'1'" tvg-logo="http://h:8080/logo/4/101.png" group-title="BS",ＮＨＫ  総合"1"`,
		"http://h:8080/stream/4/16625/101\n",
		`tvg-id="32736.32736.1024" tvg-name="地上局" tvg-logo="" group-title="GR",地上局`,
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, "データ") {
		t.Error("non-TV service must be excluded")
	}
	if n := strings.Count(out, "#EXTINF"); n != 2 {
		t.Errorf("EXTINF count = %d, want 2", n)
	}
}

func TestGenerateXMLTV(t *testing.T) {
	c := testCatalog()
	start := time.Date(2026, 10, 28, 21, 0, 0, 0, time.FixedZone("JST", 9*3600))
	seis := []api.ServiceEventInfo{
		{ServiceInfo: c.Services[0], EventList: []api.EventInfo{
			{HasTime: true, StartTime: start, HasDuration: true, DurationSec: 1800, EventName: "ニュース & 天気", TextChar: "<概要>"},
			{HasTime: false, HasDuration: true, DurationSec: 60, EventName: "時刻なし"},
		}},
		{ServiceInfo: c.Services[1], EventList: []api.EventInfo{{HasTime: true, StartTime: start, HasDuration: true, DurationSec: 60}}},
	}
	b, err := GenerateXMLTV(seis, "http://h:8080", c.Logos)
	if err != nil {
		t.Fatal(err)
	}
	var tv xmlTV
	if err := xml.Unmarshal(b, &tv); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, b)
	}
	if len(tv.Channels) != 1 || tv.Channels[0].ID != "4.16625.101" ||
		tv.Channels[0].Icon == nil || tv.Channels[0].Icon.Src != "http://h:8080/logo/4/101.png" {
		t.Fatalf("channels = %+v", tv.Channels)
	}
	if len(tv.Programmes) != 1 {
		t.Fatalf("programmes = %+v", tv.Programmes)
	}
	p := tv.Programmes[0]
	if p.Start != "20261028210000 +0900" || p.Stop != "20261028213000 +0900" ||
		p.Channel != "4.16625.101" || p.Title != "ニュース & 天気" || p.Desc != "<概要>" {
		t.Fatalf("programme = %+v", p)
	}
}

func staticCat(c *api.Catalog) CatalogFunc { return func() *api.Catalog { return c } }

func do(h http.HandlerFunc, pattern, target string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.HandleFunc(pattern, h)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
	return w
}

func TestPlaylistHandler(t *testing.T) {
	w := do(PlaylistHandler(func() *api.Catalog { return nil }, "http://h"), PlaylistPattern, "/playlist")
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("not ready = %d", w.Code)
	}
	w = do(PlaylistHandler(staticCat(testCatalog()), "http://h"), PlaylistPattern, "/playlist")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/x-mpegurl") ||
		!strings.HasPrefix(w.Body.String(), "#EXTM3U") {
		t.Fatalf("playlist = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestLogoHandler(t *testing.T) {
	h := LogoHandler(staticCat(testCatalog()))
	for _, target := range []string{"/logo/4/101.png", "/logo/4/101"} {
		w := do(h, LogoPattern, target)
		if w.Code != 200 || w.Body.String() != "PNG-DATA" || w.Header().Get("Content-Type") != "image/png" ||
			w.Header().Get("Cache-Control") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s = %d %v", target, w.Code, w.Header())
		}
	}
	for _, target := range []string{"/logo/4/999.png", "/logo/x/101.png", "/logo/4/70000.png", "/logo/4/101.png/x"} {
		if w := do(h, LogoPattern, target); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, w.Code)
		}
	}
	// ロゴが一つも取得できていないカタログでも404（panicしない）
	empty := api.NewCatalog(testCatalog().Services, nil)
	if w := do(LogoHandler(staticCat(empty)), LogoPattern, "/logo/4/101.png"); w.Code != http.StatusNotFound {
		t.Errorf("no logos = %d", w.Code)
	}
	if w := do(LogoHandler(func() *api.Catalog { return nil }), LogoPattern, "/logo/4/101.png"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("not ready = %d", w.Code)
	}
}

func TestHealthHandler(t *testing.T) {
	w := do(HealthHandler(), HealthPattern, "/health")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("health = %d %q", w.Code, w.Body.String())
	}
}
