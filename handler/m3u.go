package handler

import (
	"fmt"
	"strings"

	"github.com/ka74mi/iptv/api"
)

func groupTitle(onid uint16) string {
	switch onid {
	case 4:
		return "BS"
	case 6:
		return "CS1"
	case 7:
		return "CS2"
	default:
		return "GR"
	}
}

func GenerateM3U(services []api.ServiceInfo, baseURL string) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	for _, s := range services {
		if s.ServiceType != 0x01 {
			continue
		}
		tvgID := fmt.Sprintf("%d.%d.%d", s.Onid, s.Tsid, s.Sid)
		sb.WriteString(fmt.Sprintf(
			"#KODIPROP:mimetype=video/mp2t\n#EXTINF:-1 tvg-id=%q tvg-name=%q group-title=%q,%s\n",
			tvgID, s.ServiceName, groupTitle(s.Onid), s.ServiceName,
		))
		sb.WriteString(fmt.Sprintf(
			"%s/stream/%d/%d/%d\n",
			baseURL, s.Onid, s.Tsid, s.Sid,
		))
	}
	return sb.String()
}
