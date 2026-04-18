package handler

import (
	"fmt"
	"strings"

	"github.com/ka74mi/iptv/api"
)

// serviceTypeDigitalTV は ARIB STD-B10 で定義されるデジタルTVサービスの種別値。
// 映像サービス以外（データ放送等）を除外するために使用する。
const serviceTypeDigitalTV = 0x01

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

func GenerateM3U(services []api.ServiceInfo, baseURL string) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for _, s := range services {
		if s.ServiceType != serviceTypeDigitalTV {
			continue
		}
		tvgID := fmt.Sprintf("%d.%d.%d", s.Onid, s.Tsid, s.Sid)
		sb.WriteString(fmt.Sprintf(
			"#KODIPROP:mimetype=video/mp2t\n#EXTINF:-1 tvg-id=%q tvg-name=%q group-title=%q,%s\n",
			tvgID, s.ServiceName, groupTitle(s.Onid), s.ServiceName,
		))
		// baseURL はスキームを含む完全なURL（例: http://192.168.0.100:18080）
		sb.WriteString(fmt.Sprintf("%s/stream/%d/%d/%d\n", baseURL, s.Onid, s.Tsid, s.Sid))
	}
	return sb.String()
}
