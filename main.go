package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ka74mi/iptv/api"
	"github.com/ka74mi/iptv/handler"
)

func main() {
	edcbHost := getEnv("EDCB_HOST", "edcb")
	edcbPort := 4510
	baseURL := getEnv("BASE_URL", "http://localhost:8080")

	client := api.NewClient(edcbHost, edcbPort)

	http.HandleFunc("/playlist", func(w http.ResponseWriter, r *http.Request) {
		services, err := client.EnumService()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-mpegurl")
		w.Write([]byte(handler.GenerateM3U(services, baseURL)))
	})

	http.HandleFunc("/epg", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		seis, err := client.EnumPgInfoEx(now, now.Add(7*24*time.Hour))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		xmltv, err := handler.GenerateXMLTV(seis)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.Write(xmltv)
	})

	http.HandleFunc("/stream/", handler.StreamHandler(client))

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
