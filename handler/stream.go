package handler

import (
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/ka74mi/iptv/api"
)

func StreamHandler(client *api.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("stream request: %s", r.URL.Path)
		parts := splitPath(r.URL.Path)
		if len(parts) < 4 {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		onid, err1 := strconv.ParseUint(parts[1], 10, 16)
		tsid, err2 := strconv.ParseUint(parts[2], 10, 16)
		sid, err3 := strconv.ParseUint(parts[3], 10, 16)
		if err1 != nil || err2 != nil || err3 != nil {
			http.Error(w, "invalid ids", http.StatusBadRequest)
			return
		}

		if err := client.SendNwTVMode(2); err != nil {
			http.Error(w, "failed to set nwtv mode: "+err.Error(), http.StatusInternalServerError)
			return
		}

		nwtvID, err := client.SendNwTVIDSetCh(uint16(onid), uint16(tsid), uint16(sid))
		if err != nil {
			http.Error(w, "failed to set ch: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("nwtvID: %d", nwtvID)

        conn, err := client.OpenViewStream(nwtvID)
        if err != nil {
            client.SendNwTVIDClose(nwtvID)
            http.Error(w, "failed to open stream: "+err.Error(), http.StatusInternalServerError)
            return
        }
		defer conn.Close()
        defer func() {
            if err := client.SendNwTVIDClose(nwtvID); err != nil {
                log.Printf("SendNwTVIDClose error: %v", err)
            } else {
                log.Printf("SendNwTVIDClose success: nwtvID=%d", nwtvID)
            }
        }()

		ctx := r.Context()
		w.Header().Set("Content-Type", "video/mp2t")

		buf := make([]byte, 188*256)
		done := make(chan struct{})
		go func() {
			defer close(done)
			io.CopyBuffer(w, conn, buf)
		}()

		select {
        case <-ctx.Done():
            log.Printf("client disconnected: %s", r.URL.Path)
            conn.Close() // goroutineのio.CopyBufferを強制終了
        case <-done:
            log.Printf("stream ended: %s", r.URL.Path)
        }
	}
}

func splitPath(path string) []string {
	parts := []string{}
	current := ""
	for _, c := range path {
		if c == '/' {
			if current != "" {
				parts = append(parts, current)
			}
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
