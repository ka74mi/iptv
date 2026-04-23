package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/ka74mi/iptv/api"
)

func LogoHandler(logos *api.LogoCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if logos == nil {
			http.NotFound(w, r)
			return
		}
		// /logo/{onid}/{sid}.png
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/logo/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		onid, err := strconv.ParseUint(parts[0], 10, 16)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		sidStr := strings.TrimSuffix(parts[1], ".png")
		sid, err := strconv.ParseUint(sidStr, 10, 16)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		data := logos.Get(uint16(onid), uint16(sid))
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(data)
	}
}
