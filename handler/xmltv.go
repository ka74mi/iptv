package handler

import (
	"bytes"
	"encoding/xml"
	"time"

	"github.com/ka74mi/iptv/api"
)

type xmlTV struct {
	XMLName    xml.Name       `xml:"tv"`
	Channels   []xmlChannel   `xml:"channel"`
	Programmes []xmlProgramme `xml:"programme"`
}

type xmlChannel struct {
	ID          string   `xml:"id,attr"`
	DisplayName string   `xml:"display-name"`
	Icon        *xmlIcon `xml:"icon,omitempty"`
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
	channels, programmes := 0, 0
	for _, sei := range seis {
		if sei.ServiceInfo.ServiceType == serviceTypeDigitalTV {
			channels++
			programmes += len(sei.EventList)
		}
	}
	tv := xmlTV{
		Channels:   make([]xmlChannel, 0, channels),
		Programmes: make([]xmlProgramme, 0, programmes),
	}

	for _, sei := range seis {
		if sei.ServiceInfo.ServiceType != serviceTypeDigitalTV {
			continue
		}
		si := sei.ServiceInfo
		id := tvgID(si.Onid, si.Tsid, si.Sid)
		ch := xmlChannel{ID: id, DisplayName: si.ServiceName}
		if src := logoURL(baseURL, logos, si.Onid, si.Sid); src != "" {
			ch.Icon = &xmlIcon{Src: src}
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
				Channel: id,
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
