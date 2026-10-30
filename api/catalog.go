package api

import (
	"context"
	"fmt"
	"log/slog"
)

// ServiceTypeDigitalTV は ARIB STD-B10 で定義されるデジタルTVサービスの種別値。
const ServiceTypeDigitalTV = 0x01

// Catalog はEDCBから取得したサービス一覧とロゴの一式（読み取り専用のスナップショット）。
// 再取得時は新しい Catalog を作って丸ごと差し替える（作成後は変更しない）。
type Catalog struct {
	Services []ServiceInfo
	Logos    *LogoCache // ロゴを取得できなかった場合は nil

	tv map[[3]uint16]struct{} // 配信対象（デジタルTV）の onid/tsid/sid
}

// NewCatalog はサービス一覧とロゴからスナップショットを作る。
func NewCatalog(services []ServiceInfo, logos *LogoCache) *Catalog {
	c := &Catalog{
		Services: services,
		Logos:    logos,
		tv:       make(map[[3]uint16]struct{}, len(services)),
	}
	for _, s := range services {
		if s.ServiceType == ServiceTypeDigitalTV {
			c.tv[[3]uint16{s.Onid, s.Tsid, s.Sid}] = struct{}{}
		}
	}
	return c
}

// IsTVService は配信対象（デジタルTV）のサービスかどうかを返す。
func (c *Catalog) IsTVService(onid, tsid, sid uint16) bool {
	_, ok := c.tv[[3]uint16{onid, tsid, sid}]
	return ok
}

// LoadCatalog はEDCBからサービス一覧とロゴを取得する。
//
// prev は直前のスナップショット（初回は nil）。ロゴの取得だけが失敗した場合は、
// サービス一覧は新しいものを使い、ロゴは prev のものを引き継ぐ。
// サービス一覧が取得できない、または空の場合はエラーを返す
// （EDCBの起動直後などに空の一覧が返っても、正常な一覧を上書きしないため）。
func LoadCatalog(ctx context.Context, client *Client, prev *Catalog) (*Catalog, error) {
	services, err := client.EnumService(ctx)
	if err != nil {
		return nil, fmt.Errorf("EnumService: %w", err)
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("EnumService: empty service list")
	}
	logos, err := NewLogoCache(ctx, client, services)
	if err != nil {
		slog.Warn("loading logos failed, keeping previous logos", "err", err)
		logos = nil
		if prev != nil {
			logos = prev.Logos
		}
	}
	return NewCatalog(services, logos), nil
}
