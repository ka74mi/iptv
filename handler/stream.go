package handler

import (
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
	"context"

	"github.com/ka74mi/iptv/api"
)

// StreamHandler はHTTPリクエストを受けてEDCBからTSストリームを転送するハンドラ。
//
// URLパス形式: /stream/{onid}/{tsid}/{sid}
//
// 【EDCBストリーム配信の全体シーケンス】
//  1. SendNwTVMode(2)    - TCP送信モードをグローバル設定
//  2. SendNwTVIDSetCh()  - チューナー起動、EpgDataCap_BonのプロセスIDを取得
//  3. OpenViewStream()   - SrvPipeストリームのTCP接続確立（リトライ必須）
//  4. io.CopyBuffer()    - TSデータをHTTPクライアントに転送
//  5. conn.Close()       - SrvPipe接続を先に切断
//  6. SendNwTVIDClose()  - チューナー終了（必ずconn.Closeの後）
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

		// EDCBのグローバルTCP送信モードを設定する。
		// use_bon_ch=0（デフォルト）運用ではこの設定がないと
		// EpgDataCap_Bonが -nonw フラグ付きで起動されTCP出力が無効になる。
		if err := client.SendNwTVMode(2); err != nil {
			log.Printf("SendNwTVMode error: %v", err)
			http.Error(w, "failed to set nwtv mode: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// チューナーを起動してプロセスIDを取得する。
		// 戻り値はEpgDataCap_BonのプロセスID（NetworkTV IDではない）。
		processID, err := client.SendNwTVIDSetCh(uint16(onid), uint16(tsid), uint16(sid))
		log.Printf("SendNwTVIDSetCh: processID=%d err=%v", processID, err)
		if err != nil {
			http.Error(w, "failed to set ch: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// SrvPipeストリームに接続する。
		// SetCh成功直後はEpgDataCap_BonがFIFOを準備中のため即座には成功しない。
		// r.Context()ベースのリトライで、クライアント切断時は即座に停止する。
		var conn net.Conn
		{
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			for {
				conn, err = client.OpenViewStream(processID)
				log.Printf("OpenViewStream: processID=%d err=%v", processID, err)
				if err == nil {
					break
				}
				select {
				case <-ctx.Done():
					http.Error(w, "stream unavailable: "+ctx.Err().Error(), http.StatusServiceUnavailable)
					return
				case <-time.After(200 * time.Millisecond):
					// リトライ
				}
			}
		}
		if err != nil {
			http.Error(w, "failed to open stream: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// クリーンアップのdefer順序は重要。
		// deferは後入れ先出しなので、先にSendNwTVIDCloseをdeferし、
		// 後にconn.Closeをdeferすることで:
		//   実行順: conn.Close → SendNwTVIDClose
		// SrvPipeを切断してからチューナー終了コマンドを送ることで
		// EDCBが正常にチューナーを解放できる。
		//
		// 【SendNwTVIDCloseに渡す値】
		// use_bon_ch=0, space_or_id=0 の運用では 0 を渡す。
		// EpgDataCap_BonのプロセスIDを渡してはいけない。
		defer func() {
			if err := client.SendNwTVIDClose(0); err != nil {
				log.Printf("SendNwTVIDClose error: %v", err)
			} else {
				log.Printf("SendNwTVIDClose success")
			}
		}()
		defer conn.Close()

		ctx := r.Context()
		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		buf := make([]byte, 188*256) // TSパケット(188byte)×256
		done := make(chan struct{})
		go func() {
			defer close(done)
			n, err := io.CopyBuffer(w, conn, buf)
			log.Printf("io.CopyBuffer done: n=%d err=%v", n, err)
		}()

		select {
		case <-ctx.Done():
			// クライアントが切断した場合。
			// conn.Closeでio.CopyBufferのブロックを解除する。
			// deferのconn.Closeと二重になるが問題ない。
			log.Printf("client disconnected: %s", r.URL.Path)
			conn.Close()
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
