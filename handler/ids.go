package handler

import (
	"fmt"

	"github.com/ka74mi/iptv/api"
)

// tvgID は M3U の tvg-id と XMLTV の channel id に共通で使う識別子。
func tvgID(onid, tsid, sid uint16) string {
	return fmt.Sprintf("%d.%d.%d", onid, tsid, sid)
}

// logoURL はロゴが取得できているサービスの画像URLを返す。無ければ空文字。
func logoURL(baseURL string, logos *api.LogoCache, onid, sid uint16) string {
	if logos.Get(onid, sid) == nil {
		return ""
	}
	return fmt.Sprintf("%s/logo/%d/%d.png", baseURL, onid, sid)
}
