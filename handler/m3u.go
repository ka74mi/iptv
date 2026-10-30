package handler

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/ka74mi/iptv/api"
)

// serviceTypeDigitalTV は ARIB STD-B10 で定義されるデジタルTVサービスの種別値。
// 映像サービス以外（データ放送等）を除外するために使用する。
const serviceTypeDigitalTV = api.ServiceTypeDigitalTV

func groupTitle(onid uint16) string {
	switch onid {
	case 4:
		return "BS"
	case 6:
		return "CS1"
	case 7:
		return "CS2"
	default:
		// 地上波は onid が放送局ごとに異なる値を持つため default で GR に分類する
		return "GR"
	}
}

// m3uText は M3U の1行に載せる文字列から、行を壊す制御文字（改行など）を取り除く。
func m3uText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// m3uAttr は M3U の属性値（ダブルクォートで囲む）用に整える。
// M3U にはクォートのエスケープ規則がないため、ダブルクォートは置き換える。
func m3uAttr(s string) string {
	return strings.ReplaceAll(m3uText(s), `"`, "'")
}

func GenerateM3U(services []api.ServiceInfo, baseURL string, logos *api.LogoCache) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for _, s := range services {
		if s.ServiceType != serviceTypeDigitalTV {
			continue
		}
		fmt.Fprintf(&sb,
			"#KODIPROP:mimetype=video/mp2t\n#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
			tvgID(s.Onid, s.Tsid, s.Sid), m3uAttr(s.ServiceName), logoURL(baseURL, logos, s.Onid, s.Sid),
			groupTitle(s.Onid), m3uText(s.ServiceName),
		)
		fmt.Fprintf(&sb, "%s/stream/%d/%d/%d\n", baseURL, s.Onid, s.Tsid, s.Sid)
	}
	return sb.String()
}
