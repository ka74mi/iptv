package handler

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/ka74mi/iptv/api"
)

type xmlTV struct {
	XMLName    xml.Name       `xml:"tv"`
	Channels   []xmlChannel   `xml:"channel"`
	Programmes []xmlProgramme `xml:"programme"`
}

type xmlChannel struct {
	ID          string `xml:"id,attr"`
	DisplayName string `xml:"display-name"`
	Icon        *xmlIcon     `xml:"icon,omitempty"`
}

type xmlProgramme struct {
	Start   string `xml:"start,attr"`
	Stop    string `xml:"stop,attr"`
	Channel string `xml:"channel,attr"`
	Title   string `xml:"title"`
	Desc    string `xml:"desc,omitempty"`
}

type xmlIcon struct {
	Src string `xml:"src,attr"`
}

func GenerateXMLTV(seis []api.ServiceEventInfo, baseURL string, logos *api.LogoCache) ([]byte, error) {
	// 7日分の実測値から 1サービスあたり約195番組のため *200 で確保
	tv := xmlTV{
		Channels:   make([]xmlChannel, 0, len(seis)),
		Programmes: make([]xmlProgramme, 0, len(seis)*200),
	}

	for _, sei := range seis {
		if sei.ServiceInfo.ServiceType != serviceTypeDigitalTV {
			continue
		}
		tvgID := fmt.Sprintf("%d.%d.%d",
			sei.ServiceInfo.Onid,
			sei.ServiceInfo.Tsid,
			sei.ServiceInfo.Sid,
		)
		ch := xmlChannel{
			ID:          tvgID,
			DisplayName: sei.ServiceInfo.ServiceName,
		}
		if logos != nil && logos.Get(sei.ServiceInfo.Onid, sei.ServiceInfo.Sid) != nil {
			ch.Icon = &xmlIcon{
				Src: fmt.Sprintf("%s/logo/%d/%d.png", baseURL, sei.ServiceInfo.Onid, sei.ServiceInfo.Sid),
			}
		}
		tv.Channels = append(tv.Channels, ch)

		for _, e := range sei.EventList {
			if !e.HasTime || !e.HasDuration {
				continue
			}
			stop := e.StartTime.Add(time.Duration(e.DurationSec) * time.Second)
			tv.Programmes = append(tv.Programmes, xmlProgramme{
				Start:   formatXMLTVTime(e.StartTime),
				Stop:    formatXMLTVTime(stop),
				Channel: tvgID,
				Title:   e.EventName,
				Desc:    e.TextChar,
			})
		}
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(tv); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func formatXMLTVTime(t time.Time) string {
	return t.Format("20060102150405 -0700")
}
