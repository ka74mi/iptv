package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ka74mi/iptv/api"
	"github.com/ka74mi/iptv/handler"
)

func main() {
	edcbHost := getEnv("EDCB_HOST", "edcb")
	edcbPort, err := strconv.Atoi(getEnv("EDCB_PORT", "4510"))
	if err != nil {
		log.Fatalf("invalid EDCB_PORT: %v", err)
	}

	// BASE_URL はスキームを含む完全なURLで指定する。
	// 例: http://192.168.0.100:8080 または https://your-host.ts.net
	baseURL := getEnv("BASE_URL", "http://localhost:8080")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		log.Fatalf("BASE_URL must start with http:// or https://: %q", baseURL)
	}

	client := api.NewClient(edcbHost, edcbPort)

	services, err := client.EnumService()
	if err != nil {
		log.Fatalf("EnumService error: %v", err)
	}
	logos, err := api.NewLogoCache(client, services)
	if err != nil {
		log.Printf("NewLogoCache error: %v", err)
	}

	http.HandleFunc("/playlist", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegurl")
		if _, err := w.Write([]byte(handler.GenerateM3U(services, baseURL, logos))); err != nil {
			log.Printf("write error: %v", err)
		}
	})

	http.HandleFunc("/epg", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		seis, err := client.EnumPgInfoEx(now, now.Add(7*24*time.Hour))
		if err != nil {
			log.Printf("EnumPgInfoEx error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		xmltv, err := handler.GenerateXMLTV(seis, baseURL, logos)
		if err != nil {
			log.Printf("GenerateXMLTV error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if _, err := w.Write(xmltv); err != nil {
			log.Printf("write error: %v", err)
		}
	})

	http.HandleFunc("/stream/", handler.StreamHandler(client))
	http.HandleFunc("/logo/", handler.LogoHandler(logos))

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
